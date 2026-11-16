package engine

import (
	"context"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestWorkingCheckpointCommitsExactTaskProjectionAndRejectsDrift(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Task continuation",
		Synopsis: "Current task projection", ParentID: rootID, Reason: "test", Scope: "task"})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := selected.ObserveHost(ctx, HostObservation{EventID: "checkpoint-source-1",
		EventKind: "conversation_turn", RawEvidence: "exact host bytes",
		ObservedAt: "2026-10-24T09:00:00Z", ProducerID: "host:test"},
		HostLineage{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	rawID := observed["witness_id"].(string)
	raw, err := selected.ReadStored(ctx, rawID)
	if err != nil || raw == nil {
		t.Fatalf("raw host source unavailable: %v", err)
	}
	sourceHash, err := memory.ContinuationSourceHash(*raw)
	if err != nil {
		t.Fatal(err)
	}
	windowHash, err := memory.ContinuationWindowHash([]memory.MemoryRecord{*raw})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := memory.WorkingStateCheckpoint{WorkspaceID: "alpha", ProfileID: "team",
		OwnerGeneration: "owner-1", RequestedBlockID: leaf["block_id"].(string),
		WorkingView: "Continue from exact source.", SourceRefSHA256: [][2]string{{rawID, sourceHash}},
		HostEventID: "checkpoint-1", SessionID: "session-1", TaskID: "task-1",
		SourceEventRefs: []string{rawID}, SourceEventSHA256: windowHash,
		TaskAnchorRefs: []string{rawID}, SystemAnchorRefs: []string{rawID},
		ExactRecentTailRefs: []string{rawID}, EvidencePageRefs: [][]string{{rawID}},
		RollbackOrPageBackRef: rawID, ValidFrom: "2026-10-24T09:00:00Z",
		ValidTo: "2027-10-24T09:00:00Z", SchemaVersion: memory.CurrentSchemaVersion,
		HardPolicyVersion: 1, SemanticCatalogVersion: "catalog-1", HostVersion: "host-1",
		ModelVersion: "model-1", PromptVersion: "prompt-1", ContinuationSufficiency: "sufficient",
		EvidenceRefs: []memory.EvidenceRef{{RefType: "session_event", RefID: "checkpoint-1",
			ProducedBy: "harness", ProducerID: "host:test"}}}
	originalProjector := selected.projector
	selected.projector = failingForgetProjector{}
	if _, err := selected.CommitWorkingCheckpoint(ctx, CommitWorkingCheckpointRequest{Checkpoint: checkpoint}); err == nil {
		t.Fatal("native projection failure did not leave a retryable checkpoint")
	}
	selected.projector = originalProjector
	first, err := selected.CommitWorkingCheckpoint(ctx, CommitWorkingCheckpointRequest{Checkpoint: checkpoint})
	if err != nil || first["saved"] != true || first["replayed"] != true {
		t.Fatalf("interrupted checkpoint root did not recover: %+v, %v", first, err)
	}
	firstID := first["memory_id"].(string)
	stored, err := selected.ReadStored(ctx, firstID)
	if err != nil || stored == nil || stored.Kind != "task_state" ||
		!strings.Contains(stored.Content, "Continue from exact source.") {
		t.Fatalf("checkpoint payload was not committed: %+v, %v", stored, err)
	}
	replayed, err := selected.CommitWorkingCheckpoint(ctx, CommitWorkingCheckpointRequest{Checkpoint: checkpoint})
	if err != nil || replayed["replayed"] != true || replayed["memory_id"] != firstID {
		t.Fatalf("checkpoint retry was not idempotent: %+v, %v", replayed, err)
	}
	changed := checkpoint
	changed.WorkingView = "Continue with the next step."
	second, err := selected.CommitWorkingCheckpoint(ctx, CommitWorkingCheckpointRequest{Checkpoint: changed})
	if err != nil || second["memory_id"] == firstID {
		t.Fatalf("checkpoint successor was not appended: %+v, %v", second, err)
	}
	drift := changed
	secondRaw, err := selected.ObserveHost(ctx, HostObservation{EventID: "checkpoint-source-2",
		EventKind: "conversation_turn", RawEvidence: "second exact host event",
		ObservedAt: "2026-10-24T09:05:00Z", ProducerID: "host:test"},
		HostLineage{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	secondRawID := secondRaw["witness_id"].(string)
	secondRawRecord, err := selected.ReadStored(ctx, secondRawID)
	if err != nil || secondRawRecord == nil {
		t.Fatalf("second raw host source unavailable: %v", err)
	}
	secondHash, err := memory.ContinuationSourceHash(*secondRawRecord)
	if err != nil {
		t.Fatal(err)
	}
	drift.TaskAnchorRefs = []string{secondRawID}
	drift.EvidencePageRefs = [][]string{{rawID, secondRawID}}
	drift.SourceRefSHA256 = [][2]string{{rawID, sourceHash}, {secondRawID, secondHash}}
	if _, err := selected.CommitWorkingCheckpoint(ctx, CommitWorkingCheckpointRequest{Checkpoint: drift}); err == nil || !strings.Contains(err.Error(), "anchor drift") {
		t.Fatalf("valid raw sources bypassed established anchors: %v", err)
	}
}
