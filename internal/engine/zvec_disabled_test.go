//go:build !purego

package engine

import "testing"

func TestZvecRequiresNativeBuild(t *testing.T) {
	_, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "zvec"})
	if err == nil {
		t.Fatal("Zvec was advertised without its native Go build")
	}
}
