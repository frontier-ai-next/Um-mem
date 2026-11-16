package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestExactTermsIntersectAcrossTypesAndRespectScope(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	first, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	first.Metadata = map[string]any{
		"paths": []any{"src/cafe\u0301.go"}, "symbols": []any{"Release.publish"},
		"package_id": "um-mem", "config_keys": []any{"release.channel"},
		"endpoints": []any{"POST /v1/releases"}, "component_id": "release",
	}
	first.RepositoryID = "repo:um-mem"
	first.ChildClaims = []json.RawMessage{json.RawMessage(`{
		"origin_segments":[{"origin":{"channel":"tool_result","origin_id":"tool:release"},
		"independence_domain":"release:test","evidence_refs":[{"ref_id":"raw-release"}]}],
		"derivation_receipt":{"derived_id":"derived:release"}}`)}
	second := first
	second.BlockID = "mem_second"
	second.Content = "Second path"
	second.Metadata = map[string]any{
		"paths": []any{"src/other.go"}, "symbols": []any{"Release.publish"},
		"package_id": "other-package",
	}
	secondMembership := *first.BlockMembership
	secondMembership.RecordID = second.BlockID
	secondMembership.MembershipRevisionID = secondMembership.RevisionHash()
	second.BlockMembership = &secondMembership
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	for _, record := range []memory.MemoryRecord{first, second} {
		pending, err := writer.PrepareRootWrite(ctx, record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, skippedNativeProjector{}); err != nil {
			t.Fatal(err)
		}
	}
	indexed, err := index.ExactIndexTerms(ctx, first.BlockID)
	if err != nil || len(indexed["path"]) != 1 || indexed["path"][0] != "src/café.go" ||
		len(indexed["config_key"]) != 1 || indexed["config_key"][0] != "release.channel" {
		t.Fatalf("exact term read differs from canonical index: %+v, %v", indexed, err)
	}
	structure, err := index.ProjectStructureNodes(ctx, first.BlockID)
	if err != nil || len(structure) != 5 || structure[0].Metadata["structure_kind"] != "repository" ||
		structure[4].Metadata["structure_kind"] != "symbol" {
		t.Fatalf("project structure read lost hierarchy: %+v, %v", structure, err)
	}
	provenance, err := index.ReverseProvenanceSources(ctx, "derived:release")
	if err != nil || len(provenance) != 2 || provenance[0] != "raw-release" || provenance[1] != "tool:release" {
		t.Fatalf("reverse provenance index lost sources: %+v, %v", provenance, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	plan.Paths = []string{"src/café.go"}
	plan.Symbols = []string{"Release.publish"}
	selected, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 1 || selected[0].BlockID != first.BlockID {
		t.Fatalf("exact path/symbol intersection failed: %+v, %v", selected, err)
	}
	plan.Paths = []string{"src/café.go", "src/other.go"}
	selected, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 2 {
		t.Fatalf("multiple paths were not ORed: %+v, %v", selected, err)
	}
	plan.Facets = map[string][]string{"package": {"um-mem"}}
	selected, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 1 || selected[0].BlockID != first.BlockID {
		t.Fatalf("package facet did not intersect: %+v, %v", selected, err)
	}
	plan.RecordIDs = []string{second.BlockID}
	selected, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 0 {
		t.Fatalf("caller record IDs escaped exact facet intersection: %+v, %v", selected, err)
	}
	plan.RecordIDs = nil
	plan.Facets = nil
	plan.IncludeCandidates = false
	selected, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 0 {
		t.Fatalf("candidate escaped exact index safety filter: %+v, %v", selected, err)
	}
	otherProfile, err := OpenIndex(path, "alpha", "other")
	if err != nil {
		t.Fatal(err)
	}
	defer otherProfile.Close()
	plan.ProfileID = "other"
	plan.IncludeCandidates = true
	selected, err = otherProfile.ReadExactRecords(ctx, plan)
	if err != nil || len(selected) != 0 {
		t.Fatalf("profile leaked through exact index: %+v, %v", selected, err)
	}
	otherNodes, err := otherProfile.ProjectStructureNodes(ctx, first.BlockID)
	if err != nil || len(otherNodes) != 0 {
		t.Fatalf("profile leaked through structure nodes: %+v, %v", otherNodes, err)
	}
	otherProvenance, err := otherProfile.ReverseProvenanceSources(ctx, "derived:release")
	if err != nil || len(otherProvenance) != 0 {
		t.Fatalf("profile leaked through reverse provenance: %+v, %v", otherProvenance, err)
	}

	rows, err := writer.db.QueryContext(ctx, `SELECT payload_json FROM index_nodes WHERE workspace_id=? ORDER BY node_id`, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var repository memory.IndexNode
	firstKinds := make(map[string]memory.IndexNode)
	facets := make(map[string]bool)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		var node memory.IndexNode
		if err := json.Unmarshal([]byte(payload), &node); err != nil {
			t.Fatal(err)
		}
		ids, err := nodeRecordIDs(node.Metadata["related_record_ids"])
		if err != nil {
			t.Fatal(err)
		}
		if node.NodeType == "project_structure" {
			kind := node.Metadata["structure_kind"].(string)
			if kind == "repository" {
				repository = node
			}
			for _, id := range ids {
				if id == first.BlockID {
					firstKinds[kind] = node
				}
			}
		} else if node.NodeType == "exact_facet" {
			facets[node.Metadata["term_type"].(string)] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	shared, err := nodeRecordIDs(repository.Metadata["related_record_ids"])
	if err != nil || len(shared) != 2 || shared[0] != first.BlockID || shared[1] != second.BlockID {
		t.Fatalf("shared repository node lost record references: %v, %v", shared, err)
	}
	parentID := ""
	for _, kind := range []string{"repository", "package", "component", "file", "symbol"} {
		node, exists := firstKinds[kind]
		if !exists || node.Metadata["parent_id"] != parentID {
			t.Fatalf("project structure chain broken at %s: %+v", kind, node)
		}
		parentID = node.NodeID
	}
	if !facets["config_key"] || !facets["endpoint"] || facets["artifact_hash"] {
		t.Fatalf("exact facet node types differ from the retrieval dimensions: %v", facets)
	}
}
