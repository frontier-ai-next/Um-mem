package memorymap

import "testing"

func TestSummaryPreservesPublicFieldOrderAndUnicode(t *testing.T) {
	value, err := Summary(Data{WorkspaceID: "проект", RecordCount: 2,
		BlockCount: 3, MetaBlockCount: 1, IndexedRecords: 1,
		SemanticModel: "model", Kinds: map[string]int{"fact": 2},
		Edges: []SemanticEdge{{Draw: true}}})
	if err != nil || value != `{"workspace_id": "проект", "records": 2, "blocks": 3, "meta_blocks": 1, "indexed": 1, "model": "model", "kinds": {"fact": 2}, "semantic_links": 1}` {
		t.Fatalf("memory-map summary differs from the public shape: %q, %v", value, err)
	}
}
