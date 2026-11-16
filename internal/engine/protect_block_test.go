package engine

import (
	"context"
	"testing"
)

func TestProtectBlockBindsRevisionAndPreventsArchive(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	root := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			root = block.BlockID
			break
		}
	}
	if root == "" {
		t.Fatal("workspace root is missing")
	}
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Pinned topic",
		Synopsis: "Host-owned policy", ParentID: root, Reason: "operator setup"})
	if err != nil {
		t.Fatal(err)
	}
	id := created["block_id"].(string)
	protected, err := selected.ProtectBlock(ctx, BlockTransitionRequest{BlockID: id,
		ExpectedRevision: 1, Reason: "host policy"})
	if err != nil || protected["protected"] != true || protected["revision"] != 2 {
		t.Fatalf("block protection failed: %+v %v", protected, err)
	}
	if _, err := selected.ProtectBlock(ctx, BlockTransitionRequest{BlockID: id,
		ExpectedRevision: 1, Reason: "stale"}); err == nil {
		t.Fatal("stale protection revision was accepted")
	}
	if _, err := selected.ArchiveBlock(ctx, BlockTransitionRequest{BlockID: id,
		ExpectedRevision: 2, Reason: "remove"}); err == nil {
		t.Fatal("protected block was archived")
	}
}
