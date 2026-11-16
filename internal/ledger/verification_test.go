package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceAndDiffHashesAndVerificationReport(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("passed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".memory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".memory", "ignored.txt"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	firstHash, err := WorkspaceHash(root)
	if err != nil || firstHash != "11c989c76471f8904cb3f9298e5e12971de431b3a6617c20473af0b701570902" {
		t.Fatalf("workspace hash differs from Python: %q, %v", firstHash, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".memory", "ignored.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	secondHash, err := WorkspaceHash(root)
	if err != nil || secondHash != firstHash {
		t.Fatalf("generated memory changed workspace hash: %q, %v", secondHash, err)
	}
	diffHash, err := DiffHash(root, []string{"result.txt", "missing.txt", ".memory/ignored.txt", "../escape.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if diffHash != "5480c8cdfdbc6c23312198595b286835d440eed58985f6d6c9d3a075c0c58930" {
		t.Fatalf("diff hash differs from Python: %q", diffHash)
	}
	report, err := AppendVerificationReport(root, "alpha", "trusted-host", "sha256:result",
		"host_observation:test-1", root, []string{"result.txt"}, true)
	if err != nil || report.WorkspaceHash != firstHash || report.ReportID == "" ||
		report.DiffHash == "" || !report.Passed {
		t.Fatalf("verification report differs: %+v, %v", report, err)
	}
	prior, err := FindVerificationReport(root, "alpha", "host_observation:test-1",
		"trusted-host", "sha256:result", true)
	if err != nil || prior == nil || *prior != report {
		t.Fatalf("prior verification report was not reusable: %+v, %v", prior, err)
	}
	if _, err := FindVerificationReport(root, "alpha", "host_observation:test-1",
		"trusted-host", "sha256:changed", true); err == nil {
		t.Fatal("changed report hash reused a prior verifier event")
	}
	path, err := Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	line, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(line), "\n") != 1 {
		t.Fatalf("unexpected report count: %q", line)
	}
	var row map[string]any
	if err := json.Unmarshal(line, &row); err != nil {
		t.Fatal(err)
	}
	expectedResultHash, err := HashValue(map[string]any{"report_hash": report.ReportHash,
		"passed": report.Passed, "workspace_hash": report.WorkspaceHash, "diff_hash": report.DiffHash})
	if err != nil || row["result_hash"] != expectedResultHash {
		t.Fatalf("typed report result hash differs: %+v, %v", row, err)
	}
}

func TestDiffHashSkipsMissingFileBeyondSymlinkBoundary(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "outside")); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	empty, err := DiffHash(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := DiffHash(root, []string{"outside/missing.txt"})
	if err != nil || actual != empty {
		t.Fatalf("missing path through external symlink entered diff hash: %s != %s, %v", actual, empty, err)
	}
}
