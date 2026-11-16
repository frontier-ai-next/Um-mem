package ledger

import (
	"strings"
	"testing"
)

func TestConsolidationSemanticReportMatchesPythonAndBindsProposal(t *testing.T) {
	root := t.TempDir()
	subject := ConsolidationSemanticSubject{SourceIDs: []string{"source-b", "source-a"},
		Content: "Keep port 4318", Title: "Release port", RequestedBlockID: "stable_project_facts"}
	hash, err := subject.Hash()
	if err != nil || hash != "d0facaec8dd714c9fc29327e8e88c7c32cf69eb8a143f43fdee844711ce36988" {
		t.Fatalf("consolidation subject hash differs from Python: %s, %v", hash, err)
	}
	request := ConsolidationSemanticVerification{RepoRoot: root, WorkspaceID: "ws",
		Subject: subject, VerifierID: "reader", VerifierModelVersion: "reader-v1",
		ProposerModelVersion: "deterministic-host-proposer-v1", Coverage: true,
		Preservation: true, Faithfulness: true, Reasons: []string{"checked exact sources"}}
	report, err := AppendConsolidationSemanticVerification(request)
	if err != nil {
		t.Fatal(err)
	}
	if report.ReportHash != "d625bb725ba748be27b34df4ce9cb5b9b58182caf9559a50c69a858ffcb688aa" ||
		!report.Passed {
		t.Fatalf("consolidation report differs from Python: %+v", report)
	}
	resolver, err := OpenResolver(root, "ws")
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := resolver.ValidateConsolidationSemanticReport(report, subject,
		"deterministic-host-proposer-v1", root, nil)
	if err != nil || !semantic.IndependentFromProposer || !semantic.Coverage ||
		!semantic.Preservation || !semantic.Faithfulness || len(semantic.EvidenceRefs) != 1 {
		t.Fatalf("valid dedicated report was rejected: %+v, %v", semantic, err)
	}
	changed := subject
	changed.Content = "Keep port 9999"
	if _, err := resolver.ValidateConsolidationSemanticReport(report, changed,
		"deterministic-host-proposer-v1", root, nil); err == nil ||
		!strings.Contains(err.Error(), "not bound") {
		t.Fatalf("report accepted a different consolidation subject: %v", err)
	}
	forged := report
	forged.ReportHash = strings.Repeat("0", 64)
	if _, err := resolver.ValidateConsolidationSemanticReport(forged, subject,
		"deterministic-host-proposer-v1", root, nil); err == nil ||
		!strings.Contains(err.Error(), "hash") {
		t.Fatalf("forged semantic hash was accepted: %v", err)
	}
}

func TestConsolidationSemanticReportRejectsSameModelAndFailedAxis(t *testing.T) {
	root := t.TempDir()
	subject := ConsolidationSemanticSubject{SourceIDs: []string{"a", "b"},
		Content: "release note", Title: "Release", RequestedBlockID: "facts"}
	for _, test := range []struct {
		name                    string
		sameModel, faithfulness bool
	}{
		{"same model", true, true}, {"failed faithfulness", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifierModel := "independent-v1"
			if test.sameModel {
				verifierModel = "proposer-v1"
			}
			report, err := AppendConsolidationSemanticVerification(ConsolidationSemanticVerification{
				RepoRoot: root, WorkspaceID: "ws", Subject: subject, VerifierID: "reader",
				VerifierModelVersion: verifierModel, ProposerModelVersion: "proposer-v1",
				Coverage: true, Preservation: true, Faithfulness: test.faithfulness,
				Reasons: []string{"checked sources"}})
			if err != nil {
				t.Fatal(err)
			}
			if report.Passed {
				t.Fatal("unsafe semantic report claimed a pass")
			}
			resolver, err := OpenResolver(root, "ws")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := resolver.ValidateConsolidationSemanticReport(report, subject,
				"proposer-v1", root, nil); err == nil {
				t.Fatal("unsafe semantic report passed validation")
			}
		})
	}
}
