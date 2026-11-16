package localservice

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWorkQueuePathIdentityIsolation(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	path, err := WorkQueuePath(identity)
	if err != nil || !strings.HasSuffix(path, ".work.sqlite") {
		t.Fatalf("queue path: %s, %v", path, err)
	}
	other := identity
	other.Backend = "hindsight"
	otherPath, err := WorkQueuePath(other)
	if err != nil || otherPath == path {
		t.Fatalf("backend did not isolate queue path: %s, %v", otherPath, err)
	}
	identity.RepoRoot = filepath.Join(root, ".")
	aliasPath, err := WorkQueuePath(identity)
	if err != nil || aliasPath != path {
		t.Fatalf("equivalent path changed queue identity: %s, %v", aliasPath, err)
	}
}

func TestWorkQueuePathPythonCurrentWorkspaceFixture(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fixture uses the Windows workspace root")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil || !strings.EqualFold(root, `C:\Users\admin\Documents\UM MEM Gitlab`) {
		t.Skip("fixture belongs to the source workspace")
	}
	path, err := WorkQueuePath(Identity{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mempalace"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "local_service_72788d7b10a8e0c32bca.cc4e97275781282893af.work.sqlite" {
		t.Fatalf("queue path differs from Python: %s", path)
	}
}
