package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestRollbackRestoresHistoricalRevisionAndDependencies(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	first, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Original project fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !first.Saved {
		t.Fatalf("source write failed: %+v, %v", first, err)
	}
	witness, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Supporting context",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !witness.Saved {
		t.Fatalf("dependency write failed: %+v, %v", witness, err)
	}
	ref := memory.EvidenceRef{RefType: "test_run", RefID: "run-1",
		ProducedBy: "verifier", ProducerID: "test"}
	if _, err := selected.AddEdge(ctx, AddEdgeRequest{SrcID: first.RecordID,
		DstID: witness.RecordID, EdgeType: "derived_from", Weight: json.Number("1.0"),
		Reason: "source support", EvidenceRefs: []memory.EvidenceRef{ref}}); err != nil {
		t.Fatal(err)
	}
	original, err := selected.ReadStored(ctx, first.RecordID)
	if err != nil || original == nil {
		t.Fatal(err)
	}
	changedContent := "Revised project fact"
	changed, err := selected.Amend(ctx, AmendRequest{MemoryID: original.BlockID,
		ExpectedRevision: original.RevisionID, Reason: "new evidence",
		Content: &changedContent, RequestedBlockID: "stable_project_facts"})
	if err != nil || changed["saved"] != true {
		t.Fatalf("amendment failed: %+v, %v", changed, err)
	}
	request := RollbackRequest{MemoryID: changed["new_id"].(string),
		RevisionID: original.RevisionID, Reason: "return to confirmed fact"}
	rolled, err := selected.Rollback(ctx, request)
	if err != nil || rolled["saved"] != true || rolled["rollback_target_id"] != original.BlockID {
		t.Fatalf("rollback failed: %+v, %v", rolled, err)
	}
	id := rolled["block_id"].(string)
	result, err := selected.ReadStored(ctx, id)
	if err != nil || result == nil || result.Content != original.Content ||
		result.RevisionOf != request.MemoryID || result.RecordedAt == original.RecordedAt ||
		result.Metadata["status_reason"] != request.Reason {
		t.Fatalf("rollback did not append historical content: %+v, %v", result, err)
	}
	identity, err := selected.index.MutationIdentity(ctx, id)
	if err != nil || identity == nil || identity.Action != "rollback" ||
		identity.RollbackTargetID != original.BlockID {
		t.Fatalf("rollback mutation identity was not committed: %+v, %v", identity, err)
	}
	edges, err := selected.index.EdgesFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var supersedes, inherited bool
	for _, edge := range edges {
		supersedes = supersedes || edge.EdgeType == "supersedes" && edge.DstID == request.MemoryID
		inherited = inherited || edge.EdgeType == "derived_from" && edge.DstID == witness.RecordID
	}
	if !supersedes || !inherited {
		t.Fatalf("rollback did not restore its provenance: %+v", edges)
	}
	prior, err := selected.ReadStored(ctx, request.MemoryID)
	if err != nil || prior == nil || prior.Content != changedContent {
		t.Fatalf("rollback changed the predecessor: %+v, %v", prior, err)
	}
	replay, err := selected.Rollback(ctx, request)
	if err != nil || replay["block_id"] != id || replay["replayed"] != true {
		t.Fatalf("rollback was not idempotent: %+v, %v", replay, err)
	}
	if _, err := selected.Rollback(ctx, RollbackRequest{MemoryID: id,
		RevisionID: "foreign", Reason: request.Reason}); err == nil {
		t.Fatal("rollback accepted a revision outside canonical history")
	}
}

func TestRollbackRetriesPendingNativeProjection(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Earlier fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	old, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	changedContent := "Newer fact"
	changed, err := selected.Amend(ctx, AmendRequest{MemoryID: old.BlockID,
		ExpectedRevision: old.RevisionID, Reason: "new evidence", Content: &changedContent,
		RequestedBlockID: "stable_project_facts"})
	if err != nil {
		t.Fatal(err)
	}
	request := RollbackRequest{MemoryID: changed["new_id"].(string),
		RevisionID: old.RevisionID, Reason: "source corrected"}
	projector := selected.projector
	selected.projector = failingForgetProjector{}
	if _, err := selected.Rollback(ctx, request); err == nil {
		t.Fatal("rollback reported success before native projection")
	}
	rows, err := selected.index.ProjectionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pendingID string
	for _, row := range rows {
		if row.Status == "pending" && row.Record.RevisionOf == request.MemoryID {
			pendingID = row.Record.BlockID
		}
	}
	if pendingID == "" {
		t.Fatal("failed rollback left no recoverable pending revision")
	}
	selected.projector = projector
	result, err := selected.Rollback(ctx, request)
	if err != nil || result["block_id"] != pendingID || result["replayed"] != true {
		t.Fatalf("pending rollback did not recover: %+v, %v", result, err)
	}
}
