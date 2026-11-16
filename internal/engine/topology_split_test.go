package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestSplitBlockPublishesTwoNativeTargetsAndReplays(t *testing.T) {
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
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Mixed facts",
		Synopsis: "Facts to separate by topic", ParentID: blocks[0].Block.BlockID,
		Reason: "organize source", Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created["block_id"].(string)
	first, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "The archive stores contracts.",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !first.Saved {
		t.Fatalf("first source record was not saved: %+v, %v", first, err)
	}
	second, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "The gateway serves queries.",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !second.Saved {
		t.Fatalf("second source record was not saved: %+v, %v", second, err)
	}
	request := SplitBlockRequest{BlockID: sourceID, ExpectedRevision: 1,
		Reason: "separate contracts and queries", OperationID: "public-split-one",
		Targets: []BlockEvolutionTarget{
			{Name: "Contracts", Synopsis: "Stored contracts", RecordIDs: []string{first.RecordID},
				Tags: []string{"contract"}},
			{Name: "Queries", Synopsis: "Gateway queries", RecordIDs: []string{second.RecordID},
				Tags: []string{"query"}},
		}}
	if _, err := selected.SplitBlock(ctx, SplitBlockRequest{BlockID: sourceID,
		ExpectedRevision: 1, Reason: request.Reason, OperationID: "incomplete-split",
		Targets: []BlockEvolutionTarget{{Name: "Only one", Synopsis: "Incomplete",
			RecordIDs: []string{first.RecordID}}}}); err == nil {
		t.Fatal("split accepted a mapping that omitted a current source record")
	}
	if pending, err := selected.PendingTopologyEvolutions(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("invalid split reserved a source block: %+v, %v", pending, err)
	}
	receipt, err := selected.SplitBlock(ctx, request)
	if err != nil || receipt["ok"] != true || receipt["generation_status"] != "published" {
		t.Fatalf("public split did not publish: %+v, %v", receipt, err)
	}
	targets, ok := receipt["targets"].([]map[string]any)
	if !ok || len(targets) != 2 || targets[0]["name"] != "Contracts" ||
		targets[1]["name"] != "Queries" {
		t.Fatalf("split targets differ: %+v", receipt)
	}
	assignments := receipt["record_assignments"].(map[string]string)
	for i, recordID := range []string{first.RecordID, second.RecordID} {
		targetID := targets[i]["block_id"].(string)
		if assignments[recordID] != targetID || targets[i]["token_budget"] != float64(1000) {
			t.Fatalf("split target assignment or default budget differs: %+v", receipt)
		}
		plan := memory.NewRecallPlan([]string{"contracts", "queries"}[i])
		plan.WorkspaceID, plan.ProfileID, plan.RunID = "alpha", "team", "session-1"
		plan.ScopeChain = []string{"workspace:alpha"}
		plan.LogicalBlockIDs = []string{targetID}
		plan.IncludeCandidates = true
		recalled, err := selected.Recall(ctx, plan)
		if err != nil || len(recalled) != 1 || recalled[0].BlockID != recordID ||
			recalled[0].BlockMembership.PrimaryBlockID != targetID {
			t.Fatalf("split target native recall differs: %+v, %v", recalled, err)
		}
	}
	if source, err := selected.topology.GetBlock(ctx, sourceID); err != nil || source == nil ||
		source.Status != "archived" {
		t.Fatalf("split source was not archived: %+v, %v", source, err)
	}
	partitions, err := selected.index.NativePartitionMemberships(ctx)
	if err != nil || len(partitions) != 2 ||
		partitions[0].LogicalBlockID == sourceID || partitions[1].LogicalBlockID == sourceID ||
		partitions[0].LogicalBlockID == partitions[1].LogicalBlockID {
		t.Fatalf("published split partitions differ: %+v, %v", partitions, err)
	}
	if oldIDs, err := selected.index.RecordIDsForLogicalBlocks(ctx, []string{sourceID}); err != nil || len(oldIDs) != 0 {
		t.Fatalf("archived source retained granted records: %+v, %v", oldIDs, err)
	}
	for _, target := range targets {
		blockID := target["block_id"].(string)
		grantedIDs, err := selected.index.RecordIDsForLogicalBlocks(ctx, []string{blockID})
		if err != nil || len(grantedIDs) != 1 || assignments[grantedIDs[0]] != blockID {
			t.Fatalf("split target grant differs: block=%s ids=%+v err=%v", blockID, grantedIDs, err)
		}
	}
	replayed, err := selected.SplitBlock(ctx, request)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("exact split replay changed its receipt: %+v, %v", replayed, err)
	}
	request.Targets[0].Name = "Changed contracts"
	if _, err := selected.SplitBlock(ctx, request); err == nil {
		t.Fatal("operation ID accepted a changed split request")
	}
}

func TestSplitBlockAbortsWhenSourceRevisionChangesAfterStagingFailure(t *testing.T) {
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
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Revision source",
		Synopsis: "Revision source", ParentID: blocks[0].Block.BlockID,
		Reason: "prepare split", Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created["block_id"].(string)
	written, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "A source fact",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !written.Saved {
		t.Fatalf("source record was not saved: %+v, %v", written, err)
	}
	projector := selected.projector
	selected.projector = failTopologyProjector{delegate: projector}
	request := SplitBlockRequest{BlockID: sourceID, ExpectedRevision: 1,
		OperationID: "changed-source-split", Reason: "separate source fact",
		Targets: []BlockEvolutionTarget{{Name: "New target", Synopsis: "New target",
			RecordIDs: []string{written.RecordID}}}}
	if _, err := selected.SplitBlock(ctx, request); err == nil {
		t.Fatal("injected native staging failure was accepted")
	}
	selected.projector = projector
	newName := "Revised source"
	if _, err := selected.AmendBlock(ctx, AmendBlockRequest{BlockID: sourceID,
		ExpectedRevision: 1, Reason: "source changed before publication",
		Name: &newName}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ResumeTopologyEvolution(ctx, request.OperationID); err == nil {
		t.Fatal("changed source revision was published")
	}
	journal, err := selected.topology.TopologyEvolution(ctx, request.OperationID)
	if err != nil || journal == nil || journal.State != "aborted" ||
		journal.AbortReason != "catalog_revision_conflict" {
		t.Fatalf("catalog conflict did not abort generation: %+v, %v", journal, err)
	}
	state, err := selected.topology.GetBlock(ctx, sourceID)
	if err != nil || state == nil || state.Status != "active" || state.Revision != 2 {
		t.Fatalf("changed source was modified by failed split: %+v, %v", state, err)
	}
	stored, err := selected.ReadStored(ctx, written.RecordID)
	if err != nil || stored == nil || stored.BlockMembership.PrimaryBlockID != sourceID {
		t.Fatalf("failed split changed the published record membership: %+v, %v", stored, err)
	}
}
