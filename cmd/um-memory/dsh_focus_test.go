package main

import (
	"context"
	"slices"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestDSHFocusPrefersRelevantEvidenceOverUnresolvedNotes(t *testing.T) {
	proof := []memory.EvidenceRef{{RefType: "message", ProducedBy: "human", ProducerID: "harness-user", RefID: "turn"}}
	block := &memory.RecordBlockMembership{PrimaryBlockID: "project"}
	other := &memory.RecordBlockMembership{PrimaryBlockID: "different-topic"}
	research := memory.MemoryRecord{BlockID: "research", BlockMembership: block, Kind: "source_excerpt",
		Content: "Read the yard operating notes and prepare a report.", Metadata: map[string]any{"source": "dsh-user"}, EvidenceRefs: proof}
	confirmation := research
	confirmation.BlockID, confirmation.Content = "confirmation", "The night crew clears Dock 12 at 07:15."
	if focus := prioritizeDSHFocus([]memory.MemoryRecord{research, confirmation}); len(focus) != 1 || focus[0].BlockID != "confirmation" {
		t.Fatalf("fallback omitted the source bearing the confirmed values: %+v", focus)
	}
	draft := memory.MemoryRecord{BlockID: "draft", BlockMembership: block, Content: "Shift limit has yet to be confirmed."}
	foreign := memory.MemoryRecord{BlockID: "foreign", BlockMembership: other, EvidenceRefs: proof}
	decided := memory.MemoryRecord{BlockID: "decided", BlockMembership: block, EvidenceRefs: proof}
	ids := []string{}
	for _, record := range prioritizeDSHFocus([]memory.MemoryRecord{draft, foreign, decided}) {
		ids = append(ids, record.BlockID)
	}
	if !slices.Equal(ids, []string{"decided", "draft", "foreign"}) {
		t.Fatalf("same-topic evidence did not lead the focus: %v", ids)
	}
}

func TestDSHFocusUsesBuiltInBlocksWithoutChangingOtherSession(t *testing.T) {
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "default", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	for _, item := range []engine.CandidateRequest{
		{OperationID: "north", Title: "Yarrow ramp", Content: "The north loading ramp uses YARROW-92.", Kind: "decision", RequestedBlockID: "decisions_and_contracts", EvidenceKind: "user_statement"},
		{OperationID: "south", Title: "Freezer clock", Content: "The south freezer is inspected at 04:55.", Kind: "fact", RequestedBlockID: "stable_project_facts", EvidenceKind: "user_statement"},
	} {
		if result, err := selected.RememberCandidate(ctx, item); err != nil || !result.Saved {
			t.Fatalf("write: %+v, %v", result, err)
		}
	}
	first := dshFocusRequest{SessionID: "first", Query: "north loading ramp Yarrow", Limit: 1, MaxTokens: 128}
	second := dshFocusRequest{SessionID: "second", Query: "south freezer clock", Limit: 1, MaxTokens: 128}
	for _, input := range []dshFocusRequest{first, second, first} {
		result, err := dshFocus(ctx, selected, input)
		if err != nil || result["status"] != "resolved" || result["revision"] != 1 {
			t.Fatalf("focus for %s: %+v, %v", input.SessionID, result, err)
		}
		records := result["records"].([]memory.MemoryRecord)
		want := map[string]string{"first": "Yarrow ramp", "second": "Freezer clock"}[input.SessionID]
		if len(records) != 1 || records[0].Title != want {
			t.Fatalf("focus escaped its pins: %+v", records)
		}
	}
	before, err := selected.GetActiveMask(ctx, "", "first", "dsh-auto")
	if err != nil {
		t.Fatal(err)
	}
	passive := memory.NewRecallPlan("south freezer clock")
	passive.WorkspaceID, passive.ProfileID, passive.RunID = "alpha", "default", "first"
	passive.ScopeChain, passive.SearchAll, passive.IncludeCandidates = []string{"workspace:alpha"}, true, true
	if records, err := selected.Recall(ctx, passive); err != nil || len(records) == 0 || records[0].Title != "Freezer clock" {
		t.Fatalf("passive recall: %+v, %v", records, err)
	}
	after, err := selected.GetActiveMask(ctx, "", "first", "dsh-auto")
	if err != nil || before.MaskID != after.MaskID || before.Revision != after.Revision {
		t.Fatalf("passive recall altered active focus: %+v, %v", after, err)
	}
	if _, err := selected.TransitionActiveMask(ctx, before.MaskID, before.Revision,
		"deactivate", "temporary empty focus", "first", "dsh-auto"); err != nil {
		t.Fatal(err)
	}
	restored, err := dshFocus(ctx, selected, first)
	if err != nil || restored["status"] != "resolved" || restored["mask_id"] == before.MaskID {
		t.Fatalf("a relevant turn did not restore focus after deactivation: %+v, %v", restored, err)
	}
}

func TestDSHFocusFallsBackToAnArchivedHumanQuote(t *testing.T) {
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "default", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	quote, err := selected.RememberCandidate(ctx, engine.CandidateRequest{
		OperationID: "human-calibration", Title: "Calibration confirmation", Content: "Calibration begins at 09:45 on 4 August.",
		Kind: "source_excerpt", RequestedBlockID: "decisions_and_contracts", EvidenceKind: "user_statement",
		Metadata: map[string]any{"source": "dsh-user"}, EvidenceRefs: []memory.EvidenceRef{{RefType: "message", RefID: "test-confirmation",
			ProducedBy: "human", ProducerID: "harness-user"}},
	})
	if err != nil || !quote.Saved {
		t.Fatalf("capture quote: %+v, %v", quote, err)
	}
	result, err := dshFocus(ctx, selected, dshFocusRequest{SessionID: "handoff", Query: "Calibration confirmation time in August", Limit: 4, MaxTokens: 128})
	if err != nil || result["status"] != "resolved" {
		t.Fatalf("focus: %+v, %v", result, err)
	}
	records := result["records"].([]memory.MemoryRecord)
	if len(records) != 1 || records[0].BlockID != quote.RecordID {
		t.Fatalf("focus did not deliver the original quote: %+v", records)
	}
}
