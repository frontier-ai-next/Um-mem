package memory

import (
	"slices"
	"testing"
)

func TestMaterializedViewInvalidatesWhenCanonicalSourceChanges(t *testing.T) {
	source := MemoryRecord{BlockID: "episode-1", RevisionID: "episode-1@1", WorkspaceID: "ws",
		Content: "Preserve exact literal RELEASE_PORT_4318.", Scope: "workspace",
		Authority: "advisory", Status: "candidate", ValidFrom: "2026-07-15T00:00:00Z",
		ValidTo: "2026-07-16T00:00:00Z"}
	revision, err := MaterializedSourceRevisionFromRecord(source)
	if err != nil {
		t.Fatal(err)
	}
	if revision.SourceSHA256 != "sha256:75700a1ac9f34e14acb5e6c08c5b922be9707a68af33cfbacd68149961de6d98" {
		t.Fatalf("materialized source hash differs from Python: %s", revision.SourceSHA256)
	}
	ref := EvidenceRef{RefType: "verification_report", RefID: "view", ProducedBy: "verifier", ProducerID: "test"}
	view, err := NewMaterializedViewRevision("view-release", "ws", source.Content,
		[]MaterializedSourceRevision{revision}, []EvidenceRef{ref})
	if err != nil {
		t.Fatal(err)
	}
	active, err := view.Revalidate([]MemoryRecord{source}, "2026-07-15T12:00:00Z")
	if err != nil || active.Status != "active" || len(active.InvalidationReasons) != 0 {
		t.Fatalf("valid source invalidated: %+v, %v", active, err)
	}
	cases := []struct {
		name         string
		record       MemoryRecord
		asOf, reason string
	}{
		{"revision", func() MemoryRecord { r := source; r.RevisionID = "episode-1@2"; return r }(), "2026-07-15T12:00:00Z", "source_revision_changed:episode-1"},
		{"content", func() MemoryRecord {
			r := source
			r.Content = "Preserve different literal RELEASE_PORT_4318."
			return r
		}(), "2026-07-15T12:00:00Z", "source_hash_changed:episode-1"},
		{"lifecycle", func() MemoryRecord { r := source; r.Status = "retracted"; return r }(), "2026-07-15T12:00:00Z", "source_lifecycle_invalid:episode-1"},
		{"expired", source, "2026-07-17T00:00:00Z", "source_outside_validity:episode-1"},
		{"exclusive end", source, source.ValidTo, "source_outside_validity:episode-1"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			updated, err := view.Revalidate([]MemoryRecord{test.record}, test.asOf)
			if err != nil || updated.Status != "invalidated" || !slices.Contains(updated.InvalidationReasons, test.reason) {
				t.Fatalf("changed canonical source remained active: %+v, %v", updated, err)
			}
		})
	}
	missing, err := view.Revalidate(nil, "2026-07-15T12:00:00Z")
	if err != nil || !slices.Contains(missing.InvalidationReasons, "source_missing:episode-1") {
		t.Fatalf("missing source remained active: %+v, %v", missing, err)
	}
	if view.Status != "active" || len(view.InvalidationReasons) != 0 {
		t.Fatal("revalidation mutated original view")
	}
}
