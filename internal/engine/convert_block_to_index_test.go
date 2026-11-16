package engine

import (
	"context"
	"strings"
	"testing"
)

func TestConvertBlockToIndexOnlyWhenNeverUsed(t *testing.T) {
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
	empty, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Future topics",
		Synopsis: "Empty index candidate", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	id := empty["block_id"].(string)
	request := ConvertBlockToIndexRequest{BlockID: id, ExpectedRevision: 1, Reason: "nest topics"}
	if _, err := selected.ConvertBlockToIndex(ctx, ConvertBlockToIndexRequest{BlockID: id,
		ExpectedRevision: 2, Reason: "stale"}); err == nil {
		t.Fatal("stale block revision converted leaf")
	}
	converted, err := selected.ConvertBlockToIndex(ctx, request)
	if err != nil || converted["saved"] != true || converted["backend_partition_id"] != "" ||
		converted["index_only"] != true {
		t.Fatalf("empty leaf conversion failed: %+v, %v", converted, err)
	}
	already, err := selected.ConvertBlockToIndex(ctx, request)
	if err != nil || already["saved"] != false || already["already_index_only"] != true {
		t.Fatalf("converted index replay failed: %+v, %v", already, err)
	}
	child, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Nested topic",
		Synopsis: "Nested under new index", ParentID: id, Reason: "test"})
	if err != nil || child["parent_id"] != id {
		t.Fatalf("converted block did not become a parent: %+v, %v", child, err)
	}
	used, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Used leaf",
		Synopsis: "Historical coverage", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	usedID := used["block_id"].(string)
	result, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A retained fact",
		RequestedBlockID: usedID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !result.Saved {
		t.Fatalf("used leaf setup failed: %+v, %v", result, err)
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil {
		t.Fatalf("used leaf record missing: %+v, %v", record, err)
	}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: result.RecordID,
		ExpectedRevision: record.RevisionID, Reason: "withdraw the fact"}); err != nil {
		t.Fatalf("historical coverage setup failed: %v", err)
	}
	if _, err := selected.ConvertBlockToIndex(ctx, ConvertBlockToIndexRequest{BlockID: usedID,
		ExpectedRevision: 1, Reason: "unsafe"}); err == nil || !strings.Contains(err.Error(), "never held records") {
		t.Fatalf("historically used leaf was converted: %v", err)
	}
}
