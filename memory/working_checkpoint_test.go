package memory

import (
	"encoding/json"
	"slices"
	"testing"
)

// These hashes were produced with the Python working_continuity hashing rules.
func TestWorkingCheckpointPythonHashParity(t *testing.T) {
	record := MemoryRecord{BlockID: "raw-1", CanonicalKey: "raw:1", RevisionID: "rev-1",
		WorkspaceID: "workspace", ProfileID: "default", SessionID: "session-1",
		TaskID: "task-1", Content: "line 1\nline 2"}
	key, err := WorkingStateCanonicalKey("workspace", "default", "owner-1", "session-1", "task-1")
	if err != nil || key != "workspace:task_state:working_continuity:962c2d9041e0e57dfe653a53" {
		t.Fatalf("canonical key: %q, %v", key, err)
	}
	source, err := ContinuationSourceHash(record)
	if err != nil || source != "sha256:b9533e3da35c659721e5a6c1f0d193cbf016c5d0c7c269fb7dba1fd93b96a439" {
		t.Fatalf("source hash: %q, %v", source, err)
	}
	window, err := ContinuationWindowHash([]MemoryRecord{record})
	if err != nil || window != "sha256:aebc220f23e000f8f7c35671f99ff61bab99ea5afe72047775a31a031057080c" {
		t.Fatalf("window hash: %q, %v", window, err)
	}
}

func TestWorkingCheckpointJSONRejectsMalformedPairAndNullArray(t *testing.T) {
	for name, raw := range map[string]string{
		"extra hash element":  `{"source_ref_sha256":[["raw","sha256:abc","extra"]]}`,
		"null optional tuple": `{"plan":null}`,
		"unknown field":       `{"unexpected":true}`,
	} {
		var checkpoint WorkingStateCheckpoint
		if err := json.Unmarshal([]byte(raw), &checkpoint); err == nil {
			t.Fatalf("%s passed the checkpoint wire contract", name)
		}
	}
}

func checkpointFixture() WorkingStateCheckpoint {
	refs := []string{"raw-1", "task-anchor", "system-anchor", "tail-1", "page-1"}
	hashes := make([][2]string, 0, len(refs))
	for _, ref := range refs {
		hashes = append(hashes, [2]string{ref,
			"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	}
	return WorkingStateCheckpoint{WorkspaceID: "workspace", ProfileID: "default",
		OwnerGeneration: "owner-1", RequestedBlockID: "task_state", WorkingView: "Continue the task.",
		SourceRefSHA256: hashes, HostEventID: "host-event-1", SessionID: "session-1", TaskID: "task-1",
		SourceEventRefs: []string{"raw-1"}, SourceEventSHA256: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		TaskAnchorRefs: []string{"task-anchor"}, SystemAnchorRefs: []string{"system-anchor"},
		ExactRecentTailRefs: []string{"tail-1"}, EvidencePageRefs: [][]string{{"page-1"}},
		RollbackOrPageBackRef: "page-1", ValidFrom: "2026-07-14T09:00:00Z",
		ValidTo: "2026-07-14T10:00:00Z", SchemaVersion: CurrentSchemaVersion,
		HardPolicyVersion: 3, SemanticCatalogVersion: "catalog-1", HostVersion: "host-1",
		ModelVersion: "model-1", PromptVersion: "prompt-1", ContinuationSufficiency: "sufficient",
		EvidenceRefs: []EvidenceRef{{RefType: "session_event", RefID: "host-event-1",
			ProducedBy: "harness", ProducerID: "host"}}}
}

func TestWorkingCheckpointNormalizesManifestAndRejectsAnchorOrHashDrift(t *testing.T) {
	checkpoint := checkpointFixture()
	subject, err := CheckpointVerificationSubject(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if subject.PayloadHash != "9fced25624c590fc23e7d7f5493199421f985cd8003fc7fc8273f80e64b262e2" ||
		subject.SourceEvidenceHash != "7486c7ae1cf8df3fda8a892bab6d66888b9939b263f8bbb2056f3c1ae3a0df56" {
		t.Fatalf("candidate verifier subject differs from Python: %+v", subject)
	}
	packet, err := BuildContinuationPacket(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(packet.ExactLiteralManifest) != 9 ||
		!slices.Equal(packet.ExactLiteralManifest[8].Literals,
			[]string{"raw-1", "task-anchor", "system-anchor", "tail-1", "page-1"}) {
		t.Fatalf("literal manifest lost raw coverage: %#v", packet.ExactLiteralManifest)
	}
	if packet.Checkpoint.Plan == nil || packet.Checkpoint.RejectedApproaches == nil {
		t.Fatal("optional Python tuples must be JSON arrays")
	}
	checkpoint.SourceRefSHA256 = checkpoint.SourceRefSHA256[:4]
	if _, err := NormalizeWorkingStateCheckpoint(checkpoint); err == nil {
		t.Fatal("missing raw source pin accepted")
	}
	checkpoint = checkpointFixture()
	checkpoint.ExactLiteralManifest = []ContinuationLiteralSet{{Dimension: "goals_constraints", Literals: []string{"wrong"}}}
	if _, err := NormalizeWorkingStateCheckpoint(checkpoint); err == nil {
		t.Fatal("literal manifest drift accepted")
	}
}
