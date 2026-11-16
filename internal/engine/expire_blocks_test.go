package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
)

func TestExpireDueBlocksArchivesOnlyUnreferencedExpiredBlocks(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	opened, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil || len(states) != 1 {
		t.Fatalf("workspace root is unavailable: %+v %v", states, err)
	}
	rootID := states[0].Block.BlockID
	create := func(name, expires, scope string) map[string]any {
		t.Helper()
		result, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: name, Synopsis: name,
			ParentID: rootID, ExpiresAt: expires, Scope: scope, Reason: "expiry fixture"})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	expired := create("Expired empty", "2020-01-01T00:00:00Z", "workspace")
	durable := create("Durable empty", "", "workspace")
	occupied := create("Expired occupied", "2020-01-01T00:00:00Z", "workspace")
	occupiedID := occupied["block_id"].(string)
	stored, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Retained finding",
		RequestedBlockID: occupiedID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !stored.Saved {
		t.Fatalf("occupied block write failed: %+v %v", stored, err)
	}
	protected := create("Expired session", "2020-01-01T00:00:00Z", "session")
	protectedID := protected["block_id"].(string)
	state, err := opened.GetBlock(ctx, protectedID)
	if err != nil || state == nil {
		t.Fatalf("session block missing: %+v %v", state, err)
	}
	_, err = selected.ProtectBlock(ctx, BlockTransitionRequest{BlockID: protectedID,
		ExpectedRevision: state.Revision, Reason: "host owned"})
	if err != nil {
		t.Fatal(err)
	}
	hostSession, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Expired owned session",
		Synopsis: "Host session", ParentID: rootID, ExpiresAt: "2020-01-01T00:00:00Z",
		Scope: "session", Reason: "expiry fixture",
		Metadata: map[string]any{"umbrella_memory_session_id": "session-host"}})
	if err != nil {
		t.Fatal(err)
	}
	hostSessionID := hostSession["block_id"].(string)
	_, err = selected.ProtectBlock(ctx, BlockTransitionRequest{BlockID: hostSessionID,
		ExpectedRevision: 1, Reason: "host owned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ExpireDueBlocks(ctx, "not-a-time", ""); err == nil {
		t.Fatal("invalid as_of was accepted")
	}
	result, err := selected.ExpireDueBlocks(ctx, "2099-01-01T00:00:00Z", "expiry fixture")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result["archived_block_ids"],
		[]string{expired["block_id"].(string), hostSessionID}) {
		t.Fatalf("unexpected archived blocks: %+v", result)
	}
	blockers := result["blockers"].(map[string][]string)
	if !reflect.DeepEqual(blockers[occupiedID], []string{"block_has_current_records"}) ||
		!reflect.DeepEqual(blockers[protectedID], []string{"block_is_protected"}) {
		t.Fatalf("expired block guards failed: %+v", blockers)
	}
	for _, id := range []string{durable["block_id"].(string), occupiedID, protectedID} {
		state, err := opened.GetBlock(ctx, id)
		if err != nil || state == nil || state.Status != "active" {
			t.Fatalf("guarded block %s changed: %+v %v", id, state, err)
		}
	}
	archived, err := opened.GetBlock(ctx, expired["block_id"].(string))
	if err != nil || archived == nil || archived.Status != "archived" {
		t.Fatalf("expired block was not archived: %+v %v", archived, err)
	}
	archived, err = opened.GetBlock(ctx, hostSessionID)
	if err != nil || archived == nil || archived.Status != "archived" || !archived.Protected {
		t.Fatalf("owned session block was not archived: %+v %v", archived, err)
	}
}

func TestExpireDueBlocksArchivesChildrenBeforeParent(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	opened, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil || len(states) != 1 {
		t.Fatalf("workspace root is unavailable: %+v %v", states, err)
	}
	parent, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Expired branch",
		Synopsis: "Old branch", ParentID: states[0].Block.BlockID, IndexOnly: true,
		ExpiresAt: "2020-01-01T00:00:00Z", Reason: "expiry fixture"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Expired leaf",
		Synopsis: "Old leaf", ParentID: parent["block_id"].(string),
		ExpiresAt: "2020-01-01T00:00:00Z", Reason: "expiry fixture"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := selected.ExpireDueBlocks(ctx, "2099-01-01T00:00:00Z", "expiry fixture")
	if err != nil || !reflect.DeepEqual(report["archived_block_ids"],
		[]string{child["block_id"].(string), parent["block_id"].(string)}) {
		t.Fatalf("nested expiry did not archive child then parent: %+v %v", report, err)
	}
}
