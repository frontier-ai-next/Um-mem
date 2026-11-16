package mempalace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestPythonMemPalacePartitionAndCollection(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("testdata/python_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Partition      PartitionIdentity `json:"partition"`
		Store          string            `json:"store"`
		CollectionName string            `json:"collection_name"`
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	partition, err := Partition(record)
	if err != nil || partition != expected.Partition {
		t.Fatalf("MemPalace partition differs from Python: %+v, %v", partition, err)
	}
	if store := StoreForBlock(record); store != expected.Store {
		t.Fatalf("MemPalace store differs from Python: %q", store)
	}
	name, err := CollectionName(record)
	if err != nil || name != expected.CollectionName {
		t.Fatalf("MemPalace collection differs from Python: %q, %v", name, err)
	}
}

func TestPythonMemPalaceLongWindows(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM tokenizer fixture is unavailable")
	}
	tokenizer, err := openTokenizer(modelDir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	record.Content = strings.Repeat("café 東京 alpha beta gamma delta epsilon zeta ", 100)
	rows, err := NativeRows(record, 7, tokenizer)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("testdata/python_windows.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected []struct {
		ID       string `json:"id"`
		Document string `json:"document"`
		Start    *int   `json:"start"`
		End      *int   `json:"end"`
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(expected) {
		t.Fatalf("MemPalace window count %d differs from Python %d", len(rows), len(expected))
	}
	for i, row := range rows {
		if row.ID != expected[i].ID || row.Document != expected[i].Document {
			t.Fatalf("MemPalace window %d differs from Python: %q", i, row.ID)
		}
		if expected[i].Start != nil && !reflect.DeepEqual(row.Metadata["window_start_byte"], *expected[i].Start) {
			t.Fatalf("MemPalace start offset differs at %d: %v != %d", i, row.Metadata["window_start_byte"], *expected[i].Start)
		}
		if expected[i].End != nil && !reflect.DeepEqual(row.Metadata["window_end_byte"], *expected[i].End) {
			t.Fatalf("MemPalace end offset differs at %d: %v != %d", i, row.Metadata["window_end_byte"], *expected[i].End)
		}
	}
}

func TestPythonMemPalaceMetadata(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := nativeMetadata(record, 7)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("testdata/python_metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]any
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) || !sameNativeMetadata(actual, expected) {
		for key, value := range actual {
			if !sameNativeMetadata(map[string]any{key: value}, map[string]any{key: expected[key]}) {
				t.Errorf("MemPalace metadata %s differs: %v != %v", key, value, expected[key])
			}
		}
	}
}

func TestNativeMetadataKeepsLargeIntegerIdentity(t *testing.T) {
	if sameJSONValue(`{"counter":9007199254740992}`, `{"counter":9007199254740993}`) {
		t.Fatal("MemPalace immutable metadata comparison lost large integer identity")
	}
}
