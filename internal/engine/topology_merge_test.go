package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestMergeBlocksPublishesNativeTargetAndReplays(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	blocks, err := selected.topology.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	parentID := blocks[0].Block.BlockID
	var sources []BlockMergeSource
	var recordIDs []string
	for _, name := range []string{"Archive facts", "Gateway facts"} {
		created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: name,
			Synopsis: name, ParentID: parentID, Reason: "organize sources", Scope: "workspace"})
		if err != nil {
			t.Fatal(err)
		}
		id := created["block_id"].(string)
		candidate, err := selected.RememberCandidate(ctx, CandidateRequest{
			Content: name + " are durable.", RequestedBlockID: id,
			SessionID: "session-1", RunID: "session-1"})
		if err != nil || !candidate.Saved {
			t.Fatalf("source record was not saved: %+v, %v", candidate, err)
		}
		sources = append(sources, BlockMergeSource{BlockID: id, ExpectedRevision: 1})
		recordIDs = append(recordIDs, candidate.RecordID)
	}
	request := MergeBlocksRequest{Sources: sources, ParentID: parentID,
		Target: BlockEvolutionTarget{Name: "Merged facts", Synopsis: "Durable facts",
			RecordIDs: recordIDs}, Reason: "combine related facts", OperationID: "public-merge-one"}
	receipt, err := selected.MergeBlocks(ctx, request)
	if err != nil || receipt["ok"] != true || receipt["generation_status"] != "published" {
		t.Fatalf("merge did not publish: %+v, %v", receipt, err)
	}
	target := receipt["target"].(map[string]any)
	targetID := target["block_id"].(string)
	for _, recordID := range recordIDs {
		plan := memory.NewRecallPlan("durable")
		plan.WorkspaceID, plan.ProfileID, plan.RunID = "alpha", "team", "session-1"
		plan.ScopeChain = []string{"workspace:alpha"}
		plan.LogicalBlockIDs = []string{targetID}
		plan.IncludeCandidates = true
		recalled, err := selected.Recall(ctx, plan)
		if err != nil || len(recalled) != 2 {
			t.Fatalf("merged native recall differs: %+v, %v", recalled, err)
		}
		found := false
		for _, item := range recalled {
			if item.BlockID == recordID && item.BlockMembership.PrimaryBlockID == targetID {
				found = true
			}
		}
		if !found {
			t.Fatalf("merged record %s absent from native target", recordID)
		}
	}
	for _, source := range sources {
		state, err := selected.topology.GetBlock(ctx, source.BlockID)
		if err != nil || state == nil || state.Status != "archived" {
			t.Fatalf("merge source was not archived: %+v, %v", state, err)
		}
	}
	replay, err := selected.MergeBlocks(ctx, request)
	if err != nil || !reflect.DeepEqual(replay, receipt) {
		t.Fatalf("merge replay changed its receipt: %+v, %v", replay, err)
	}
	request.Target.Name = "different"
	if _, err := selected.MergeBlocks(ctx, request); err == nil {
		t.Fatal("operation ID accepted a changed merge request")
	}
}
