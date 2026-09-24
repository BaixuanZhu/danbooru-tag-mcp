package env

import (
	"os"
	"os/exec"
	"testing"
)

// TestPersistRoundtrip verifies the Persist -> readUserEnv registry roundtrip.
// Real registry operations have external side effects, so it only runs with
// DANBOORU_MCP_TEST_REGISTRY=1; it writes a throwaway named value and deletes
// it afterwards, never touching the user PATH.
func TestPersistRoundtrip(t *testing.T) {
	if os.Getenv("DANBOORU_MCP_TEST_REGISTRY") == "" {
		t.Skip("requires DANBOORU_MCP_TEST_REGISTRY=1 to avoid touching the real registry in daily test runs")
	}

	const name = "DANBOORU_MCP_TEST_VALUE"
	if err := Persist(name, "cafe value with spaces"); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}
	got, err := readUserEnv(name)
	if err != nil {
		t.Fatalf("readUserEnv failed: %v", err)
	}
	if got != "cafe value with spaces" {
		t.Errorf("roundtrip mismatch: %q", got)
	}

	// Clean up the throwaway value (Persist only writes; deletion goes through reg.exe)
	if out, err := runRegDelete(name); err != nil {
		t.Errorf("failed to clean up the throwaway registry value (please delete HKCU\\Environment\\%s manually): %v\n%s", name, err, out)
	}
}

// runRegDelete deletes a value under HKCU\Environment, for registry test cleanup.
func runRegDelete(valueName string) (string, error) {
	out, err := exec.Command("reg", "delete", `HKCU\Environment`, "/v", valueName, "/f").CombinedOutput()
	return string(out), err
}

func TestSplitPathEntries(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string", "", []string{}},
		{"separators only", ";;;", []string{}},
		{"regular entries", `C:\a;C:\b`, []string{`C:\a`, `C:\b`}},
		{"skips empty entries", `C:\a;;C:\b;`, []string{`C:\a`, `C:\b`}},
		{"blank entry counts as empty", `C:\a; ;C:\b`, []string{`C:\a`, `C:\b`}},
		{"single entry", `C:\only`, []string{`C:\only`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitPathEntries(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitPathEntries(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestContainsEntry(t *testing.T) {
	tests := []struct {
		name string
		path string
		dir  string
		want bool
	}{
		{"empty PATH", "", `C:\x`, false},
		{"exact hit", `C:\a;C:\x;C:\b`, `C:\x`, true},
		{"case-insensitive", `C:\A;C:\X`, `c:\x`, true},
		{"tolerates trailing separator", `C:\a;C:\x\;C:\b`, `C:\x`, true},
		{"dir with trailing separator", `C:\a;C:\x`, `C:\x\`, true},
		{"no hit", `C:\a;C:\b`, `C:\x`, false},
		{"same prefix different dir", `C:\x-other`, `C:\x`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsEntry(tt.path, tt.dir); got != tt.want {
				t.Errorf("containsEntry(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
			}
		})
	}
}
