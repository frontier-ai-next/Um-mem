package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type verifiedProjector struct{}

func (verifiedProjector) BackendID() string                       { return "hindsight" }
func (verifiedProjector) RequiresNative(memory.MemoryRecord) bool { return true }
func (verifiedProjector) Project(_ context.Context, record memory.MemoryRecord, _ string, _ int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{NativeID: "UM-MEM:block:" + record.BlockID, Verified: true}, nil
}

func TestExactToolsOverMCP(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "mcp-test-session")
	ctx := context.Background()
	config := engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"}
	path, err := engine.IndexPath(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.RunID = "mcp-test-session"
	record.EvidenceRefs = []memory.EvidenceRef{{RefType: "test_run", RefID: "exact-evidence", ProducedBy: "verifier", ProducerID: "go-test"}}
	record.Metadata["source_segments"] = []any{"ignored-non-object", map[string]any{"source_id": "segment-evidence"}}
	record.Metadata["working_set_owner"] = map[string]any{"owner_id": "node-a", "generation_id": "generation-1"}
	writer, err := store.OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	staged, err := writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, verifiedProjector{}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	selected, err := engine.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server := New(selected, "alpha", "hindsight")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "go-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	listed, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	toolNames := map[string]bool{}
	for _, tool := range listed.Tools {
		toolNames[tool.Name] = true
	}
	if len(toolNames) != 46 || !toolNames["export_obsidian"] || !toolNames["memory_map"] || !toolNames["commit_working_checkpoint"] || !toolNames["consolidate_observations"] || !toolNames["rebuild_indexes"] || !toolNames["health"] || !toolNames["merge_blocks"] || !toolNames["split_block"] || !toolNames["resume_topology_evolution"] || !toolNames["abort_topology_evolution"] || !toolNames["pending_topology_evolutions"] || !toolNames["verification_subject"] || !toolNames["promote"] || !toolNames["procedure"] || !toolNames["convert_block_to_index"] || !toolNames["export_graph"] || !toolNames["block_map"] || !toolNames["expand_block"] || !toolNames["navigation_envelope"] || !toolNames["observe_maintenance"] || !toolNames["add_block_edge"] || !toolNames["remove_block_edge"] || !toolNames["add_edge"] || !toolNames["create_block"] || !toolNames["amend_block"] || !toolNames["archive_block"] || !toolNames["revive_block"] || !toolNames["move_block"] || !toolNames["delete_block"] || !toolNames["forget"] || !toolNames["amend"] || !toolNames["remember"] || !toolNames["get_active_mask"] || !toolNames["resolve_active_mask"] || !toolNames["set_active_mask"] || !toolNames["amend_active_mask"] || !toolNames["transition_active_mask"] || !toolNames["read_exact_record"] ||
		!toolNames["read_exact_records"] || !toolNames["projection_barrier"] || !toolNames["recall"] || !toolNames["recent"] || !toolNames["list_records"] || !toolNames["build_context"] || !toolNames["operation_status"] || !toolNames["read_working_set"] {
		t.Fatalf("Go MCP advertised an incomplete or unexpected tool: %+v, %v", listed, err)
	}
	graphResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "export_graph", Arguments: map[string]any{}})
	if err != nil || graphResult.IsError || graphResult.StructuredContent.(map[string]any)["ok"] != true ||
		graphResult.StructuredContent.(map[string]any)["audit"].(map[string]any)["record_count"] != float64(1) {
		t.Fatalf("MCP graph export lost canonical record: %+v, %v", graphResult, err)
	}
	statusResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "operation_status",
		Arguments: map[string]any{"operation_id": "unknown-operation"}})
	if err != nil || statusResult.IsError ||
		statusResult.StructuredContent.(map[string]any)["reason_code"] != "operation_not_found" {
		t.Fatalf("MCP operation status changed missing-receipt contract: %+v, %v", statusResult, err)
	}
	workingResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_working_set", Arguments: map[string]any{
		"owner_id": "node-a", "generation_id": "generation-1", "record_ids": []string{record.BlockID},
		"max_tokens": 512, "max_bytes": 2048,
	}})
	if err != nil || workingResult.IsError {
		t.Fatalf("MCP working-set read failed: %+v, %v", workingResult, err)
	}
	workingPayload := workingResult.StructuredContent.(map[string]any)
	if workingPayload["ok"] != true || workingPayload["visibility"] != "working_unverified" ||
		len(workingPayload["records"].([]any)) != 1 ||
		!strings.Contains(workingPayload["rendered_context"].(string), record.Content) {
		t.Fatalf("MCP working-set read lost owned candidate: %+v", workingPayload)
	}
	recent, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "recent", Arguments: map[string]any{"limit": 8}})
	if err != nil || recent.IsError {
		t.Fatalf("MCP recent call failed: %+v, %v", recent, err)
	}
	recentPayload := recent.StructuredContent.(map[string]any)
	if recentPayload["ok"] != true || recentPayload["count"] != float64(1) ||
		recentPayload["records"].([]any)[0].(map[string]any)["block_id"] != record.BlockID {
		t.Fatalf("MCP recent lost the current record: %+v", recentPayload)
	}
	listedRecords, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "list_records", Arguments: map[string]any{
		"scope": "workspace", "scope_id": "alpha", "limit": 32,
	}})
	if err != nil || listedRecords.IsError {
		t.Fatalf("MCP list_records call failed: %+v, %v", listedRecords, err)
	}
	listPayload := listedRecords.StructuredContent.(map[string]any)
	if listPayload["ok"] != true || listPayload["snapshot_count"] != float64(1) ||
		listPayload["records"].([]any)[0].(map[string]any)["record_id"] != record.BlockID {
		t.Fatalf("MCP list_records lost the canonical ID: %+v", listPayload)
	}
	recalled, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "recall", Arguments: map[string]any{
		"query": "Bound fact", "record_ids": []string{record.BlockID}, "max_tokens": 512,
	}})
	if err != nil || recalled.IsError {
		t.Fatalf("MCP recall call failed: %+v, %v", recalled, err)
	}
	recallPayload := recalled.StructuredContent.(map[string]any)
	recallHits, ok := recallPayload["hits"].([]any)
	if recallPayload["ok"] != true || !ok || len(recallHits) != 1 ||
		recallHits[0].(map[string]any)["source"] != record.BlockID {
		t.Fatalf("MCP recall lost the scoped record: %+v", recallPayload)
	}
	contextResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "build_context", Arguments: map[string]any{
		"query": "Bound fact", "record_ids": []string{record.BlockID}, "max_tokens": 512,
		"render_context": true,
	}})
	if err != nil || contextResult.IsError {
		t.Fatalf("MCP build_context failed: %+v, %v", contextResult, err)
	}
	contextPayload := contextResult.StructuredContent.(map[string]any)
	if contextPayload["ok"] != true || contextPayload["authority"] != "advisory" ||
		!strings.Contains(contextPayload["rendered_context"].(string), record.Content) ||
		len(contextPayload["items"].([]any)) != 1 {
		t.Fatalf("MCP build_context lost bounded evidence: %+v", contextPayload)
	}
	contextItem := contextPayload["items"].([]any)[0].(map[string]any)
	decisionID, _ := contextItem["metadata"].(map[string]any)["context_decision_id"].(string)
	if decisionID == "" {
		t.Fatal("MCP build_context did not attach a durable context decision")
	}
	decisionTrace, err := selected.MemoryDecisionTrace(ctx, decisionID)
	if err != nil || len(decisionTrace.UsageEvents) != 3 ||
		decisionTrace.UsageEvents[0].UsageType != "candidate" ||
		decisionTrace.UsageEvents[1].UsageType != "selected" ||
		decisionTrace.UsageEvents[2].UsageType != "rendered" ||
		decisionTrace.Decision.DownstreamOutcome != "neutral_pending_verifier" {
		t.Fatalf("MCP context journal differs from the neutral funnel: %+v %v", decisionTrace, err)
	}
	if decisionTrace.Decision.CatalogRevision != selected.CatalogRevisionID() ||
		decisionTrace.Decision.CostLatencyMS < 0 {
		t.Fatalf("context journal lost measured cost or catalog: %+v", decisionTrace.Decision)
	}
	operatorReport, err := selected.MemoryOperatorReport(ctx, decisionID)
	if err != nil || len(operatorReport.Entries) != 1 || operatorReport.Entries[0].Disposition != "unknown" {
		t.Fatalf("missing action-use callback was inferred as nonuse: %+v %v", operatorReport, err)
	}
	repeatedContext, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "build_context", Arguments: map[string]any{
		"query": "Bound fact", "record_ids": []string{record.BlockID}, "max_tokens": 512,
	}})
	if err != nil || repeatedContext.IsError {
		t.Fatalf("repeat context failed: %+v %v", repeatedContext, err)
	}
	repeatedItem := repeatedContext.StructuredContent.(map[string]any)["items"].([]any)[0].(map[string]any)
	repeatedID, _ := repeatedItem["metadata"].(map[string]any)["context_decision_id"].(string)
	if repeatedID == "" || repeatedID == decisionID {
		t.Fatal("repeated context collided with the first decision event")
	}
	explainBudget := 512
	explanation, err := ExplainRecallPayload(ctx, selected, ExplainRecallRequest{
		RecallArgs:   RecallArgs{Query: "Bound fact", RecordIDs: []string{record.BlockID}, MaxTokens: &explainBudget},
		GoldBlockIDs: []string{record.BlockID}, GoldLiterals: []string{record.Content},
	}, "alpha", "hindsight", []string{"workspace:alpha"}, record.RunID)
	if err != nil || explanation.RunID != record.RunID || explanation.PromptIncludesTrace ||
		len(explanation.CandidateRecordIDs) != 1 || explanation.CandidateRecordIDs[0] != record.BlockID ||
		len(explanation.RenderedRecordIDs) != 1 || explanation.RenderedRecordIDs[0] != record.BlockID ||
		!explanation.QualityGate.Passed || explanation.QualityGate.Status != "passed" ||
		len(explanation.RenderedContextSHA256) != 64 {
		t.Fatalf("recall explanation lost bounded evidence or quality gate: %+v %v", explanation, err)
	}
	wideLimit := 100
	deep, err := DeepReadPayload(ctx, selected, DeepReadRequest{
		RecallArgs:     RecallArgs{Query: "Bound fact", RecordIDs: []string{record.BlockID}, Limit: &wideLimit},
		RunID:          record.RunID,
		LogicalBlockID: "leaf",
		BlockDescriptors: []map[string]any{{"block_id": "leaf", "guidance": map[string]any{
			"block_id": "leaf", "synopsis": "Bound facts", "use_when": []string{"exact lookup"},
			"avoid_when": []string{},
		}}},
	}, "alpha", "hindsight")
	if err != nil || deep.Trace.LogicalBlockID != "leaf" || deep.Trace.Limit != 32 ||
		deep.Trace.HostActionsAllowed || deep.Trace.WritePermission || deep.Trace.PromotionPermission ||
		deep.Trace.ReverseProvenanceDepth != 1 || deep.Trace.FinalOutput != "evidence" ||
		len(deep.Lease.Items) != 1 || deep.Lease.Items[0].Source != record.BlockID ||
		deep.Lease.Items[0].Authority != "non_directive" ||
		!strings.Contains(deep.Lease.RenderedContext, record.Content) ||
		strings.Contains(deep.Lease.RenderedContext, "deep_read_trace") {
		t.Fatalf("deep read escaped block or contaminated prompt: %+v %v", deep, err)
	}
	if _, err := DeepReadPayload(ctx, selected, DeepReadRequest{
		RecallArgs:     RecallArgs{Query: "Bound fact", LogicalBlockIDs: []string{"other"}},
		LogicalBlockID: "leaf",
	}, "alpha", "hindsight"); err == nil {
		t.Fatal("deep reader accepted a second logical block")
	}
	barrier, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "projection_barrier",
		Arguments: map[string]any{"record_id": record.BlockID, "commit_revision": staged.CommitRevision, "timeout_seconds": 0}})
	if err != nil || barrier.IsError {
		t.Fatalf("MCP projection barrier call failed: %+v, %v", barrier, err)
	}
	barrierPayload := barrier.StructuredContent.(map[string]any)
	if barrierPayload["ok"] != true || barrierPayload["projection_revision"] != float64(staged.CommitRevision) {
		t.Fatalf("MCP projection barrier lost the committed revision: %+v", barrierPayload)
	}
	for _, arguments := range []map[string]any{{"record_id": record.BlockID}, {"evidence_ref": "exact-evidence"}, {"evidence_ref": "segment-evidence"}} {
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_exact_record", Arguments: arguments})
		if err != nil || result.IsError {
			t.Fatalf("exact MCP read failed: %+v, %v", result, err)
		}
		payload := result.StructuredContent.(map[string]any)
		stored := payload["record"].(map[string]any)
		if payload["ok"] != true || stored["block_id"] != record.BlockID || stored["content"] != record.Content {
			t.Fatalf("exact MCP record differs: %+v", payload)
		}
	}
	batch, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_exact_records", Arguments: map[string]any{"record_ids": []string{record.BlockID}}})
	if err != nil || batch.IsError {
		t.Fatalf("batch MCP read failed: %+v, %v", batch, err)
	}
	batchPayload := batch.StructuredContent.(map[string]any)
	if len(batchPayload["records"].([]any)) != 1 {
		t.Fatalf("batch MCP response lost the exact record: %+v", batchPayload)
	}
	invalid, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_exact_record", Arguments: map[string]any{"record_id": record.BlockID, "evidence_ref": "exact-evidence"}})
	if err != nil || invalid.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("ambiguous selector was accepted: %+v, %v", invalid, err)
	}
	second := record
	second.BlockID, second.RevisionID = "mem_second", "rev_second"
	second.Content = "A different fact with the same evidence reference"
	second.DeliveryPolicy = "reflection_only"
	membership := *record.BlockMembership
	membership.RecordID, membership.RecordRevisionID = second.BlockID, second.RevisionID
	membership.MembershipRevisionID = membership.RevisionHash()
	second.BlockMembership = &membership
	writer, err = store.OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	staged, err = writer.PrepareRootWrite(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, verifiedProjector{}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	reflection, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "build_context", Arguments: map[string]any{
		"query": "different fact", "record_ids": []string{second.BlockID}, "render_context": true,
	}})
	if err != nil || reflection.IsError {
		t.Fatalf("MCP reflection-only context call failed: %+v, %v", reflection, err)
	}
	if payload := reflection.StructuredContent.(map[string]any); payload["ok"] != true ||
		len(payload["items"].([]any)) != 0 {
		t.Fatalf("reflection-only record entered overlay: %+v", payload)
	}
	ambiguous, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_exact_record", Arguments: map[string]any{"evidence_ref": "exact-evidence"}})
	if err != nil || ambiguous.StructuredContent.(map[string]any)["error"] != "ambiguous_evidence_ref" {
		t.Fatalf("shared evidence reference did not fail closed: %+v, %v", ambiguous, err)
	}
	other, err := engine.Open(engine.Config{RepoRoot: config.RepoRoot, WorkspaceID: "alpha", ProfileID: "other", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if leaked, err := other.ReadEvidence(ctx, "exact-evidence"); err == nil || leaked != nil {
		t.Fatalf("exact evidence crossed profile boundary: %+v, %v", leaked, err)
	}
}
