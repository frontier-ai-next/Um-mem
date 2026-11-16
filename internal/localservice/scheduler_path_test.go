package localservice

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSchedulerPathIdentityAndRoleIndependence(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	path, digest, err := SchedulerPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 || !strings.Contains(filepath.Base(path), "."+digest[:20]+".scheduler.sqlite") {
		t.Fatalf("invalid scheduler identity path: %s %s", path, digest)
	}
	other := identity
	other.Backend = "zvec"
	otherPath, otherDigest, err := SchedulerPath(other)
	if err != nil || otherPath == path || otherDigest == digest {
		t.Fatalf("backend did not isolate scheduler state: %s %s %v", otherPath, otherDigest, err)
	}
	identity.RepoRoot = filepath.Join(root, ".")
	aliasPath, aliasDigest, err := SchedulerPath(identity)
	if err != nil || aliasPath != path || aliasDigest != digest {
		t.Fatalf("equivalent root changed scheduler identity: %s %s %v", aliasPath, aliasDigest, err)
	}
}

func TestSchedulerPathPythonCurrentWorkspaceFixture(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fixture uses the Windows workspace root")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil || !strings.EqualFold(root, `C:\Users\admin\Documents\UM MEM Gitlab`) {
		t.Skip("fixture belongs to the source workspace")
	}
	path, digest, err := SchedulerPath(Identity{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mempalace"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "local_service_72788d7b10a8e0c32bca.a93a45bbc4d395db2857.scheduler.sqlite" ||
		digest != "a93a45bbc4d395db28577eb2520009cf25387f0efaacc95985acbece71edf751" {
		t.Fatalf("scheduler path differs from Python: %s %s", path, digest)
	}
}
