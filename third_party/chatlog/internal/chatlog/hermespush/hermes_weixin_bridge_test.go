package hermespush

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPythonFromShebangRejectsShellLauncher(t *testing.T) {
	if got := pythonFromShebang("#!/usr/bin/env bash\nexec /tmp/hermes"); got != "" {
		t.Fatalf("shell launcher was treated as Python: %q", got)
	}
}

func TestPythonFromShebangAcceptsPython(t *testing.T) {
	tests := map[string]string{
		"#!/opt/hermes/venv/bin/python3\n": "/opt/hermes/venv/bin/python3",
		"#!/usr/bin/env python3\n":         "python3",
	}
	for script, want := range tests {
		if got := pythonFromShebang(script); got != want {
			t.Fatalf("pythonFromShebang(%q) = %q, want %q", script, got, want)
		}
	}
}

func TestDiscoverHermesPythonFallsBackPastShellLauncher(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(binDir, "hermes")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	pythonBin := filepath.Join(home, ".hermes", "hermes-agent", "venv", "bin", "python3")
	if err := os.MkdirAll(filepath.Dir(pythonBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonBin, []byte("python fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", binDir)

	gotPython, gotRoot, err := discoverHermesPython()
	if err != nil {
		t.Fatal(err)
	}
	if gotPython != pythonBin {
		t.Fatalf("python = %q, want %q", gotPython, pythonBin)
	}
	wantRoot := filepath.Join(home, ".hermes", "hermes-agent")
	if gotRoot != wantRoot {
		t.Fatalf("Hermes root = %q, want %q", gotRoot, wantRoot)
	}
}
