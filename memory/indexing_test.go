package memory

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestIndexDocumentCompilesExactAndReverseProvenance(t *testing.T) {
	fixture, err := os.ReadFile("testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.Metadata = map[string]any{
		"paths":       []any{"src/cafe\u0301.go"},
		"config_keys": []any{"release.channel"},
		"package_id":  "um-mem",
		"version":     2.4, // Python's index skips non-sequence scalar metadata.
	}
	record.ChildClaims = []json.RawMessage{json.RawMessage(`{
		"origin_segments":[{"origin":{"channel":"tool_result","origin_id":"tool:pytest:raw-1"},
		"independence_domain":"pytest:independent:tool","evidence_refs":[{"ref_id":"raw-source"}]}],
		"derivation_receipt":{"derived_id":"derived:release-endpoint"}}`)}
	document, err := CompileIndexDocument(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []ExactTerm{
		{Type: "path", Value: "src/café.go"},
		{Type: "config_key", Value: "release.channel"},
		{Type: "origin_channel", Value: "tool_result"},
		{Type: "raw_source_ref", Value: "raw-source"},
	} {
		if !slices.Contains(document.ExactTerms, wanted) {
			t.Fatalf("missing exact term %+v: %+v", wanted, document.ExactTerms)
		}
	}
	if slices.Contains(document.ExactTerms, ExactTerm{Type: "version", Value: "2.4"}) {
		t.Fatal("non-sequence scalar metadata became an exact term")
	}
	if len(document.ProvenanceLinks) != 2 ||
		document.ProvenanceLinks[0] != (ProvenanceLink{"derived:release-endpoint", "raw-source"}) ||
		document.ProvenanceLinks[1] != (ProvenanceLink{"derived:release-endpoint", "tool:pytest:raw-1"}) {
		t.Fatalf("reverse provenance links differ: %+v", document.ProvenanceLinks)
	}
}
