package zvec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestPythonZvecPartitionAndChunks(t *testing.T) {
	fixture, err := os.ReadFile("testdata/python_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Record            memory.MemoryRecord `json:"record"`
		Partition         PartitionIdentity   `json:"partition"`
		Chunks            []Chunk             `json:"chunks"`
		SourceSHA256      string              `json:"source_sha256"`
		ProjectionLockKey string              `json:"projection_lock_key"`
	}
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	partition, err := Partition(expected.Record)
	if err != nil || partition != expected.Partition {
		t.Fatalf("Go Zvec partition identity differs from Python: %+v, %v", partition, err)
	}
	chunks, sourceHash := Chunks(expected.Record)
	if sourceHash != expected.SourceSHA256 || !reflect.DeepEqual(chunks, expected.Chunks) {
		t.Fatalf("Go Zvec native documents differ from Python: hash %q, chunks %+v", sourceHash, chunks)
	}
	candidate := NativeCandidate{ChunkID: chunks[0].ID, RecordID: expected.Record.BlockID,
		SourceSHA256: sourceHash, CommitRevision: 9}
	if err := ValidateCandidate(candidate, expected.Record, 9, partition.PartitionID); err != nil {
		t.Fatalf("matching canonical candidate rejected: %v", err)
	}
	for name, altered := range map[string]NativeCandidate{
		"record":   {ChunkID: candidate.ChunkID, RecordID: "other", SourceSHA256: sourceHash, CommitRevision: 9},
		"revision": {ChunkID: candidate.ChunkID, RecordID: candidate.RecordID, SourceSHA256: sourceHash, CommitRevision: 10},
		"hash":     {ChunkID: candidate.ChunkID, RecordID: candidate.RecordID, SourceSHA256: "other", CommitRevision: 9},
		"chunk":    {ChunkID: "other", RecordID: candidate.RecordID, SourceSHA256: sourceHash, CommitRevision: 9},
	} {
		if err := ValidateCandidate(altered, expected.Record, 9, partition.PartitionID); err == nil {
			t.Errorf("altered %s candidate was admitted", name)
		}
	}
	if err := ValidateCandidate(candidate, expected.Record, 9, "other"); err == nil {
		t.Error("candidate from another physical partition was admitted")
	}
	expected.Record.ProfileID = "team"
	lockPath, err := ProjectionLockPath(t.TempDir(), expected.Record)
	if err != nil || filepath.Base(lockPath) != "UM_MEM_index.sqlite.projection-"+expected.ProjectionLockKey+".lock" {
		t.Fatalf("Go projection lock differs from Python: %q, %v", lockPath, err)
	}
}
