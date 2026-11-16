package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMutationIdentitiesMatchPython(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "mutation_ids_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		RecordFixture       string `json:"record_fixture"`
		CanonicalKey        string `json:"canonical_key"`
		BlockIDForKey       string `json:"block_id_for_key"`
		RevisionID          string `json:"revision_id"`
		SuccessorRevisionID string `json:"successor_revision_id"`
		BlockIDForRevision  string `json:"block_id_for_revision"`
		DuplicateClusterID  string `json:"duplicate_cluster_id"`
		UnicodeKey          string `json:"unicode_key"`
	}
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	recordJSON, err := os.ReadFile(filepath.Join("testdata", expected.RecordFixture))
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeCanonicalRecord(recordJSON)
	if err != nil {
		t.Fatal(err)
	}
	key, err := DeriveCanonicalKey(record.WorkspaceID, record.ProfileID, record.Kind, record.BlockID)
	if err != nil || key != expected.CanonicalKey {
		t.Fatalf("canonical key differs from Python: %q, %v", key, err)
	}
	if got := BlockIDForCanonicalKey(key); got != expected.BlockIDForKey {
		t.Fatalf("canonical block ID differs from Python: %q", got)
	}
	revision, err := RevisionIDForRecord(key, record, "insert", "")
	if err != nil || revision != expected.RevisionID {
		t.Fatalf("root revision differs from Python: %q, %v", revision, err)
	}
	successor, err := RevisionIDForRecord(key, record, "correction", "rev_previous")
	if err != nil || successor != expected.SuccessorRevisionID {
		t.Fatalf("successor revision differs from Python: %q, %v", successor, err)
	}
	if got := BlockIDForRevisionID(revision); got != expected.BlockIDForRevision {
		t.Fatalf("revision block ID differs from Python: %q", got)
	}
	duplicate, err := DuplicateClusterID(record)
	if err != nil || duplicate != expected.DuplicateClusterID {
		t.Fatalf("duplicate cluster differs from Python: %q, %v", duplicate, err)
	}
	unicodeKey, err := DeriveCanonicalKey("проект", "default", "наблюдение", "запись, 1")
	if err != nil || unicodeKey != expected.UnicodeKey {
		t.Fatalf("Unicode canonical key differs from Python: %q, %v", unicodeKey, err)
	}
}
