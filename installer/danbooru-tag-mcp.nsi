; danbooru-tag-mcp - NSIS installer for Windows
;
; Usage:
;   makensis /DAPP_VERSION=0.1.0 [/DAPP_ARCH=arm64] installer/danbooru-tag-mcp.nsi
;
; Output:
;   dist/danbooru-tag-mcp-windows-<arch>-setup.exe
;
; Design:
;   - Per-user install to %LOCALAPPDATA%\Programs\danbooru-tag-mcp (no UAC,
;     no admin), matching install.ps1's default directory.
;   - Environment config (PATH) is delegated to danbooru-tag-mcp.exe itself,
;     which silently registers its own directory into the user PATH on every
;     startup (env.EnsureUserPath, idempotent). The installer just drops the
;     exe and runs it once.
;   - The installer stub itself is x86; on ARM64 Windows it runs under the
;     built-in x86 emulation, while the exe it drops is the native
;     ${APP_ARCH} build.
;
; Path note: NSIS resolves File paths relative to this script's directory
; (installer/), so the built exe is referenced as "..\dist\<arch>\danbooru-tag-mcp.exe".

Unicode true
ManifestDPIAware true

; Version injected from the command line: /DAPP_VERSION=x.y.z
!ifndef APP_VERSION
  !define APP_VERSION "0.0.0"
!endif

; Target CPU arch injected from the command line: /DAPP_ARCH=amd64|arm64
!ifndef APP_ARCH
  !define APP_ARCH "amd64"
!endif

Name "danbooru-tag-mcp ${APP_VERSION}"
OutFile "..\dist\danbooru-tag-mcp-windows-${APP_ARCH}-setup.exe"
InstallDir "$LOCALAPPDATA\Programs\danbooru-tag-mcp"
; Reuse a previous install directory (own upgrade, or an install.ps1 install,
; which writes the same registry value)
InstallDirRegKey HKCU "Software\danbooru-tag-mcp" "InstallDir"
RequestExecutionLevel user
ShowInstDetails show
ShowUnInstDetails show
BrandingText "danbooru-tag-mcp ${APP_VERSION}"

; ---------- MUI2 (modern UI) ----------
!include "MUI2.nsh"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_NOAUTOCLOSE
!define MUI_UNFINISHPAGE_NOAUTOCLOSE

; Installer pages
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_COMPONENTS
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

; Uninstaller pages
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; Languages (English first = default)
!insertmacro MUI_LANGUAGE "English"

; ---------- Section: core files (required) ----------
Section "danbooru-tag-mcp executable (required)" SecCore
  SectionIn RO
  SetOutPath "$INSTDIR"
  File "..\dist\${APP_ARCH}\danbooru-tag-mcp.exe"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; Remember install location (reused on upgrade)
  WriteRegStr HKCU "Software\danbooru-tag-mcp" "InstallDir" "$INSTDIR"

  ; Add/Remove Programs entry (per-user, HKCU)
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "DisplayName" "danbooru-tag-mcp - Danbooru tag lookup MCP server"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "Publisher" "BaixuanZhu"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "URLInfoAbout" "https://github.com/BaixuanZhu/danbooru-tag-mcp"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "DisplayIcon" '"$INSTDIR\danbooru-tag-mcp.exe"'
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp" "NoRepair" 1
SectionEnd

; ---------- Section: register the user PATH (recommended) ----------
Section "Register the user PATH (recommended)" SecConfig
  ; Run the exe once to trigger its built-in bootstrap: it appends $INSTDIR
  ; to the user PATH (registry HKCU\Environment) silently and idempotently,
  ; so MCP client configs can use the bare command name.
  ; nsExec avoids a flashing console window.
  nsExec::ExecToLog '"$INSTDIR\danbooru-tag-mcp.exe" version'
SectionEnd

; ---------- Section: Start Menu shortcut to uninstaller ----------
Section "Start Menu uninstall shortcut" SecShortcut
  CreateDirectory "$SMPROGRAMS\danbooru-tag-mcp"
  CreateShortcut "$SMPROGRAMS\danbooru-tag-mcp\Uninstall danbooru-tag-mcp.lnk" "$INSTDIR\uninstall.exe"
SectionEnd

; ---------- Section descriptions (hover tooltips) ----------
!insertmacro MUI_FUNCTION_DESCRIPTION_BEGIN
  !insertmacro MUI_DESCRIPTION_TEXT ${SecCore}     "danbooru-tag-mcp.exe and its uninstaller (required)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecConfig}   "Add the install directory to the user PATH so MCP clients can use the bare command name (recommended)"
  !insertmacro MUI_DESCRIPTION_TEXT ${SecShortcut} "Create an uninstall shortcut under Start Menu > danbooru-tag-mcp"
!insertmacro MUI_FUNCTION_DESCRIPTION_END

; ---------- Uninstaller ----------
Section "Uninstall"
  ; Files (".bak" is left behind by a self-upgrade that could not delete the
  ; old exe while its process was still running)
  Delete "$INSTDIR\danbooru-tag-mcp.exe"
  Delete "$INSTDIR\danbooru-tag-mcp.exe.bak"
  Delete "$INSTDIR\uninstall.exe"

  ; Start Menu
  Delete "$SMPROGRAMS\danbooru-tag-mcp\Uninstall danbooru-tag-mcp.lnk"
  RMDir "$SMPROGRAMS\danbooru-tag-mcp"

  ; Registry entries
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\danbooru-tag-mcp"
  DeleteRegKey HKCU "Software\danbooru-tag-mcp"

  DetailPrint "Uninstall complete."

  ; Remove the (now empty) install directory
  RMDir "$INSTDIR"
SectionEnd
