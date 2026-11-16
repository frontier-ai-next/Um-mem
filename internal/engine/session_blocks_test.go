package engine

import (
	"context"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

func TestProtectedSessionBlockMaintenanceUsesExactGuards(t *testing.T) {
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
		t.Fatalf("root unavailable: %+v %v", states, err)
	}
	rootID := states[0].Block.BlockID
	left, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Left sessions", Synopsis: "Left",
		ParentID: rootID, Reason: "organize", IndexOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	right, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Right sessions", Synopsis: "Right",
		ParentID: rootID, Reason: "organize", IndexOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	leftID, rightID := left["block_id"].(string), right["block_id"].(string)
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Current session",
		Synopsis: "Session state", ParentID: leftID, Reason: "host setup", Scope: "session",
		Metadata: map[string]any{"umbrella_memory_session_id": "s-1", "owner": "host"}})
	if err != nil {
		t.Fatal(err)
	}
	id := created["block_id"].(string)
	protected, err := selected.ProtectBlock(ctx, BlockTransitionRequest{BlockID: id,
		ExpectedRevision: 1, Reason: "host ownership"})
	if err != nil || protected["protected"] != true {
		t.Fatalf("session protection failed: %+v %v", protected, err)
	}
	if _, err := selected.RepairProtectedBlockParent(ctx, RepairProtectedBlockParentRequest{
		BlockID: id, ExpectedRevision: 2, ExpectedParentID: leftID, ParentID: rightID,
		ExpectedMetadata: map[string]any{"owner": "wrong"}, Reason: "repair"}); err == nil {
		t.Fatal("repair accepted an incorrect metadata guard")
	}
	repaired, err := selected.RepairProtectedBlockParent(ctx, RepairProtectedBlockParentRequest{
		BlockID: id, ExpectedRevision: 2, ExpectedParentID: leftID, ParentID: rightID,
		ExpectedMetadata: map[string]any{"owner": "host"}, Reason: "repair"})
	if err != nil || repaired["parent_id"] != rightID {
		t.Fatalf("protected parent repair failed: %+v %v", repaired, err)
	}
	if _, err := selected.ArchiveSessionBlock(ctx, SessionBlockRequest{BlockID: id,
		ExpectedRevision: 3, SessionID: "other", Reason: "finished"}); err == nil {
		t.Fatal("session archive accepted another session")
	}
	archived, err := selected.ArchiveSessionBlock(ctx, SessionBlockRequest{BlockID: id,
		ExpectedRevision: 3, SessionID: "s-1", Reason: "finished"})
	if err != nil || archived["archived"] != true {
		t.Fatalf("protected session archive failed: %+v %v", archived, err)
	}
	revived, err := selected.ReviveSessionBlock(ctx, SessionBlockRequest{BlockID: id,
		ExpectedRevision: 4, SessionID: "s-1", Reason: "resume"})
	if err != nil || revived["archived"] != false {
		t.Fatalf("protected session revive failed: %+v %v", revived, err)
	}
	expiry, err := selected.SetSessionBlockExpiry(ctx, SessionBlockRequest{BlockID: id,
		ExpectedRevision: 5, SessionID: "s-1", Reason: "schedule", ExpiresAt: "2030-01-01T00:00:00Z"})
	if err != nil || expiry["ok"] != true {
		t.Fatalf("protected session expiry failed: %+v %v", expiry, err)
	}
	state, err := opened.GetBlock(ctx, id)
	if err != nil || state == nil || state.Block.LifecyclePolicy["expires_at"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("protected session expiry was not saved: %+v %v", state, err)
	}
}
