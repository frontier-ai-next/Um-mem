package localservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func panelDecoded(t *testing.T, responseBody []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestPanelDataAPIUsesGoCatalogAndCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	opened, err := catalog.Open(root, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	states, err := opened.ListBlocks(ctx, false)
	opened.Close()
	if err != nil || len(states) != 1 {
		t.Fatalf("workspace root: %+v %v", states, err)
	}
	created, err := selected.CreateBlock(ctx, engine.CreateBlockRequest{Name: "Panel notes",
		Synopsis: "Notes entered in the panel", ParentID: states[0].Block.BlockID,
		Reason: "panel integration test", Scope: "workspace"})
	if err != nil || created["ok"] != true {
		t.Fatalf("writable panel block: %+v %v", created, err)
	}
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	server, err := New(identity, map[string]string{"operator": "operator-secret"}, EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigurePanel(selected); err != nil {
		t.Fatal(err)
	}
	queuePath, err := WorkQueuePath(identity)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := servicequeue.Open(queuePath, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := server.ConfigureWorkQueue(queue); err != nil {
		t.Fatal(err)
	}
	panel := server.panel
	connect := panelRequest(t, panel, server, http.MethodPost, "/panel/connect",
		map[string]any{"protocol_version": 1, "schema_version": 2, "api_version": 2,
			"request_id": "connect", "caller_role": "operator", "bootstrap_secret": "operator-secret"}, nil)
	if connect.Code != http.StatusOK {
		t.Fatalf("panel connect: %d %s", connect.Code, connect.Body.String())
	}
	url := panelDecoded(t, connect.Body.Bytes())["url"].(string)
	token := strings.TrimPrefix(url, "http://127.0.0.1:8181/panel/#")
	auth := panelRequest(t, panel, server, http.MethodPost, "/panel/auth",
		map[string]any{"token": token}, nil)
	if auth.Code != http.StatusOK || len(auth.Result().Cookies()) != 1 {
		t.Fatalf("panel auth: %d %s", auth.Code, auth.Body.String())
	}
	cookie := auth.Result().Cookies()[0]
	emptySnapshot := panelRequest(t, panel, server, http.MethodGet, "/panel/api/snapshot", nil, cookie)
	if emptySnapshot.Code != http.StatusOK {
		t.Fatalf("empty panel snapshot: %d %s", emptySnapshot.Code, emptySnapshot.Body.String())
	}
	if events, ok := panelDecoded(t, emptySnapshot.Body.Bytes())["events"].([]any); !ok || len(events) != 0 {
		t.Fatalf("empty panel events must be []: %s", emptySnapshot.Body.String())
	}
	write := panelRequest(t, panel, server, http.MethodPost, "/panel/api/call",
		map[string]any{"tool": "remember", "arguments": map[string]any{
			"title": "One note", "content": "Exact panel content 🧠",
			"operation_id": "panel-write-1", "requested_block_id": created["block_id"]}}, cookie)
	if write.Code != http.StatusOK {
		t.Fatalf("panel write: %d %s", write.Code, write.Body.String())
	}
	written := panelDecoded(t, write.Body.Bytes())
	if written["ok"] != true || written["memory_id"] == "" {
		t.Fatalf("panel write result: %+v", written)
	}
	id := written["memory_id"].(string)
	snapshot := panelRequest(t, panel, server, http.MethodGet, "/panel/api/snapshot", nil, cookie)
	if snapshot.Code != http.StatusOK {
		t.Fatalf("panel snapshot: %d %s", snapshot.Code, snapshot.Body.String())
	}
	summary := panelDecoded(t, snapshot.Body.Bytes())
	if summary["record_scope"] != "workspace" || summary["workspace"] != "alpha" ||
		len(summary["records"].([]any)) != 1 {
		t.Fatalf("panel snapshot shape: %+v", summary)
	}
	roots := summary["tree"].([]any)
	if len(roots) != 2 || roots[0].(map[string]any)["node_kind"] != "workspace" ||
		roots[1].(map[string]any)["block_id"] != "memory_root_index" ||
		roots[1].(map[string]any)["meta"] != true {
		t.Fatalf("panel tree must merge workspace and packaged roots: %+v", roots)
	}
	packaged := map[string]map[string]any{}
	var walk func([]any)
	walk = func(nodes []any) {
		for _, raw := range nodes {
			node := raw.(map[string]any)
			packaged[node["block_id"].(string)] = node
			if children, ok := node["children"].([]any); ok {
				walk(children)
			}
		}
	}
	walk(roots)
	if fact := packaged["project_scope_stable_project_facts"]; fact == nil ||
		fact["meta"] != true || fact["scope"] != "project" {
		t.Fatalf("panel tree lost the packaged project block: %+v", packaged)
	}
	if _, ok := summary["events"].([]any); !ok {
		t.Fatalf("panel events must be an array: %#v", summary["events"])
	}
	page := panelRequest(t, panel, server, http.MethodGet,
		"/panel/api/records?block="+created["block_id"].(string), nil, cookie)
	if page.Code != http.StatusOK || len(panelDecoded(t, page.Body.Bytes())["records"].([]any)) != 1 {
		t.Fatalf("panel record page: %d %s", page.Code, page.Body.String())
	}
	detail := panelRequest(t, panel, server, http.MethodGet, "/panel/api/record?id="+id, nil, cookie)
	if detail.Code != http.StatusOK {
		t.Fatalf("panel detail: %d %s", detail.Code, detail.Body.String())
	}
	data := panelDecoded(t, detail.Body.Bytes())
	if data["record"].(map[string]any)["content"] != "Exact panel content 🧠" ||
		data["is_current"] != true || len(data["history"].([]any)) != 1 {
		t.Fatalf("panel detail shape: %+v", data)
	}
	stale := panelRequest(t, panel, server, http.MethodPost, "/panel/api/call",
		map[string]any{"tool": "amend", "arguments": map[string]any{
			"memory_id": id, "expected_revision": "stale", "reason": "correct typo",
			"title": "Updated note", "content": "Revised panel content",
			"operation_id": "panel-amend-stale"}}, cookie)
	if stale.Code != http.StatusConflict ||
		panelDecoded(t, stale.Body.Bytes())["reason_code"] != "canonical_revision_conflict" {
		t.Fatalf("stale panel edit did not surface CAS conflict: %d %s", stale.Code, stale.Body.String())
	}
	valid := panelRequest(t, panel, server, http.MethodPost, "/panel/api/call",
		map[string]any{"tool": "amend", "arguments": map[string]any{
			"memory_id": id, "expected_revision": data["record"].(map[string]any)["revision_id"],
			"reason": "correct typo", "title": "Updated note", "content": "Revised panel content",
			"operation_id": "panel-amend-valid"}}, cookie)
	if valid.Code != http.StatusOK || panelDecoded(t, valid.Body.Bytes())["ok"] != true {
		t.Fatalf("panel edit failed: %d %s", valid.Code, valid.Body.String())
	}
	blockEdit := map[string]any{"tool": "amend_block", "arguments": map[string]any{
		"block_id": created["block_id"], "expected_revision": created["revision"],
		"name": "Panel notes revised", "synopsis": "Revised in the Atlas panel",
		"reason": "operator updated the topic"}}
	updatedBlock := panelRequest(t, panel, server, http.MethodPost, "/panel/api/call", blockEdit, cookie)
	if updatedBlock.Code != http.StatusOK || panelDecoded(t, updatedBlock.Body.Bytes())["ok"] != true {
		t.Fatalf("panel block edit failed: %d %s", updatedBlock.Code, updatedBlock.Body.String())
	}
	staleBlock := panelRequest(t, panel, server, http.MethodPost, "/panel/api/call", blockEdit, cookie)
	if staleBlock.Code != http.StatusConflict ||
		panelDecoded(t, staleBlock.Body.Bytes())["reason_code"] != "canonical_revision_conflict" {
		t.Fatalf("stale panel block edit did not surface CAS conflict: %d %s", staleBlock.Code, staleBlock.Body.String())
	}
	invalid := panelRequest(t, panel, server, http.MethodGet,
		"/panel/api/snapshot?scope=project", nil, cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("panel accepted missing exact project scope: %d", invalid.Code)
	}
	server.listenerPort = 8181
	routed := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8181/panel/api/snapshot", nil)
	routed.RemoteAddr = "127.0.0.1:12345"
	routed.AddCookie(cookie)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, routed)
	if response.Code != http.StatusOK || panelDecoded(t, response.Body.Bytes())["ok"] != true {
		t.Fatalf("listener did not route authenticated panel API: %d %s", response.Code, response.Body.String())
	}
}
