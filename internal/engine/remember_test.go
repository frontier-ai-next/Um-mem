package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/receipts"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type countingProjector struct {
	attempts int
	failOnce bool
}

func (*countingProjector) BackendID() string                       { return "hindsight" }
func (*countingProjector) RequiresNative(memory.MemoryRecord) bool { return true }
func (projector *countingProjector) Project(_ context.Context, record memory.MemoryRecord, _ string, _ int64) (store.ProjectionResult, error) {
	projector.attempts++
	if projector.failOnce {
		projector.failOnce = false
		return store.ProjectionResult{}, errors.New("transient native failure")
	}
	return store.ProjectionResult{NativeID: "UM-MEM:block:" + record.BlockID, Verified: true}, nil
}

func TestRememberCandidateRoutesAndWritesCanonicalRecord(t *testing.T) {
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	selected.projector = verifiedProjector{}
	ctx := context.Background()
	for _, blocked := range []CandidateRequest{
		{Content: "Do not write to an index", RequestedBlockID: "memory_root_index"},
		{Content: "Do not bypass promotion", RequestedBlockID: "verified_lessons"},
		{Content: "Do not set governance metadata", RequestedBlockID: "stable_project_facts", Metadata: map[string]any{"promotion_gate": "accept"}},
	} {
		result, err := selected.RememberCandidate(ctx, blocked)
		if err != nil || result.Saved || result.Route.Reason == "" {
			t.Fatalf("protected route was accepted: %+v, %v", result, err)
		}
	}
	rejectedRequest := CandidateRequest{OperationID: "rejected-route", Content: "Still protected", RequestedBlockID: "memory_root_index"}
	rejected, err := selected.RememberCandidate(ctx, rejectedRequest)
	if err != nil || rejected.Saved || rejected.Route.Reason != "index_only_block_cannot_store_records" {
		t.Fatalf("operation-scoped route rejection differs: %+v, %v", rejected, err)
	}
	replayed, err := selected.RememberCandidate(ctx, rejectedRequest)
	if err != nil || replayed.Saved || !replayed.Replayed || replayed.Route.Reason != rejected.Route.Reason {
		t.Fatalf("route rejection receipt did not replay: %+v, %v", replayed, err)
	}
	status, err := selected.OperationStatus(ctx, rejectedRequest.OperationID)
	if err != nil || status["ok"] != false || status["reason_code"] != rejected.Route.Reason {
		t.Fatalf("rejected operation status is misleading: %+v, %v", status, err)
	}
	otherRole, err := selected.OperationStatusFor(ctx, rejectedRequest.OperationID, "operator", 1)
	if err != nil || otherRole["durable_status"] != "not_found" {
		t.Fatalf("another caller role read the operation receipt: %+v, %v", otherRole, err)
	}
	if _, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Project scope needs identity", RequestedBlockID: "project_scope_stable_project_facts"}); err == nil {
		t.Fatal("project-scoped candidate had no project identity")
	}
	result, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "A durable project fact", RequestedBlockID: "stable_project_facts",
		Tags: []string{"source"}, SourceID: "source-1", EvidenceKind: "agent_tool_claim",
	})
	if err != nil || !result.Saved || result.RecordID == "" || result.Receipt == nil || result.Receipt.ProjectionPending {
		t.Fatalf("candidate write failed: %+v, %v", result, err)
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil {
		t.Fatalf("candidate record is unavailable: %+v, %v", record, err)
	}
	if record.Status != "candidate" || record.Authority != "advisory" || record.ProfileID != "team" ||
		record.BlockMembership.PrimaryBlockID != "stable_project_facts" ||
		record.Metadata["route_decision_reason"] != "requested_block_policy" ||
		record.Metadata["source_id"] != "source-1" {
		t.Fatalf("candidate governance or provenance differs: %+v", record)
	}
	if record.BlockID != memory.BlockIDForCanonicalKey(record.CanonicalKey) ||
		record.SemanticCatalogVersion != selected.CatalogRevisionID() ||
		record.BlockMembership.CatalogRevisionID != selected.CatalogRevisionID() {
		t.Fatalf("candidate canonical identity differs: %+v", record)
	}
	revision, err := memory.RevisionIDForRecord(record.CanonicalKey, *record, "insert", "")
	if err != nil || revision != record.RevisionID {
		t.Fatalf("candidate semantic revision is unstable: %q, %v", revision, err)
	}
}

func TestRememberCandidateOperationReceiptRetriesDeterministically(t *testing.T) {
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	projector := &countingProjector{failOnce: true}
	selected.projector = projector
	ctx := context.Background()
	request := CandidateRequest{OperationID: "operation-1", Content: "An idempotent fact", RequestedBlockID: "stable_project_facts"}
	if _, err := selected.RememberCandidate(ctx, request); err == nil {
		t.Fatal("native failure was not reported")
	}
	identity := receipts.Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight", CallerRole: "go_cli", ProtocolVersion: 2}
	failed, err := receipts.Status(ctx, selected.config.RepoRoot, identity, request.OperationID)
	if err != nil || failed == nil || failed.Status != "failed" {
		t.Fatalf("failed operation receipt was not durable: %+v, %v", failed, err)
	}
	first, err := selected.RememberCandidate(ctx, request)
	if err != nil || !first.Saved || !first.Replayed || first.RecordID == "" || first.Receipt == nil || first.Receipt.ProjectionPending {
		t.Fatalf("retry did not recover native projection: %+v, %v", first, err)
	}
	if projector.attempts != 2 {
		t.Fatalf("native projection attempts = %d, want 2", projector.attempts)
	}
	second, err := selected.RememberCandidate(ctx, request)
	if err != nil || !second.Saved || !second.Replayed || second.RecordID != first.RecordID || projector.attempts != 2 {
		t.Fatalf("committed receipt did not replay exactly: %+v, %v", second, err)
	}
	changed := request
	changed.Content = "Changed fact"
	if _, err := selected.RememberCandidate(ctx, changed); err == nil || projector.attempts != 2 {
		t.Fatalf("changed operation reused receipt: %v", err)
	}
	stored, err := selected.ReadStored(ctx, first.RecordID)
	if err != nil || stored == nil || stored.Content != request.Content {
		t.Fatalf("recovered canonical record differs: %+v, %v", stored, err)
	}
}

func TestRememberCandidateRecoversAfterCommitBeforeReceipt(t *testing.T) {
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	projector := &countingProjector{}
	selected.projector = projector
	ctx := context.Background()
	request := CandidateRequest{OperationID: "crash-boundary", Content: "Fact survives receipt crash", RequestedBlockID: "stable_project_facts"}
	identity := receipts.Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight", CallerRole: "go_cli", ProtocolVersion: 2}
	fingerprintRequest := request
	fingerprintRequest.OperationID = ""
	arguments := map[string]any{"tool": "remember-candidate", "request": fingerprintRequest}
	claim, err := receipts.Claim(ctx, selected.config.RepoRoot, identity, request.OperationID, arguments)
	if err != nil || claim.Status != "new" {
		t.Fatalf("crash-boundary claim failed: %+v, %v", claim, err)
	}
	request.operationCallerRole, request.operationProtocolVersion = "go_cli", 2
	request.operationFingerprint, err = receipts.Fingerprint(arguments)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := selected.rememberCandidateOnce(ctx, request, claim.CreatedAt, false)
	if err != nil || !committed.Saved || projector.attempts != 1 {
		t.Fatalf("canonical write before crash failed: %+v, %v", committed, err)
	}
	recovered, err := selected.RememberCandidate(ctx, request)
	if err != nil || !recovered.Saved || !recovered.Replayed || recovered.RecordID != committed.RecordID || projector.attempts != 1 {
		t.Fatalf("receipt retry duplicated canonical/native write: %+v, %v", recovered, err)
	}
	state, err := receipts.Status(ctx, selected.config.RepoRoot, identity, request.OperationID)
	if err != nil || state == nil || state.Status != "committed" {
		t.Fatalf("recovered receipt was not committed: %+v, %v", state, err)
	}
}
