package engine

import (
	"context"
	"strings"
	"testing"
)

func TestObserveHostRetainsExactHiddenSourceAndReplays(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	observation := HostObservation{EventID: "turn-1", EventKind: "conversation_turn",
		RawEvidence: "  Точный исходный текст\n", ObservedAt: "2026-10-23T12:00:00Z",
		ProducerID: "host:test", Speaker: "user"}
	lineage := HostLineage{SessionID: "session-1", ProjectID: "project-1"}
	result, err := selected.ObserveHost(ctx, observation, lineage)
	if err != nil || result["ok"] != true || result["route_outcome"] != "raw_witness_only" ||
		result["source_bytes"] != len([]byte(observation.RawEvidence)) {
		t.Fatalf("host source write failed: %+v, %v", result, err)
	}
	id := result["witness_id"].(string)
	if id != "mem_b6fbf8364c64a2fd" {
		t.Fatalf("Go host witness ID differs from Python creation identity: %s", id)
	}
	record, err := selected.ReadStored(ctx, id)
	if err != nil || record == nil || record.Content != observation.RawEvidence ||
		record.BlockMembership.PrimaryBlockID != "transient_logs" ||
		record.DeliveryPolicy != "hidden" || record.Retention != "ephemeral" ||
		record.SessionID != lineage.SessionID || record.ProjectID != lineage.ProjectID {
		t.Fatalf("host witness lost exact bytes or isolation: %+v, %v", record, err)
	}
	replayed, err := selected.ObserveHost(ctx, observation, lineage)
	if err != nil || replayed["witness_id"] != id || replayed["replayed"] != true {
		t.Fatalf("host source replay changed identity: %+v, %v", replayed, err)
	}
	changed := observation
	changed.RawEvidence = "different source"
	if _, err := selected.ObserveHost(ctx, changed, lineage); err == nil ||
		!strings.Contains(err.Error(), "registered_source_conflict") {
		t.Fatalf("host source ID accepted different bytes: %v", err)
	}
	if _, err := selected.ObserveHost(ctx, observation,
		HostLineage{SessionID: "foreign"}); err == nil {
		t.Fatal("host source ID crossed session lineage")
	}
}

func TestObserveHostReturnsAgentPlacementWithoutSemanticWrite(t *testing.T) {
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
	leaf, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Observed facts",
		Synopsis: "Agent choices", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.ObserveHost(ctx, HostObservation{EventID: "file-1",
		EventKind: "file_changed", RawEvidence: "diff bytes", AtomicClaim: "A file changed",
		ObservedAt: "2026-10-23T12:00:00Z", ProducerID: "host:test"},
		HostLineage{SessionID: "session-1"})
	if err != nil || result["route_outcome"] != "needs_agent_choice" || result["memory_id"] != "" {
		t.Fatalf("host observation made an automatic semantic write: %+v, %v", result, err)
	}
	manifest := result["placement_manifest"].(map[string]any)
	candidates := manifest["candidate_blocks"].([]map[string]any)
	if manifest["requires_one_writable_leaf"] != true || len(candidates) != 1 ||
		candidates[0]["block_id"] != leaf["block_id"] {
		t.Fatalf("host observation lost explicit agent choice: %+v", manifest)
	}
}

func TestObserveHostRecoversPendingNativeProjection(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	observation := HostObservation{EventID: "turn-retry", EventKind: "conversation_turn",
		RawEvidence: "exact pending source", ObservedAt: "2026-10-23T12:00:00Z",
		ProducerID: "host:test"}
	lineage := HostLineage{SessionID: "session-1"}
	original := selected.projector
	selected.projector = failingForgetProjector{}
	if _, err := selected.ObserveHost(ctx, observation, lineage); err == nil {
		t.Fatal("native failure did not keep host observation pending")
	}
	selected.projector = original
	result, err := selected.ObserveHost(ctx, observation, lineage)
	if err != nil || result["ok"] != true || result["replayed"] != true {
		t.Fatalf("pending host witness was not recovered: %+v, %v", result, err)
	}
	stored, err := selected.ReadStored(ctx, result["witness_id"].(string))
	if err != nil || stored == nil || stored.Content != observation.RawEvidence {
		t.Fatalf("recovered witness is unavailable: %+v, %v", stored, err)
	}
}

func TestHostVerificationReportInputsCannotChangeOnReplay(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	observation := HostObservation{EventID: "test-inputs", EventKind: "test_passed",
		RawEvidence: "1 passed", AtomicClaim: "A test passed", ObservedAt: "2026-10-23T12:00:00Z",
		ProducerID: "host:test", WorkspaceRoot: t.TempDir(), ChangedFiles: []string{"one.txt"}}
	lineage := HostLineage{SessionID: "session-1"}
	if _, err := selected.ObserveHost(ctx, observation, lineage); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []HostObservation{
		func() HostObservation { copy := observation; copy.WorkspaceRoot = t.TempDir(); return copy }(),
		func() HostObservation { copy := observation; copy.ChangedFiles = []string{"two.txt"}; return copy }(),
		func() HostObservation { copy := observation; copy.ArtifactHash = "sha256:other"; return copy }(),
	} {
		if _, err := selected.ObserveHost(ctx, changed, lineage); err == nil ||
			!strings.Contains(err.Error(), "registered_source_conflict") {
			t.Fatalf("changed report input reused original witness: %v", err)
		}
	}
}

func TestCommittedHostReceiptRejectsRetractedWitness(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	observation := HostObservation{EventID: "turn-replay", EventKind: "conversation_turn",
		RawEvidence: "exact", ObservedAt: "2026-10-23T12:00:00Z", ProducerID: "host:test"}
	lineage := HostLineage{SessionID: "session-1"}
	result, err := selected.ObserveHostFor(ctx, observation, lineage, "observe-replay", "host_hook", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: result["witness_id"].(string)}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ObserveHostFor(ctx, observation, lineage, "observe-replay", "host_hook", 2); err == nil {
		t.Fatal("committed receipt advertised a retracted witness as current")
	}
}
