package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestBlockLocalDeepReaderBoundsAndAuthority(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	primary, err := DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	primary.SourceBackend = "mempalace"
	primary.Content = "materialized procedure"
	raw := primary
	raw.BlockID, raw.RevisionID = "raw-event", "raw-revision"
	raw.Content = "canonical raw tool span"
	rawMembership := *primary.BlockMembership
	rawMembership.PrimaryBlockID = "raw_events"
	raw.BlockMembership = &rawMembership
	region := BlockLocalEvidenceRegion{BackendID: "mempalace", WorkspaceID: "alpha",
		LogicalBlockID: "leaf", PrimaryRecordIDs: []string{primary.BlockID}, Records: []MemoryRecord{primary, raw}}
	result, err := ReadBlockLocalEvidenceRegion(region, func(id string) []string {
		if id == primary.BlockID {
			return []string{"missing", "raw-event", "raw-event"}
		}
		return nil
	}, 4, 4)
	if err != nil || len(result.Items) != 2 || result.Items[0].EvidenceKind != "selected_evidence" ||
		result.Items[1].EvidenceKind != "canonical_raw_span" || result.Items[1].SourceRecordID != primary.BlockID ||
		result.Items[1].Content != raw.Content || result.BackendPath != "selected_backend" ||
		result.ReverseProvenanceDepth != 1 || result.WritePermission || result.PromotionPermission ||
		result.Items[1].WritePermission || result.Items[1].PromotionPermission ||
		result.Items[1].Authority != "advisory" || result.Items[1].Surface != "supplemental_evidence" {
		t.Fatalf("deep reader widened scope or authority: %+v %v", result, err)
	}
	digest := sha256.Sum256([]byte(raw.Content))
	if result.Items[1].CanonicalContentSHA256 != "sha256:"+hex.EncodeToString(digest[:]) {
		t.Fatal("canonical span content hash changed")
	}
	foreign := raw
	foreign.WorkspaceID = "other"
	region.Records = []MemoryRecord{primary, foreign}
	if _, err := ReadBlockLocalEvidenceRegion(region, func(string) []string { return nil }, 4, 4); err == nil {
		t.Fatal("foreign workspace record entered deep reader")
	}
	region.Records = []MemoryRecord{primary, raw}
	region.LogicalBlockID = "unrelated"
	if _, err := ReadBlockLocalEvidenceRegion(region, func(string) []string { return nil }, 4, 4); err == nil {
		t.Fatal("primary record escaped selected logical block")
	}
}
