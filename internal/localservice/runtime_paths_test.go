package localservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimePathsMatchPythonIdentityHash(t *testing.T) {
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	paths, err := runtimePathsForScope(identity, []string{"win32", "UNIT"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths.Registry) != "local_service_9e7b60c087d0a575f0b1.json" ||
		filepath.Base(paths.Bootstrap) != "local_service_9e7b60c087d0a575f0b1.bootstrap.json" ||
		filepath.Base(paths.StartupLock) != "local_service_9e7b60c087d0a575f0b1.startup.lock" {
		t.Fatalf("runtime path differs from Python: %+v", paths)
	}
}

func TestCurrentHostRuntimePathAgainstPython(t *testing.T) {
	expected := os.Getenv("UM_MEM_TEST_PYTHON_RUNTIME_KEY")
	if expected == "" {
		t.Skip("Python current-host runtime key is unavailable")
	}
	identity := Identity{RepoRoot: filepath.Join("..", ".."), WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	paths, err := Paths(identity)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(paths.Registry) != "local_service_"+expected+".json" ||
		!strings.HasSuffix(paths.Bootstrap, ".bootstrap.json") {
		t.Fatalf("current host runtime path differs from Python: %+v", paths)
	}
}

func TestSLOPathFollowsRegistryAndSeparatesBackend(t *testing.T) {
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	runtimeFiles, err := Paths(identity)
	if err != nil {
		t.Fatal(err)
	}
	path, err := SLOPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	stem := strings.TrimSuffix(filepath.Base(runtimeFiles.Registry), ".json")
	if filepath.Dir(path) != filepath.Dir(runtimeFiles.Registry) ||
		!strings.HasPrefix(filepath.Base(path), stem+".") ||
		!strings.HasSuffix(path, ".slo.sqlite") {
		t.Fatalf("service SLO path does not follow the listener registry: %s", path)
	}
	other := identity
	other.Backend = "mnemosyne"
	otherPath, err := SLOPath(other)
	if err != nil || otherPath == path {
		t.Fatalf("service SLO backend identity was not isolated: %s, %v", otherPath, err)
	}
}

func TestCurrentHostSLOPathAgainstPython(t *testing.T) {
	expected := os.Getenv("UM_MEM_TEST_PYTHON_SLO_PATH")
	if expected == "" {
		t.Skip("Python current-host SLO path is unavailable")
	}
	identity := Identity{RepoRoot: filepath.Join("..", ".."), WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	path, err := SLOPath(identity)
	if err != nil || !strings.EqualFold(path, expected) {
		t.Fatalf("service SLO path differs from Python: %s, %v; want %s", path, err, expected)
	}
}

func TestBootstrapReuseAndRegistryLifecycle(t *testing.T) {
	root := os.Getenv("UM_MEM_TEST_GO_SERVICE_ROOT")
	if root == "" {
		root = t.TempDir()
	} else if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	secrets, err := LoadOrCreateBootstrap(identity)
	if err != nil {
		t.Fatal(err)
	}
	roles, err := roleNames()
	if err != nil || len(secrets) != len(roles) {
		t.Fatalf("local service bootstrap has wrong roles: %d, %v", len(secrets), err)
	}
	again, err := LoadOrCreateBootstrap(identity)
	if err != nil || again["operator"] != secrets["operator"] {
		t.Fatalf("local service bootstrap was replaced: %v", err)
	}
	paths, err := writeRegistry(identity, 48129, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeRegistry(identity, 48130, 1234); err == nil {
		t.Fatal("local service registry was overwritten")
	}
	if err := clearRegistry(paths, os.Getpid()+1, 48129); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Registry); err != nil {
		t.Fatal("foreign process cleared local service registry")
	}
	if err := clearRegistry(paths, os.Getpid(), 48129); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.Registry); !os.IsNotExist(err) {
		t.Fatal("local service registry survived owner cleanup")
	}
	wrongBackend := identity
	wrongBackend.Backend = "zvec"
	if _, err := LoadOrCreateBootstrap(wrongBackend); err == nil {
		t.Fatal("local service bootstrap accepted another selected backend")
	}
}
