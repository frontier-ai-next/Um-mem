package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestActiveMaskMCPResolvesCommittedBoundEvidence(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	opened, err := catalog.Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	blocks, err := opened.ListBlocks(ctx, false)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("catalog root is unavailable: %+v, %v", blocks, err)
	}
	block := memory.Block{BlockID: "focus-facts", BlockType: "semantic_leaf", Temperature: "warm",
		Scope: "workspace", Authority: "advisory", DeliveryPolicy: "on_demand",
		Retention: "durable", Name: "Focus facts", ParentID: blocks[0].Block.BlockID,
		AgentWritable: true, Owner: "test", Producer: "test", Consumers: []string{"memory_agent"},
		RecallPath: "hybrid_recall", LifecyclePolicy: map[string]any{
			"initial_status": "active", "mutable": true, "terminal_statuses": []string{"archived"}},
		IsolationPolicy: map[string]any{"dimension": "workspace", "cross_workspace": false},
		TokenBudget:     128}
	if _, err := opened.CreateBlock(ctx, block, catalog.CreateOptions{
		BackendPartitionID: "focus-facts-partition", Actor: "test", Reason: "mask fixture"}); err != nil {
		t.Fatal(err)
	}
	config := engine.Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"}
	selected, err := engine.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	fixture, err := os.ReadFile(filepath.Join("..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	record.RunID = "session-1"
	if err := selected.BindRootRecord(ctx, &record, "focus-facts"); err != nil {
		t.Fatal(err)
	}
	writer, err := store.OpenWriter(selected.IndexPath(), "alpha", "team", "hindsight")
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
	t.Setenv("UM_MEM_SESSION_ID", "session-1")
	t.Setenv("UM_MEM_TASK_ID", "task-1")
	t.Setenv("UM_MEM_PROJECT_ID", "project-1")
	t.Setenv("UM_MEM_ACTIVE_SCOPE_PROJECT_ONLY", "")
	server := New(selected, "alpha", "hindsight")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "mask-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		response, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || response.IsError {
			t.Fatalf("%s failed: %+v, %v", name, response, err)
		}
		return response.StructuredContent.(map[string]any)
	}
	missing := call("resolve_active_mask", map[string]any{"max_tokens": 512})
	if missing["ok"] != true || missing["lease"].(map[string]any)["trace"].(map[string]any)["resolution_status"] != "not_found" {
		t.Fatalf("missing mask was not reported: %+v", missing)
	}
	blockMap := call("block_map", map[string]any{"depth": 2, "include_meta": true})
	roots := blockMap["roots"].([]any)
	if blockMap["ok"] != true || len(roots) != 2 ||
		roots[0].(map[string]any)["node_kind"] != "workspace" ||
		roots[1].(map[string]any)["block_id"] != "memory_root_index" {
		t.Fatalf("block map lost workspace or protected meta tree: %+v", blockMap)
	}
	children := roots[0].(map[string]any)["children"].([]any)
	if len(children) != 1 || children[0].(map[string]any)["block_id"] != "focus-facts" ||
		children[0].(map[string]any)["record_count"] != float64(1) ||
		children[0].(map[string]any)["writable"] != true {
		t.Fatalf("block map lost committed leaf coverage: %+v", blockMap)
	}
	expanded := call("expand_block", map[string]any{"block_id": "focus-facts"})
	if expanded["ok"] != true || expanded["child_count"] != float64(0) ||
		expanded["root"].(map[string]any)["record_count"] != float64(1) ||
		len(expanded["path_ids"].([]any)) != 2 {
		t.Fatalf("expanded leaf lost path or coverage: %+v", expanded)
	}
	edge := call("add_block_edge", map[string]any{"src_id": blocks[0].Block.BlockID,
		"dst_id": "focus-facts", "edge_type": "related_to", "reason": "catalog relation"})
	if edge["ok"] != true || edge["revision"] != float64(1) {
		t.Fatalf("catalog relation was not added: %+v", edge)
	}
	expanded = call("expand_block", map[string]any{"block_id": "focus-facts"})
	relations := expanded["root"].(map[string]any)["links"].(map[string]any)["related_block_ids"].([]any)
	if len(relations) != 1 || relations[0] != blocks[0].Block.BlockID {
		t.Fatalf("expanded leaf lost its relation: %+v", expanded)
	}
	removed := call("remove_block_edge", map[string]any{"edge_id": edge["edge_id"],
		"expected_revision": 1, "reason": "catalog relation removed"})
	if removed["ok"] != true || removed["removed"].(map[string]any)["edge_id"] != edge["edge_id"] {
		t.Fatalf("catalog relation was not removed: %+v", removed)
	}
	created := call("set_active_mask", map[string]any{"selected_block_ids": []string{"focus-facts"},
		"token_budget": 512, "activation_reason": "current task"})
	if created["ok"] != true {
		t.Fatalf("active mask was not created: %+v", created)
	}
	maskID := created["mask"].(map[string]any)["mask_id"].(string)
	got := call("get_active_mask", map[string]any{})
	if got["ok"] != true || got["mask"].(map[string]any)["mask_id"] != maskID {
		t.Fatalf("current mask was not read: %+v", got)
	}
	resolved := call("resolve_active_mask", map[string]any{"max_tokens": 512})
	lease := resolved["lease"].(map[string]any)
	trace := lease["trace"].(map[string]any)
	if resolved["ok"] != true || trace["resolution_status"] != "resolved" ||
		trace["mask_id"] != maskID || !strings.Contains(lease["rendered_context"].(string), record.Content) ||
		len(lease["items"].([]any)) != 1 || lease["items"].([]any)[0].(map[string]any)["authority"] != "non_directive" {
		t.Fatalf("active mask omitted committed advisory evidence: %+v", resolved)
	}
	grantRevision, grantBudget := 1, 512
	granted, err := GrantedContextPayload(ctx, selected, GrantedContextArgs{
		LogicalBlockIDs: []string{"focus-facts"}, SelectedRecordsOnly: true,
		ActiveMaskID: maskID, ActiveMaskRevision: &grantRevision,
		IncludeCandidates: true, MaxTokens: &grantBudget,
	}, "alpha", "hindsight", []string{"workspace:alpha"}, "session-1", "task-1", true)
	if err != nil || granted["ok"] != true ||
		!strings.Contains(granted["rendered_context"].(string), record.Content) ||
		granted["diagnostics"].(map[string]any)["active_mask_status"] != "resolved" ||
		granted["diagnostics"].(map[string]any)["semantic_recall_performed"] != false {
		t.Fatalf("revision-pinned active grant differs: %+v, %v", granted, err)
	}
	t.Setenv("UM_MEM_ACTIVE_SCOPE_PROJECT_ONLY", "1")
	projectOnly := call("resolve_active_mask", map[string]any{"max_tokens": 512})
	if projectOnly["ok"] != true || len(projectOnly["lease"].(map[string]any)["items"].([]any)) != 0 ||
		projectOnly["lease"].(map[string]any)["rendered_context"] != "" {
		t.Fatalf("project-only host scope admitted workspace record: %+v", projectOnly)
	}
	t.Setenv("UM_MEM_ACTIVE_SCOPE_PROJECT_ONLY", "")
	stale := call("resolve_active_mask", map[string]any{"expected_revision": 2})
	if stale["lease"].(map[string]any)["trace"].(map[string]any)["resolution_status"] != "revision_mismatch" ||
		len(stale["lease"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatalf("stale mask leaked content: %+v", stale)
	}
	zero := call("resolve_active_mask", map[string]any{"max_tokens": 0})
	if zero["lease"].(map[string]any)["rendered_context"] != "" {
		t.Fatalf("zero budget returned context: %+v", zero)
	}
	amended := call("amend_active_mask", map[string]any{"mask_id": maskID,
		"expected_revision": 1, "activation_reason": "smaller focus", "token_budget": 256})
	if amended["ok"] != true || amended["mask"].(map[string]any)["revision"] != float64(2) ||
		amended["mask"].(map[string]any)["token_budget"] != float64(256) {
		t.Fatalf("mask amendment did not advance revision: %+v", amended)
	}
	staleAmend, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "amend_active_mask",
		Arguments: map[string]any{"mask_id": maskID, "expected_revision": 1,
			"activation_reason": "stale", "token_budget": 128}})
	if err != nil || staleAmend.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("stale mask amendment was accepted: %+v, %v", staleAmend, err)
	}
	wrongScope, err := ActiveMaskPayload(ctx, selected, ResolveActiveMaskArgs{MaskID: maskID},
		"alpha", "hindsight", "another-session", "task-1", []string{"workspace:alpha"})
	if err != nil || wrongScope["lease"].(map[string]any)["trace"].(map[string]any)["resolution_status"] != "scope_mismatch" {
		t.Fatalf("foreign session scope was accepted: %+v, %v", wrongScope, err)
	}
	if _, err := selected.TransitionActiveMask(ctx, maskID, 2, "suspend", "foreign request", "other-session", "task-1"); err == nil {
		t.Fatal("foreign session transitioned active mask")
	}
	if _, err := selected.AmendActiveMask(ctx, catalog.MaskAmendment{MaskID: maskID,
		ExpectedRevision: 2, ActivationReason: "foreign request", SessionID: "other-session", TaskID: "task-1"}); err == nil {
		t.Fatal("foreign session amended active mask")
	}
	suspended := call("transition_active_mask", map[string]any{"mask_id": maskID,
		"expected_revision": 2, "transition": "suspend", "reason": "focus paused"})
	if suspended["ok"] != true || suspended["mask"].(map[string]any)["status"] != "suspended" {
		t.Fatalf("mask was not suspended: %+v", suspended)
	}
	paused := call("resolve_active_mask", map[string]any{})
	if paused["lease"].(map[string]any)["trace"].(map[string]any)["resolution_status"] != "suspended" ||
		len(paused["lease"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatalf("suspended mask delivered content: %+v", paused)
	}
	resumed := call("transition_active_mask", map[string]any{"mask_id": maskID,
		"expected_revision": 3, "transition": "resume", "reason": "focus resumed"})
	if resumed["ok"] != true || resumed["mask"].(map[string]any)["status"] != "active" {
		t.Fatalf("mask was not resumed: %+v", resumed)
	}
	deactivated := call("transition_active_mask", map[string]any{"mask_id": maskID,
		"expected_revision": 4, "transition": "deactivate", "reason": "task complete"})
	if deactivated["ok"] != true || deactivated["mask"].(map[string]any)["status"] != "expired" {
		t.Fatalf("mask was not deactivated: %+v", deactivated)
	}
}
