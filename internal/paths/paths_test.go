package paths

import (
	"path/filepath"
	"testing"
)

func TestNormalizeWorkspaceID(t *testing.T) {
	cases := []struct {
		input string
		want  string
		err   bool
	}{
		{"", "", false},
		{"./workspaces/alpha/.memory", "alpha", false},
		{`.workspaces\alpha`, "alpha", false},
		{"workspaces/Alpha Beta/ignored", "Alpha_Beta", false},
		{"..", "", true},
		{"workspaces/..", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeWorkspaceID(tc.input)
		if got != tc.want || (err != nil) != tc.err {
			t.Errorf("NormalizeWorkspaceID(%q) = %q, %v; want %q, error=%v", tc.input, got, err, tc.want, tc.err)
		}
	}
}

func TestWorkspaceMemoryRootIsolation(t *testing.T) {
	store := t.TempDir()
	manager, err := WorkspaceMemoryRoot(store, ManagerWorkspaceID)
	if err != nil || manager != filepath.Join(store, ".UM-MEM", "memory") {
		t.Fatalf("manager root = %q, %v", manager, err)
	}
	alpha, err := WorkspaceMemoryRoot(store, "alpha")
	if err != nil || alpha != filepath.Join(store, "workspaces", "alpha", ".memory") {
		t.Fatalf("workspace root = %q, %v", alpha, err)
	}
	if alpha == manager {
		t.Fatal("manager and workspace storage must be isolated")
	}
	if _, err := WorkspaceMemoryRoot(store, "../escape"); err == nil {
		t.Fatal("path traversal was accepted")
	}
}
