//go:build purego

package zvec

import (
	"os"
	"testing"

	native "github.com/zvec-ai/zvec-go"
)

func TestReadPythonNativeCollection(t *testing.T) {
	path := os.Getenv("UM_MEM_TEST_PYTHON_ZVEC_COLLECTION")
	if path == "" {
		t.Skip("UM_MEM_TEST_PYTHON_ZVEC_COLLECTION is unset")
	}
	collection, err := OpenNativeCollection(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer collection.Close()
	got, err := collection.collection.Fetch([]string{"python-doc-1"}, &native.FetchOptions{IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	defer native.FreeDocs(got)
	if len(got) != 1 || got[0].GetPK() != "python-doc-1" {
		t.Fatalf("Go SDK did not retrieve Python document: %d docs", len(got))
	}
	content, err := got[0].GetStringField("content")
	if err != nil || content != "Ångström🧭 test" {
		t.Fatalf("Go SDK content differs from Python: %q, %v", content, err)
	}
	revision, err := got[0].GetInt64Field("commit_revision")
	if err != nil || revision != 7 {
		t.Fatalf("Go SDK revision differs from Python: %d, %v", revision, err)
	}
	vector, err := got[0].GetVectorFP32Field("embedding")
	if err != nil || len(vector) != embeddingDimensions || vector[383] != 1 {
		t.Fatalf("Go SDK vector differs from Python: %d dimensions, %v", len(vector), err)
	}
}
