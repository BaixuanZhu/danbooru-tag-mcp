package app

import "testing"

func TestBootstrapEnabled(t *testing.T) {
	// Bootstrap is a package-level var; tests must restore it after mutation
	orig := Bootstrap
	defer func() { Bootstrap = orig }()

	tests := []struct {
		name      string
		exe       string
		noBootEnv string
		bootstrap string
		tempDir   string
		want      bool
	}{
		{"normal install location", `C:\Tools\danbooru-tag-mcp\danbooru-tag-mcp.exe`, "", "on", `C:\Users\x\AppData\Local\Temp`, true},
		{"env var disables explicitly", `C:\Tools\app.exe`, "1", "on", `C:\Temp`, false},
		{"blank env var counts as unset", `C:\Tools\app.exe`, "  ", "on", `C:\Temp`, true},
		{"ldflags-injected off (dev build)", `C:\Tools\app.exe`, "", "off", `C:\Temp`, false},
		{"exe under Temp (go run)", `C:\Temp\go-build123\app.exe`, "", "on", `C:\Temp`, false},
		{"exe is the Temp dir itself", `C:\Temp`, "", "on", `C:\Temp`, false},
		{"exe locate failure treated as non-Temp", "", "", "on", `C:\Temp`, true},
		{"case-insensitive", `c:\temp\app.exe`, "", "on", `C:\TEMP`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Bootstrap = tt.bootstrap
			got := bootstrapEnabled(tt.exe, tt.noBootEnv, tt.tempDir)
			if got != tt.want {
				t.Errorf("bootstrapEnabled(%q, %q, %q) = %v, want %v", tt.exe, tt.noBootEnv, tt.tempDir, got, tt.want)
			}
		})
	}
}

func TestIsUnderTemp(t *testing.T) {
	tests := []struct {
		name    string
		exe     string
		tempDir string
		want    bool
	}{
		{"subdirectory", `C:\Temp\go-build1\app.exe`, `C:\Temp`, true},
		{"the dir itself", `C:\Temp`, `C:\Temp`, true},
		{"tempDir with trailing separator", `C:\Temp\app.exe`, `C:\Temp\`, true},
		{"sibling dir does not count", `C:\TempX\app.exe`, `C:\Temp`, false},
		{"different drive", `D:\Tools\app.exe`, `C:\Temp`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnderTemp(tt.exe, tt.tempDir); got != tt.want {
				t.Errorf("isUnderTemp(%q, %q) = %v, want %v", tt.exe, tt.tempDir, got, tt.want)
			}
		})
	}
}
