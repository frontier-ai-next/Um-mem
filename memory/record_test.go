package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestCandidateCannotClaimDirectiveAuthority(t *testing.T) {
	record, err := NewMemoryRecord("Observed parser behavior", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := record.ValidateSafety(); err != nil {
		t.Fatal(err)
	}
	record.Authority = "directive"
	if err := record.ValidateSafety(); err == nil {
		t.Fatal("unverified candidate acquired directive authority")
	}
}

func TestMembershipIdentityPreservesPythonUnicodeAndHTMLBytes(t *testing.T) {
	staticMaterial := `["\u00e9&<","\u03bb"]`
	staticHash := sha256.Sum256([]byte(staticMaterial))
	if got, want := StaticBackendPartitionID("é&<", "λ"), "static:"+hex.EncodeToString(staticHash[:]); got != want {
		t.Fatalf("static partition ID differs from Python: %s", got)
	}
	membership := RecordBlockMembership{
		PrimaryBlockID: "λ&<", CatalogRevisionID: "cat", BackendPartitionID: "static:demo",
		SecondaryBlockIDs: []string{}, RecordID: "r", RecordRevisionID: "rev",
		WorkspaceID: "é", ValidFrom: "2024-01-01T00:00:00Z",
	}
	material := `{"backend_partition_id":"static:demo","catalog_revision_id":"cat","primary_block_id":"λ&<","record_id":"r","record_revision_id":"rev","secondary_block_ids":[],"valid_from":"2024-01-01T00:00:00Z","workspace_id":"é"}`
	identityHash := sha256.Sum256([]byte(material))
	if got, want := membership.RevisionHash(), "membership_"+hex.EncodeToString(identityHash[:]); got != want {
		t.Fatalf("membership revision differs from Python: %s", got)
	}
}

func TestAcceptedMemoryRequiresDecisiveEvidence(t *testing.T) {
	record, err := NewMemoryRecord("The parser test passes", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.Status = "accepted"
	record.VerificationStatus = "verified"
	record.TrustLevel = "workspace_verified"
	record.PromotionDecision = "accepted"
	record.VerificationMethods = []string{"test_run"}
	record.EvidenceRefs = []EvidenceRef{{RefType: "test_run", RefID: "run-1", ProducedBy: "verifier"}}
	if err := record.ValidateSafety(); err == nil {
		t.Fatal("evidence without a producer identity was accepted")
	}
	record.EvidenceRefs[0].ProducerID = "pytest"
	if err := record.ValidateSafety(); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedMemoryIsNotInjectable(t *testing.T) {
	record, err := NewMemoryRecord("private data", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.Sensitivity = "secret"
	if !record.IsProtected() {
		t.Fatal("secret memory was not protected")
	}
	record.Sensitivity = "internal"
	record.ContentMode = "non_injectable"
	if !record.IsProtected() {
		t.Fatal("non-injectable memory was not protected")
	}
}

func TestRecordRejectsGovernanceMetadataAndInvalidRevision(t *testing.T) {
	record, err := NewMemoryRecord("fact", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.Metadata["verification_status"] = "verified"
	if err := record.ValidateSafety(); err == nil {
		t.Fatal("metadata spoofed verification status")
	}
	delete(record.Metadata, "verification_status")
	record.RevisionOf = "old"
	if err := record.ValidateSafety(); err == nil {
		t.Fatal("revision without exact predecessor was accepted")
	}
	record.SupersedesIDs = []string{"old"}
	if err := record.ValidateSafety(); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalRecordJSONRoundTrip(t *testing.T) {
	fixture, err := os.ReadFile("testdata/record_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.ValidateSafety(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var original, roundTrip map[string]any
	if err := json.Unmarshal(fixture, &original); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, roundTrip) {
		t.Fatal("Go record model dropped or changed a canonical JSON field")
	}
}

func TestCanonicalRecordRejectsUnknownAndMissingFields(t *testing.T) {
	fixture, err := os.ReadFile("testdata/record_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(fixture, &payload); err != nil {
		t.Fatal(err)
	}
	delete(payload, "workspace_id")
	missing, _ := json.Marshal(payload)
	if _, err := DecodeCanonicalRecord(missing); err == nil {
		t.Fatal("missing canonical field was accepted")
	}
	payload["workspace_id"] = "alpha"
	payload["unexpected"] = true
	unknown, _ := json.Marshal(payload)
	if _, err := DecodeCanonicalRecord(unknown); err == nil {
		t.Fatal("unknown canonical field was accepted")
	}
}

func TestBoundMembershipHashMatchesCanonicalFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	membership := record.BlockMembership
	if membership.BackendPartitionID != StaticBackendPartitionID("team", "leaf") ||
		membership.MembershipRevisionID != membership.RevisionHash() {
		t.Fatal("Go membership identity differs from the canonical Python fixture")
	}
	membership.PrimaryBlockID = "different"
	if err := record.ValidateSafety(); err == nil {
		t.Fatal("membership tampering was accepted")
	}
}
