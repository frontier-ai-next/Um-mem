package ledger

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestResolverBindsReportToLedgerAndWorkspace(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "result.txt")
	if err := os.WriteFile(file, []byte("passed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := AppendVerificationReport(root, "alpha", "verifier-1", "report-hash",
		"stage-1", root, []string{"result.txt"}, true)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := OpenResolver(root, "alpha")
	if err != nil || resolver.Row(report.ReportID) == nil || resolver.Row(report.LedgerHash) == nil {
		t.Fatalf("resolver lost report event identities: %v", err)
	}
	mutable := resolver.Row(report.ReportID)
	mutable["result_hash"] = "forged"
	mutable["result"].(map[string]any)["verifier_id"] = "forged"
	if original := resolver.Row(report.ReportID); original["result_hash"] == "forged" ||
		original["result"].(map[string]any)["verifier_id"] == "forged" {
		t.Fatal("resolver exposed mutable cached proof")
	}
	issues, err := resolver.ValidateVerificationReport(report, root, []string{"result.txt"}, false)
	if err != nil || len(issues) != 0 {
		t.Fatalf("valid report was rejected: %v, %v", issues, err)
	}
	wrong := report
	wrong.VerifierID = "other-verifier"
	issues, err = resolver.ValidateVerificationReport(wrong, root, []string{"result.txt"}, false)
	if err != nil || !slices.Contains(issues, "verification_report_verifier_mismatch") {
		t.Fatalf("forged verifier was accepted: %v, %v", issues, err)
	}
	if err := os.WriteFile(file, []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	issues, err = resolver.ValidateVerificationReport(report, root, []string{"result.txt"}, false)
	if err != nil || !slices.Contains(issues, "proof_stale_rerun_required") {
		t.Fatalf("changed workspace accepted stale report: %v, %v", issues, err)
	}
	ref := memory.EvidenceRef{RefType: "verification_report", RefID: report.ReportID,
		ProducedBy: "verifier", ProducerID: "verifier-1", Hash: &report.LedgerHash}
	if got := resolver.ValidateRef(ref); len(got) != 0 {
		t.Fatalf("valid ledger reference was rejected: %v", got)
	}
	badHash := "forged"
	ref.Hash = &badHash
	if got := resolver.ValidateRef(ref); !slices.Contains(got, "evidence_hash_mismatch") {
		t.Fatalf("forged ledger hash was accepted: %v", got)
	}
}

func TestResolverOrdersEvidenceAndRejectsDuplicateIdentity(t *testing.T) {
	root := t.TempDir()
	first, err := Append(root, "alpha", "supervisor", "stage-1", "observe",
		map[string]any{"value": "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Append(root, "alpha", "verifier", "stage-2", "test_run",
		map[string]any{"value": "second"})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := OpenResolver(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	ref := memory.EvidenceRef{RefType: "test_run", RefID: second.ID,
		Hash: &second.Hash, ProducedBy: "verifier", ProducerID: "verifier-1",
		CreatedAfterEvent: &first.ID}
	if issues := resolver.ValidateRef(ref); len(issues) != 0 {
		t.Fatalf("ordered proof was rejected: %v", issues)
	}
	ref.RefID, ref.Hash, ref.CreatedAfterEvent = first.ID, &first.Hash, &second.ID
	ref.ProducedBy = "supervisor"
	if issues := resolver.ValidateRef(ref); !slices.Contains(issues, "stale_proof_ref") {
		t.Fatalf("older proof passed a newer-event requirement: %v", issues)
	}
	path, err := Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boundary := slices.Index(content, byte('\n'))
	if boundary < 0 {
		t.Fatal("ledger event has no line terminator")
	}
	firstLine := content[:boundary+1]
	if err := os.WriteFile(path, append(content, firstLine...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenResolver(root, "alpha"); err == nil {
		t.Fatal("duplicate supervisor event identity was accepted")
	}
}
