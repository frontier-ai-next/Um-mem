package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPGeneratedSessionKeepsWriteAndReadTogether(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "")
	t.Setenv("UM_MEM_PROJECT_ID", "")
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server := New(selected, "alpha", "mnemosyne")
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
	written, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "Session-bound evidence", "requested_block_id": "stable_project_facts",
		"operation_id": "generated-session-write",
	}})
	if err != nil || written.IsError {
		t.Fatalf("MCP write failed: %+v, %v", written, err)
	}
	id, _ := written.StructuredContent.(map[string]any)["memory_id"].(string)
	record, err := selected.ReadStored(ctx, id)
	if err != nil || record == nil || record.RunID == "" || record.SessionID != record.RunID {
		t.Fatalf("MCP generated lineage was not bound to the write: %+v, %v", record, err)
	}
	for _, tool := range []string{"recall", "build_context"} {
		result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: map[string]any{
			"query": "Session-bound evidence", "record_ids": []string{id}, "max_tokens": 512,
		}})
		if err != nil || result.IsError {
			t.Fatalf("MCP %s failed: %+v, %v", tool, result, err)
		}
		payload := result.StructuredContent.(map[string]any)
		items := payload["hits"]
		if tool == "build_context" {
			items = payload["items"]
		}
		if payload["run_id"] != record.RunID || len(items.([]any)) != 1 {
			t.Fatalf("MCP %s lost the generated session: %+v", tool, payload)
		}
	}
}

func TestRememberMCPBindsDurableOperation(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "trusted-session")
	t.Setenv("UM_MEM_PROJECT_ID", "trusted-project")
	t.Setenv("UM_MEM_TASK_ID", "")
	t.Setenv("UM_MEM_SUBTASK_ID", "")
	t.Setenv("UM_MEM_USER_ID", "")
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server := New(selected, "alpha", "mnemosyne")
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
	foundSchema := false
	for _, tool := range listed.Tools {
		if tool.Name != "remember" {
			continue
		}
		schema := tool.InputSchema.(map[string]any)
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("remember schema has no properties: %+v", schema)
		}
		modeSchema, modeOK := properties["write_mode"].(map[string]any)
		operationSchema, operationOK := properties["operation"].(map[string]any)
		if !modeOK || !operationOK {
			t.Fatalf("remember schema lacks operation or write mode: %+v", schema)
		}
		modes, modesOK := modeSchema["enum"].([]any)
		operations, operationsOK := operationSchema["enum"].([]any)
		if !modesOK || !operationsOK {
			t.Fatalf("remember schema lacks supported enum values: %+v", schema)
		}
		if len(modes) != 2 || modes[1] != "lossless_atomic" ||
			len(operations) != 3 || operations[1] != "correct" || operations[2] != "repair" {
			t.Fatalf("remember schema omitted supported modes: %+v", schema)
		}
		foundSchema = true
	}
	if !foundSchema {
		t.Fatal("remember tool schema missing")
	}
	forged, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "Forged project", "requested_block_id": "stable_project_facts",
		"project_id": "foreign-project", "operation_id": "forged-operation",
	}})
	if err != nil || forged.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("MCP accepted foreign project lineage: %+v, %v", forged, err)
	}
	forged, err = clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "Forged parent", "requested_block_id": "stable_project_facts",
		"parent_session_id": "foreign-parent", "operation_id": "forged-parent-operation",
	}})
	if err != nil || forged.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("MCP accepted foreign parent lineage: %+v, %v", forged, err)
	}
	args := map[string]any{"content": "A durable candidate", "requested_block_id": "stable_project_facts",
		"operation_id": "mcp-candidate-1"}
	first, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: args})
	if err != nil || first.IsError {
		t.Fatalf("candidate tool failed: %+v, %v", first, err)
	}
	created := first.StructuredContent.(map[string]any)
	id, ok := created["memory_id"].(string)
	if created["ok"] != true || !ok || id == "" || created["replayed"] != false {
		t.Fatalf("candidate tool returned invalid acknowledgment: %+v", created)
	}
	record, err := selected.ReadStored(ctx, id)
	if err != nil || record == nil || record.ProjectID != "trusted-project" ||
		record.SessionID != "trusted-session" || record.RunID != "trusted-session" ||
		record.Metadata["evidence_kind"] != "agent_tool_claim" {
		t.Fatalf("MCP write lost trusted host lineage: %+v, %v", record, err)
	}
	replayed, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: args})
	if err != nil || replayed.IsError {
		t.Fatalf("candidate replay failed: %+v, %v", replayed, err)
	}
	second := replayed.StructuredContent.(map[string]any)
	if second["ok"] != true || second["memory_id"] != id || second["replayed"] != true {
		t.Fatalf("candidate replay changed identity: %+v", second)
	}
	status, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "operation_status",
		Arguments: map[string]any{"operation_id": "mcp-candidate-1"}})
	if err != nil || status.IsError {
		t.Fatalf("operation status failed: %+v, %v", status, err)
	}
	observed := status.StructuredContent.(map[string]any)
	if observed["ok"] != true || observed["record_id"] != id || observed["durable_status"] != "committed" {
		t.Fatalf("MCP receipt was not visible to the same identity: %+v", observed)
	}
	forgotten, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "forget", Arguments: map[string]any{
		"memory_id": id, "expected_revision": record.RevisionID, "reason": "withdraw fact"}})
	if err != nil || forgotten.IsError || forgotten.StructuredContent.(map[string]any)["saved"] != true {
		t.Fatalf("MCP forget did not commit a retraction: %+v, %v", forgotten, err)
	}
	repeatedForget, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "forget", Arguments: map[string]any{
		"memory_id": id, "expected_revision": record.RevisionID, "reason": "withdraw fact"}})
	if err != nil || repeatedForget.IsError || repeatedForget.StructuredContent.(map[string]any)["already_retracted"] != true {
		t.Fatalf("MCP forget was not idempotent: %+v, %v", repeatedForget, err)
	}
	numeric, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "Integer metadata", "requested_block_id": "stable_project_facts",
		"operation_id": "mcp-candidate-2", "metadata": map[string]any{"rank": 7},
	}})
	if err != nil || numeric.IsError || numeric.StructuredContent.(map[string]any)["ok"] != true {
		t.Fatalf("MCP numeric candidate failed: %+v, %v", numeric, err)
	}
	numericID := numeric.StructuredContent.(map[string]any)["memory_id"].(string)
	numericRecord, err := selected.ReadStored(ctx, numericID)
	if err != nil || numericRecord == nil || numericRecord.Metadata["rank"] != json.Number("7") {
		t.Fatalf("MCP changed integer metadata semantics: %+v, %v", numericRecord, err)
	}
	amendArgs := map[string]any{
		"memory_id": numericID, "expected_revision": numericRecord.RevisionID,
		"reason": "correct numeric fact", "content": "A corrected numeric candidate",
		"operation_id": "mcp-amend-1"}
	corrected, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "amend", Arguments: amendArgs})
	if err != nil || corrected.IsError || corrected.StructuredContent.(map[string]any)["saved"] != true ||
		corrected.StructuredContent.(map[string]any)["old_id"] != numericID {
		t.Fatalf("MCP amend did not commit a correction: %+v, %v", corrected, err)
	}
	correctedReplay, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "amend", Arguments: amendArgs})
	if err != nil || correctedReplay.IsError ||
		correctedReplay.StructuredContent.(map[string]any)["replayed"] != true ||
		correctedReplay.StructuredContent.(map[string]any)["new_id"] != corrected.StructuredContent.(map[string]any)["new_id"] {
		t.Fatalf("MCP amend receipt did not replay: %+v, %v", correctedReplay, err)
	}
	edgeArgs := map[string]any{"src_id": corrected.StructuredContent.(map[string]any)["new_id"],
		"dst_id": numericID, "edge_type": "derived_from", "reason": "correction lineage", "weight": 0.5,
		"evidence_refs": []map[string]any{{"ref_type": "test_run", "ref_id": "mcp-edge-run",
			"produced_by": "verifier", "producer_id": "go-test"}}}
	edge, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "add_edge", Arguments: edgeArgs})
	if err != nil || edge.IsError || edge.StructuredContent.(map[string]any)["ok"] != true {
		t.Fatalf("MCP add_edge did not persist evidenced link: %+v, %v", edge, err)
	}
	edgeReplay, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "add_edge", Arguments: edgeArgs})
	if err != nil || edgeReplay.IsError || edgeReplay.StructuredContent.(map[string]any)["replayed"] != true {
		t.Fatalf("MCP add_edge did not replay exact link: %+v, %v", edgeReplay, err)
	}
	currentID := corrected.StructuredContent.(map[string]any)["new_id"].(string)
	current, err := selected.ReadStored(ctx, currentID)
	if err != nil || current == nil {
		t.Fatal(err)
	}
	correctArgs := map[string]any{"content": "A further correction",
		"requested_block_id": "stable_project_facts", "operation": "correct",
		"memory_id": currentID, "expected_revision": current.RevisionID,
		"reason": "updated evidence", "operation_id": "mcp-correct-1"}
	correctedAgain, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: correctArgs})
	if err != nil || correctedAgain.IsError {
		t.Fatalf("remember correction failed: %+v, %v", correctedAgain, err)
	}
	correction := correctedAgain.StructuredContent.(map[string]any)
	if correction["saved"] != true || correction["pending_host_approval"] != false ||
		correction["memory_id"] == currentID || correction["operation"] != "correct" {
		t.Fatalf("remember correction changed contract: %+v", correction)
	}
	correctionReplay, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: correctArgs})
	if err != nil || correctionReplay.IsError ||
		correctionReplay.StructuredContent.(map[string]any)["replayed"] != true ||
		correctionReplay.StructuredContent.(map[string]any)["memory_id"] != correction["memory_id"] {
		t.Fatalf("remember correction replay changed revision: %+v, %v", correctionReplay, err)
	}
}

func TestRememberMCPBoundLosslessSource(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "trusted-session")
	t.Setenv("UM_MEM_PROJECT_ID", "trusted-project")
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "mcp-turn", "A🧠B", "trusted-host", "Ada", "",
		engine.HostLineage{SessionID: "trusted-session", ProjectID: "trusted-project"})
	if err != nil {
		t.Fatal(err)
	}
	server := New(selected, "alpha", "mnemosyne")
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
	args := map[string]any{"content": "A🧠", "requested_block_id": "stable_project_facts",
		"write_mode": "lossless_atomic", "operation_id": "mcp-bound-source",
		"source_segments": []any{map[string]any{
			"source_id": "mcp-turn", "source_sha256": source["source_sha256"],
			"byte_start": 0, "byte_end": 5, "text": "A🧠"}}}
	written, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: args})
	if err != nil || written.IsError {
		t.Fatalf("MCP bound source call failed: %+v, %v", written, err)
	}
	response := written.StructuredContent.(map[string]any)
	if response["ok"] != true || response["status"] != "accepted" ||
		response["verification_status"] != "evidence_backed" ||
		response["source_coverage_status"] != "registered" {
		t.Fatalf("MCP bound source response lost verification: %+v", response)
	}
	replayed, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: args})
	if err != nil || replayed.IsError || replayed.StructuredContent.(map[string]any)["replayed"] != true {
		t.Fatalf("MCP bound source receipt did not replay: %+v, %v", replayed, err)
	}
}

func TestRememberMCPRepairBindsRawWitness(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "trusted-session")
	t.Setenv("UM_MEM_PROJECT_ID", "trusted-project")
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "repair-turn", "Exact host output", "trusted-host", "", "",
		engine.HostLineage{SessionID: "trusted-session", ProjectID: "trusted-project"})
	if err != nil {
		t.Fatal(err)
	}
	server := New(selected, "alpha", "mnemosyne")
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
	ref := map[string]any{"ref_type": "artifact", "ref_id": source["witness_id"],
		"hash":        "sha256:" + source["source_sha256"].(string),
		"produced_by": "watcher", "producer_id": "trusted-host"}
	args := map[string]any{"content": "Repair from observed host output",
		"operation": "repair", "repair_of": source["witness_id"],
		"requested_block_id": "stable_project_facts", "operation_id": "repair-1",
		"evidence_refs": []any{ref}}
	written, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: args})
	if err != nil || written.IsError {
		encoded, _ := json.Marshal(written)
		t.Fatalf("repair tool call failed: %s, %v", encoded, err)
	}
	response := written.StructuredContent.(map[string]any)
	if response["ok"] != true || response["operation"] != "repair" ||
		response["repair_of"] != source["witness_id"] || response["status"] != "candidate" {
		t.Fatalf("repair response lost typed source: %+v", response)
	}
	record, err := selected.ReadStored(ctx, response["memory_id"].(string))
	if err != nil || record == nil || record.Metadata["repair_of"] != source["witness_id"] ||
		len(response["provenance_edges"].([]any)) != 1 {
		t.Fatalf("repair record lost raw witness evidence: %+v, %+v, %v", record, response, err)
	}
	invalid := map[string]any{"content": "Unsupported repair", "operation": "repair",
		"repair_of": "missing", "requested_block_id": "stable_project_facts",
		"evidence_refs": []any{ref}}
	rejected, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: invalid})
	if err != nil || rejected.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("repair accepted unmatched evidence: %+v, %v", rejected, err)
	}
}

func TestOpenCodeProjectOnlyRememberRoutesSupportedTwins(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "opencode:session-1")
	t.Setenv("UM_MEM_PROJECT_ID", "project-1")
	t.Setenv("UM_MEM_WRITE_SCOPE_PROJECT_ONLY", "1")
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server := New(selected, "alpha", "mnemosyne")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "opencode-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "A project-only fact", "requested_block_id": "stable_project_facts",
		"operation_id": "opencode-fact-1"}})
	if err != nil || result.IsError {
		t.Fatalf("OpenCode project write failed: %+v, %v", result, err)
	}
	payload := result.StructuredContent.(map[string]any)
	id, ok := payload["memory_id"].(string)
	if payload["ok"] != true || !ok || payload["requested_block_id"] != "project_scope_stable_project_facts" ||
		payload["actual_block_id"] != "project_scope_stable_project_facts" {
		t.Fatalf("OpenCode write was not routed to project twin: %+v", payload)
	}
	record, err := selected.ReadStored(ctx, id)
	if err != nil || record == nil || record.Scope != "project" || record.ProjectID != "project-1" ||
		record.BlockMembership.PrimaryBlockID != "project_scope_stable_project_facts" {
		t.Fatalf("OpenCode write crossed project scope: %+v, %v", record, err)
	}
	mapResponse, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "block_map",
		Arguments: map[string]any{}})
	if err != nil || mapResponse.IsError {
		t.Fatalf("workspace root lookup failed: %+v, %v", mapResponse, err)
	}
	rootID := mapResponse.StructuredContent.(map[string]any)["roots"].([]any)[0].(map[string]any)["block_id"].(string)
	createdBlock, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "create_block", Arguments: map[string]any{
		"name": "Project notes", "synopsis": "Project-specific findings", "parent_id": rootID,
		"reason": "local project memory", "scope": "project"}})
	if err != nil || createdBlock.IsError || createdBlock.StructuredContent.(map[string]any)["ok"] != true {
		t.Fatalf("project leaf creation failed: %+v, %v", createdBlock, err)
	}
	dynamicID := createdBlock.StructuredContent.(map[string]any)["block_id"].(string)
	amendedBlock, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "amend_block", Arguments: map[string]any{
		"block_id": dynamicID, "expected_revision": 1, "reason": "clarify section",
		"name": "Reviewed project notes", "metadata_patch": map[string]any{"reviewed": true}}})
	if err != nil || amendedBlock.IsError || amendedBlock.StructuredContent.(map[string]any)["revision"] != float64(2) {
		t.Fatalf("project leaf amendment failed: %+v, %v", amendedBlock, err)
	}
	dynamic, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "Dynamic project finding", "requested_block_id": dynamicID,
		"operation_id": "opencode-dynamic"}})
	if err != nil || dynamic.IsError || dynamic.StructuredContent.(map[string]any)["ok"] != true ||
		dynamic.StructuredContent.(map[string]any)["actual_block_id"] != dynamicID {
		t.Fatalf("OpenCode could not write a newly created project leaf: %+v, %v", dynamic, err)
	}
	archivedBlock, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "archive_block", Arguments: map[string]any{
		"block_id": dynamicID, "expected_revision": 2, "reason": "project notes retired"}})
	if err != nil || archivedBlock.IsError || archivedBlock.StructuredContent.(map[string]any)["archived"] != true {
		t.Fatalf("project leaf archive failed: %+v, %v", archivedBlock, err)
	}
	revivedBlock, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "revive_block", Arguments: map[string]any{
		"block_id": dynamicID, "expected_revision": 3, "reason": "project notes restored"}})
	if err != nil || revivedBlock.IsError || revivedBlock.StructuredContent.(map[string]any)["status"] != "active" {
		t.Fatalf("project leaf revival failed: %+v, %v", revivedBlock, err)
	}
	newParent, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "create_block", Arguments: map[string]any{
		"name": "Organized notes", "synopsis": "New parent for project notes",
		"parent_id": rootID, "reason": "reorganize", "index_only": true}})
	if err != nil || newParent.IsError || newParent.StructuredContent.(map[string]any)["ok"] != true {
		t.Fatalf("new project branch creation failed: %+v, %v", newParent, err)
	}
	newParentID := newParent.StructuredContent.(map[string]any)["block_id"].(string)
	movedBlock, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "move_block", Arguments: map[string]any{
		"block_id": dynamicID, "parent_id": newParentID, "expected_revision": 4,
		"reason": "organize project notes"}})
	if err != nil || movedBlock.IsError || movedBlock.StructuredContent.(map[string]any)["parent_id"] != newParentID {
		t.Fatalf("project leaf move failed: %+v, %v", movedBlock, err)
	}
	disposable, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "create_block", Arguments: map[string]any{
		"name": "Disposable notes", "synopsis": "Empty section", "parent_id": rootID,
		"reason": "deletion test"}})
	if err != nil || disposable.IsError || disposable.StructuredContent.(map[string]any)["ok"] != true {
		t.Fatalf("disposable leaf creation failed: %+v, %v", disposable, err)
	}
	disposableID := disposable.StructuredContent.(map[string]any)["block_id"].(string)
	if response, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "archive_block", Arguments: map[string]any{
		"block_id": disposableID, "expected_revision": 1, "reason": "retire empty leaf"}}); err != nil || response.IsError || response.StructuredContent.(map[string]any)["archived"] != true {
		t.Fatalf("disposable leaf archive failed: %+v, %v", response, err)
	}
	deleted, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "delete_block", Arguments: map[string]any{
		"block_id": disposableID, "expected_revision": 2, "reason": "remove empty leaf"}})
	if err != nil || deleted.IsError || deleted.StructuredContent.(map[string]any)["deleted"] != true {
		t.Fatalf("empty project leaf deletion failed: %+v, %v", deleted, err)
	}
	unsupported, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "remember", Arguments: map[string]any{
		"content": "Unsupported workspace route", "requested_block_id": "workspace_core_directives",
		"operation_id": "opencode-unsupported"}})
	if err != nil || unsupported.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("OpenCode accepted unsupported workspace route: %+v, %v", unsupported, err)
	}
}
