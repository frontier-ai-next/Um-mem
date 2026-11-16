package memory

import (
	"slices"
	"testing"
)

func TestConsolidationSourceEnvelopeKeepsTransitiveRawCoverage(t *testing.T) {
	ref := EvidenceRef{RefType: "verification_report", RefID: "preservation",
		ProducedBy: "verifier", ProducerID: "test"}
	manifest, err := NewPreservationManifest([]string{"episode-2", "episode-1"},
		[]string{"RELEASE_PORT_4318"}, []string{"episode-old"}, []EvidenceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	working, err := BuildConsolidationSourceEnvelope("fold-1", "working_fold", "task",
		[]string{"folded-branch-1"}, manifest)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := BuildConsolidationSourceEnvelope("durable-1", "durable_consolidation", "durable",
		[]string{"fold-1"}, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(working.TransitiveRawSourceIDs, []string{"episode-1", "episode-2"}) ||
		!slices.Equal(durable.TransitiveRawSourceIDs, working.TransitiveRawSourceIDs) ||
		slices.Equal(durable.DirectSourceIDs, durable.TransitiveRawSourceIDs) {
		t.Fatalf("fold and durable consolidation lost their distinct source identities: %+v %+v", working, durable)
	}
	if _, err := BuildConsolidationSourceEnvelope("bad", "working_fold", "durable",
		[]string{"fold-1"}, manifest); err == nil {
		t.Fatal("working fold claimed durable lifetime")
	}
	if _, err := NewPreservationManifest(nil, nil, nil, []EvidenceRef{ref}); err == nil {
		t.Fatal("preservation accepted no raw source IDs")
	}
}
