package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestDerivedChainInvalidatesAfterSourceCorrection(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	source.BlockID = "source"
	source.RevisionID = "rev_source_1"
	source.CanonicalKey = "alpha:source"
	source.Content = "Original measured finding"
	bind := func(record *memory.MemoryRecord) {
		membership := *record.BlockMembership
		membership.RecordID = record.BlockID
		membership.RecordRevisionID = record.RevisionID
		membership.MembershipRevisionID = membership.RevisionHash()
		record.BlockMembership = &membership
	}
	bind(&source)
	derived := source
	derived.BlockID = "derived"
	derived.RevisionID = "rev_derived"
	derived.CanonicalKey = ""
	derived.Content = "Derived measured finding"
	bind(&derived)
	dependent := derived
	dependent.BlockID = "dependent"
	dependent.RevisionID = "rev_dependent"
	dependent.Content = "Dependent measured finding"
	bind(&dependent)
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	for _, record := range []memory.MemoryRecord{source, derived, dependent} {
		pending, err := writer.PrepareRootWrite(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
			t.Fatal(err)
		}
	}
	first := memory.EdgeRecord{SrcID: derived.BlockID, DstID: source.BlockID,
		EdgeType: "derived_from", CreatedAt: "2024-01-01T00:00:00Z"}
	if _, err := writer.AddEdge(ctx, first); err != nil {
		t.Fatal(err)
	}
	if replay, err := writer.AddEdge(ctx, first); err != nil || !replay.Replayed {
		t.Fatalf("graph edge replay was not idempotent: %+v, %v", replay, err)
	}
	second := memory.EdgeRecord{SrcID: dependent.BlockID, DstID: derived.BlockID,
		EdgeType: "derived_from", CreatedAt: "2024-01-01T00:00:00Z"}
	if _, err := writer.AddEdge(ctx, second); err != nil {
		t.Fatal(err)
	}
	evidence := memory.EvidenceRef{RefType: "test_run", RefID: "dependency-check",
		ProducedBy: "verifier", ProducerID: "verifier:dependency-test"}
	if _, err := writer.AddEdge(ctx, memory.EdgeRecord{SrcID: dependent.BlockID,
		DstID: source.BlockID, EdgeType: "cites", EvidenceRefs: []memory.EvidenceRef{evidence},
		CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	linked, err := index.EdgesFor(ctx, derived.BlockID)
	if err != nil || len(linked) != 2 {
		t.Fatalf("scoped graph neighbors differ: %+v, %v", linked, err)
	}
	between, err := index.EdgesBetween(ctx, []string{source.BlockID, derived.BlockID, dependent.BlockID}, 10)
	if err != nil || len(between) != 1 || between[0].EdgeType != "cites" {
		t.Fatalf("evidence-bearing graph slice differs: %+v, %v", between, err)
	}
	cycle := memory.EdgeRecord{SrcID: source.BlockID, DstID: dependent.BlockID,
		EdgeType: "derived_from", CreatedAt: "2024-01-01T00:00:00Z"}
	if _, err := writer.AddEdge(ctx, cycle); err == nil {
		t.Fatal("transitive derivation cycle was accepted")
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	plan.RecordIDs = []string{source.BlockID, derived.BlockID, dependent.BlockID}
	before, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(before) != 3 {
		t.Fatalf("valid derivation chain was hidden: %+v, %v", before, err)
	}
	corrected := source
	corrected.BlockID = "source_corrected"
	corrected.RevisionID = "rev_source_2"
	corrected.RevisionOf = source.BlockID
	corrected.SupersedesIDs = []string{source.BlockID}
	corrected.Content = "Corrected measured finding"
	corrected.ValidFrom = "2024-02-01T00:00:00Z"
	corrected.RecordedAt = corrected.ValidFrom
	corrected.UpdatedAt = corrected.ValidFrom
	membership := *corrected.BlockMembership
	membership.RecordID = corrected.BlockID
	membership.RecordRevisionID = corrected.RevisionID
	membership.ValidFrom = corrected.ValidFrom
	membership.MembershipRevisionID = membership.RevisionHash()
	corrected.BlockMembership = &membership
	pending, err := writer.PrepareSuccessorWrite(ctx, corrected, "correction", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	plan.RecordIDs = append(plan.RecordIDs, corrected.BlockID)
	current, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(current) != 1 || current[0].BlockID != corrected.BlockID {
		t.Fatalf("invalidated descendants leaked after correction: %+v, %v", current, err)
	}
	validation, err := index.ValidateDependencies(ctx, dependent.BlockID, DependencyOptions{})
	if err != nil || validation.Status != "invalidated" || len(validation.CheckedRecordIDs) != 3 {
		t.Fatalf("transitive invalidation receipt differs: %+v, %v", validation, err)
	}
	issueFound := false
	for _, issue := range validation.Issues {
		if issue.RecordID == source.BlockID && issue.Code == "revision_not_current" {
			issueFound = true
		}
	}
	if !issueFound {
		t.Fatalf("corrected source was not identified: %+v", validation.Issues)
	}
	historical := plan
	historical.TemporalMode = "historical"
	historical.AsOf = "2024-01-15T00:00:00Z"
	previous, err := index.ReadExactRecords(ctx, historical)
	if err != nil || len(previous) != 3 {
		t.Fatalf("historical derivation chain disappeared: %+v, %v", previous, err)
	}
	validThen, err := index.ValidateDependencies(ctx, dependent.BlockID, DependencyOptions{AsOf: historical.AsOf})
	if err != nil || validThen.Status != "valid" {
		t.Fatalf("historical dependency receipt differs: %+v, %v", validThen, err)
	}
	for _, limits := range []DependencyOptions{{MaxNodes: 1}, {MaxEdges: 1}} {
		bounded, err := index.ValidateDependencies(ctx, dependent.BlockID, limits)
		if err != nil || bounded.Status != "incomplete" {
			t.Fatalf("dependency budget was ignored: %+v, %v", bounded, err)
		}
	}
}
