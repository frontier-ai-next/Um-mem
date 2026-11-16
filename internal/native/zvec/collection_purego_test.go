//go:build purego

package zvec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type fixedEmbedder struct{}

func (fixedEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = make([]float32, embeddingDimensions)
		vectors[i][383] = 1
	}
	return vectors, nil
}

func TestNativeProjectRoundTrip(t *testing.T) {
	if os.Getenv("ZVEC_LIBRARY_PATH") == "" {
		t.Skip("ZVEC_LIBRARY_PATH is unset")
	}
	data, err := os.ReadFile("testdata/python_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Record memory.MemoryRecord `json:"record"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("UM_MEM_TEST_GO_ZVEC_OUTPUT")
	if path == "" {
		path = filepath.Join(t.TempDir(), "collection")
	}
	collection, err := OpenNativeCollection(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer collection.Close()
	chunks, _ := Chunks(fixture.Record)
	for range 2 {
		id, err := collection.Project(context.Background(), fixture.Record, 9, fixedEmbedder{})
		if err != nil || id != chunks[0].ID {
			t.Fatalf("native projection failed: %s, %v", id, err)
		}
	}
	queryVector := make([]float32, embeddingDimensions)
	queryVector[383] = 1
	dense, lexical, err := collection.SearchCandidates(queryVector, "Ångström", 10, true)
	if err != nil || len(dense) == 0 || len(lexical) == 0 {
		t.Fatalf("native dense or lexical search failed: dense=%d lexical=%d err=%v", len(dense), len(lexical), err)
	}
	if dense[0].RecordID != fixture.Record.BlockID || dense[0].CommitRevision != 9 ||
		lexical[0].RecordID != fixture.Record.BlockID || lexical[0].CommitRevision != 9 {
		t.Fatalf("native candidates lost canonical identity: dense=%+v lexical=%+v", dense[0], lexical[0])
	}
	if _, err := collection.Project(context.Background(), fixture.Record, 10, fixedEmbedder{}); err == nil || !strings.Contains(err.Error(), "immutable commit_revision") {
		t.Fatalf("immutable revision conflict was not detected: %v", err)
	}
}
