package engine

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestConsolidateObservationsKeepsVerifierProofAndRawSources(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	sourceIDs := []string{}
	witnessIDs := []string{}
	for _, item := range []struct{ id, claim string }{{"episode-1", "Session established the first outcome."},
		{"episode-2", "Session established the second outcome."}} {
		observed, err := selected.ObserveHost(ctx, HostObservation{EventID: item.id,
			EventKind: "session_episode", RawEvidence: "raw session evidence for " + item.id,
			AtomicClaim: item.claim, ObservedAt: "2026-07-30T12:00:00Z",
			ProducerID: "host:lifecycle"}, HostLineage{SessionID: "session-1"})
		if err != nil {
			t.Fatal(err)
		}
		witnessID := observed["witness_id"].(string)
		retained, err := selected.RememberCandidate(ctx, CandidateRequest{Content: item.claim,
			Title: item.claim, RequestedBlockID: "stable_project_facts", SessionID: "session-1",
			EvidenceRefs: observed["evidence_refs"].([]memory.EvidenceRef),
			Metadata:     map[string]any{"raw_witness_id": witnessID}})
		if err != nil || !retained.Saved {
			t.Fatalf("source setup failed: %+v, %v", retained, err)
		}
		sourceIDs = append(sourceIDs, retained.RecordID)
		witnessIDs = append(witnessIDs, witnessID)
	}
	general, err := ledger.AppendVerificationReport(root, "alpha", "verifier:lifecycle",
		"sha256:lifecycle-fixture", "memory_promotion", root, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	content, title := "The two host-observed session outcomes form one bounded episode.", "Controlled lifecycle summary"
	semantic, err := ledger.AppendConsolidationSemanticVerification(ledger.ConsolidationSemanticVerification{
		RepoRoot: root, WorkspaceID: "alpha", Subject: ledger.ConsolidationSemanticSubject{
			SourceIDs: sourceIDs, Content: content, Title: title, RequestedBlockID: "stable_project_facts"},
		VerifierID: "verifier:lifecycle-semantic", VerifierModelVersion: "independent-reader-v1",
		ProposerModelVersion: "deterministic-host-proposer-v1", Coverage: true,
		Preservation: true, Faithfulness: true, Reasons: []string{"both sources preserved"}})
	if err != nil {
		t.Fatal(err)
	}
	request := ConsolidateObservationsRequest{SourceIDs: []string{sourceIDs[1], sourceIDs[0]},
		Content: content, Title: title, Reason: "controlled session boundary",
		VerificationReports:        []ledger.VerificationReport{general},
		SemanticVerificationReport: semantic, RequestedBlockID: "stable_project_facts",
		SessionID: "session-1"}
	native := selected.projector
	selected.projector = interruptedPublicationProjector{}
	if _, err := selected.ConsolidateObservations(ctx, request); err == nil {
		t.Fatal("interrupted native projection was treated as a committed consolidation")
	}
	selected.projector = native
	result, err := selected.ConsolidateObservations(ctx, request)
	if err != nil || result["ok"] != true || result["route_outcome"] != "consolidated" {
		t.Fatalf("consolidation failed: %+v, %v", result, err)
	}
	recordID := result["memory_id"].(string)
	record, err := selected.ReadStored(ctx, recordID)
	if err != nil || record == nil || record.Status != "candidate" || record.Authority != "advisory" ||
		record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID != "stable_project_facts" {
		t.Fatalf("consolidation record is not canonical: %+v, %v", record, err)
	}
	edges, err := selected.index.EdgesFor(ctx, recordID)
	if err != nil {
		t.Fatal(err)
	}
	linked := map[string]bool{}
	for _, edge := range edges {
		if edge.SrcID == recordID && edge.EdgeType == "derived_from" {
			linked[edge.DstID] = true
		}
	}
	if !linked[sourceIDs[0]] || !linked[sourceIDs[1]] || len(linked) != 2 {
		t.Fatalf("consolidation lost raw source provenance: %+v", edges)
	}
	for _, witnessID := range witnessIDs {
		witness, err := selected.ReadStored(ctx, witnessID)
		if err != nil || witness == nil || witness.DeliveryPolicy != "hidden" {
			t.Fatalf("raw witness was lost: %+v, %v", witness, err)
		}
	}
	receipt, err := selected.index.ConsolidationReceipt(ctx,
		result["consolidation_receipt"].(memory.ConsolidationCommitReceipt).ReceiptID)
	if err != nil || receipt == nil || len(receipt.CommittedIDs) != 1 || receipt.CommittedIDs[0] != recordID {
		t.Fatalf("canonical consolidation receipt is missing: %+v, %v", receipt, err)
	}
	replayed, err := selected.ConsolidateObservations(ctx, request)
	if err != nil || replayed["replayed"] != true || replayed["memory_id"] != recordID {
		t.Fatalf("consolidation replay failed: %+v, %v", replayed, err)
	}
	changed := request
	changed.Content = "A different summary without matching semantic verification."
	if _, err := selected.ConsolidateObservations(ctx, changed); err == nil {
		t.Fatal("semantic report was accepted for a different consolidation proposal")
	}
	bare, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "An unsupported semantic note",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !bare.Saved {
		t.Fatalf("bare source setup failed: %+v, %v", bare, err)
	}
	unbound := request
	unbound.SourceIDs = []string{sourceIDs[0], bare.RecordID}
	if _, err := selected.ConsolidateObservations(ctx, unbound); err == nil {
		t.Fatal("source without typed raw-witness provenance was consolidated")
	}
}
