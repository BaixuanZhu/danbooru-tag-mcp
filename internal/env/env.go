// Package env persists danbooru-tag-mcp's own directory into the user PATH,
// written to the registry at
//
//	HKCU\Environment
//
// It does not shell out to cmd.exe or setx (setx truncates long PATH values,
// a classic pitfall). After writing it broadcasts WM_SETTINGCHANGE so newly
// started processes see the change. This lets MCP client configs reference
// the bare command name "danbooru-tag-mcp" instead of an absolute path.
package env

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	modAdvapi32 = syscall.NewLazyDLL("advapi32.dll")
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegOpenKeyEx           = modAdvapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueEx        = modAdvapi32.NewProc("RegQueryValueExW")
	procRegSetValueEx          = modAdvapi32.NewProc("RegSetValueExW")
	procRegCloseKey            = modAdvapi32.NewProc("RegCloseKey")
	procSendMessageW           = modUser32.NewProc("SendMessageTimeoutW")
	procSetEnvironmentVariable = modKernel32.NewProc("SetEnvironmentVariableW")
)

const (
	HKEY_CURRENT_USER = 0x80000001
	KEY_READ          = 0x20019
	KEY_ALL_ACCESS    = 0xF003F
	REG_EXPAND_SZ     = 2

	HWND_BROADCAST   = 0xFFFF
	WM_SETTINGCHANGE = 0x001A
	SMTO_ABORTIFHUNG = 0x0002
)

// Persist writes a value to HKCU\Environment (REG_EXPAND_SZ, supports variable
// expansion). It also sets the current process's env var and broadcasts
// WM_SETTINGCHANGE to notify other processes.
func Persist(name, value string) error {
	var hKey syscall.Handle
	envKey, _ := syscall.UTF16PtrFromString("Environment")
	r1, _, e := procRegOpenKeyEx.Call(
		uintptr(HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(envKey)),
		0,
		uintptr(KEY_ALL_ACCESS),
		uintptr(unsafe.Pointer(&hKey)),
	)
	if r1 != 0 {
		return fmt.Errorf("failed to open registry key: %w", os.NewSyscallError("RegOpenKeyEx", e))
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	// UTF16FromString appends the trailing \0; size in UTF-16 units (not UTF-8 bytes)
	valueUTF16, err := syscall.UTF16FromString(value)
	if err != nil {
		return fmt.Errorf("value contains invalid characters: %w", err)
	}
	nameUTF16, _ := syscall.UTF16PtrFromString(name)

	r1, _, e = procRegSetValueEx.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(nameUTF16)),
		0,
		uintptr(REG_EXPAND_SZ),
		uintptr(unsafe.Pointer(&valueUTF16[0])),
		uintptr(len(valueUTF16)*2), // byte size including the trailing \0
	)
	if r1 != 0 {
		return fmt.Errorf("failed to write registry value: %w", os.NewSyscallError("RegSetValueEx", e))
	}

	setCurrentProcessEnv(name, value)
	broadcastSettingChange()
	return nil
}

// setCurrentProcessEnv sets the current process's environment variable (UTF-16 interface)
func setCurrentProcessEnv(name, value string) {
	nameUTF16, _ := syscall.UTF16PtrFromString(name)
	valueUTF16, _ := syscall.UTF16PtrFromString(value)
	procSetEnvironmentVariable.Call(
		uintptr(unsafe.Pointer(nameUTF16)),
		uintptr(unsafe.Pointer(valueUTF16)),
	)
}

// broadcastSettingChange broadcasts WM_SETTINGCHANGE so Explorer etc. refresh the environment
func broadcastSettingChange() {
	envUTF16, _ := syscall.UTF16PtrFromString("Environment")
	procSendMessageW.Call(
		uintptr(HWND_BROADCAST),
		uintptr(WM_SETTINGCHANGE),
		0,
		uintptr(unsafe.Pointer(envUTF16)),
		SMTO_ABORTIFHUNG,
		5000,
		0,
	)
}

// readUserEnv reads a value from HKCU\Environment
func readUserEnv(name string) (string, error) {
	var hKey syscall.Handle
	envKey, _ := syscall.UTF16PtrFromString("Environment")
	r1, _, e := procRegOpenKeyEx.Call(
		uintptr(HKEY_CURRENT_USER),
		uintptr(unsafe.Pointer(envKey)),
		0,
		uintptr(KEY_READ),
		uintptr(unsafe.Pointer(&hKey)),
	)
	if r1 != 0 {
		return "", os.NewSyscallError("RegOpenKeyEx", e)
	}
	defer procRegCloseKey.Call(uintptr(hKey))

	nameUTF16, _ := syscall.UTF16PtrFromString(name)
	var bufLen uint32 = 32768
	buf := make([]uint16, bufLen)
	var valueType uint32
	r1, _, e = procRegQueryValueEx.Call(
		uintptr(hKey),
		uintptr(unsafe.Pointer(nameUTF16)),
		0,
		uintptr(unsafe.Pointer(&valueType)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&bufLen)),
	)
	if r1 != 0 {
		return "", os.NewSyscallError("RegQueryValueEx", e)
	}
	return syscall.UTF16ToString(buf), nil
}

// splitPathEntries splits a PATH string, ignoring empty entries.
// Pure function, for table-driven tests.
func splitPathEntries(p string) []string {
	parts := strings.Split(p, ";")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// containsEntry reports whether the PATH string already contains dir
// (case-insensitive, matched after path normalization, tolerating trailing
// separator differences). Pure function, for table-driven tests.
func containsEntry(userPath, dir string) bool {
	want := strings.ToLower(filepath.Clean(dir))
	for _, e := range splitPathEntries(userPath) {
		if strings.ToLower(filepath.Clean(e)) == want {
			return true
		}
	}
	return false
}

// EnsureUserPath ensures the directory containing danbooru-tag-mcp.exe is in
// the user PATH. Called silently on every startup: resolves the exe directory
// via os.Executable(), and appends + persists it if missing. Injected
// automatically on first run; adapts if the user moves the exe later.
// Failures are silently ignored so the MCP main flow is never affected.
func EnsureUserPath() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	exeDir := filepath.Dir(exePath)

	userPath, _ := readUserEnv("PATH")
	if containsEntry(userPath, exeDir) {
		return // already in PATH
	}

	// Append at the end (the tool itself stays low-priority, never displaces existing entries)
	newPath := exeDir
	if strings.TrimSpace(userPath) != "" {
		newPath = userPath + ";" + exeDir
	}
	_ = Persist("PATH", newPath)
}
