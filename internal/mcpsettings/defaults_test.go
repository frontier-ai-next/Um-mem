package mcpsettings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultPathsFollowHostHomesAndKeepDataOutsideCheckout(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	t.Setenv("HERMES_HOME", filepath.Join(root, "hermes"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	}
	want := map[string]string{
		"codex":  filepath.Join(root, "codex", "config.toml"),
		"hermes": filepath.Join(root, "hermes", "config.yaml"),
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	want["claude"] = filepath.Join(home, ".claude.json")
	for host, expected := range want {
		path, err := DefaultSettingsPath(host)
		if err != nil || path != expected {
			t.Fatalf("%s settings path = %q, %v; want %q", host, path, err, expected)
		}
	}
	if _, err := DefaultSettingsPath("unknown"); err == nil {
		t.Fatal("unknown MCP host was accepted")
	}
	path, err := DefaultRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(root, "data", "um-mem", "store")
	if runtime.GOOS == "windows" {
		expected = filepath.Join(root, "local", "UM-MEM", "store")
	}
	if path != expected {
		t.Fatalf("default memory store = %q, want %q", path, expected)
	}
}
