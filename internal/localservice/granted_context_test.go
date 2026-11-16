package localservice

import (
	"context"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
)

func TestGrantedContextUsesOnlyExactHostGrantedRecords(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	first, err := selected.RememberCandidate(ctx, engine.CandidateRequest{
		OperationID: "grant-first", Content: "Accepted exact project context.",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1"})
	if err != nil || !first.Saved {
		t.Fatalf("first candidate setup failed: %+v, %v", first, err)
	}
	second, err := selected.RememberCandidate(ctx, engine.CandidateRequest{
		OperationID: "grant-second", Content: "Same block but not pinned by the grant.",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1"})
	if err != nil || !second.Saved {
		t.Fatalf("second candidate setup failed: %+v, %v", second, err)
	}
	dispatch := EngineDispatcher(selected)
	caller := Caller{Identity: Identity{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}, Role: "operator",
		Lineage: Lineage{WorkspaceID: "alpha", SessionID: "session-1"}}
	args := map[string]any{"logical_block_ids": []any{"stable_project_facts"},
		"record_ids":         []any{first.RecordID, "foreign-record"},
		"include_candidates": true, "max_tokens": 256, "render_context": true}
	result := dispatch(ctx, caller, "build_granted_context", args)
	if result["ok"] != true || result["authority"] != "advisory" ||
		result["delivery_policy"] != "start_grant" {
		t.Fatalf("exact grant failed: %+v", result)
	}
	rendered, _ := result["rendered_context"].(string)
	if !strings.Contains(rendered, "Accepted exact project context.") ||
		strings.Contains(rendered, "Same block but not pinned") || len(rendered) > 256*4 {
		t.Fatalf("exact grant leaked or overran context: %q", rendered)
	}
	diagnostics := result["diagnostics"].(map[string]any)
	if diagnostics["native_path"] != "exact_grant_index" ||
		diagnostics["semantic_recall_performed"] != false ||
		diagnostics["assigned_record_count"] != 2 ||
		diagnostics["selected_record_count"] != 1 {
		t.Fatalf("exact grant diagnostics differ: %+v", diagnostics)
	}
	if len(diagnostics["rendered_record_ids"].([]string)) != 1 ||
		diagnostics["rendered_record_ids"].([]string)[0] != first.RecordID {
		t.Fatalf("exact grant rendered wrong record: %+v", diagnostics)
	}
	service, err := New(caller.Identity, map[string]string{"operator": "operator-secret"}, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	leaseRequest := frame()
	leaseRequest["caller_role"] = "operator"
	leaseRequest["session_id"] = "session-1"
	leaseRequest["bootstrap_secret"] = "operator-secret"
	leaseRequest["capabilities"] = service.capabilities["operator"]
	lease := post(t, service, "/lease", leaseRequest)
	if lease["ok"] != true {
		t.Fatalf("operator grant lease failed: %+v", lease)
	}
	call := frame()
	call["lease"], call["caller_role"], call["session_id"] = lease["lease"], "operator", "session-1"
	call["tool"] = "build_granted_context"
	call["arguments"] = args
	viaHTTP := post(t, service, "/call", call)
	if viaHTTP["ok"] != true ||
		!strings.Contains(viaHTTP["rendered_context"].(string), "Accepted exact project context.") {
		t.Fatalf("authenticated exact grant differs: %+v", viaHTTP)
	}
	args["selected_records_only"] = true
	args["record_ids"] = []any{}
	selectedOnly := dispatch(ctx, caller, "build_granted_context", args)
	if selectedOnly["ok"] != true || len(selectedOnly["items"].([]map[string]any)) != 0 ||
		selectedOnly["diagnostics"].(map[string]any)["selected_record_count"] != 0 {
		t.Fatalf("selected-only grant expanded to the whole block: %+v", selectedOnly)
	}
	delete(args, "selected_records_only")
	args["record_ids"] = []any{first.RecordID}
	args["limit"] = 0
	zeroLimit := dispatch(ctx, caller, "build_granted_context", args)
	if zeroLimit["ok"] != true || len(zeroLimit["items"].([]map[string]any)) != 0 {
		t.Fatalf("zero record limit rendered a grant: %+v", zeroLimit)
	}
	delete(args, "limit")
	args["active_mask_id"] = "mask-without-revision"
	if refused := dispatch(ctx, caller, "build_granted_context", args); refused["ok"] != false ||
		!strings.Contains(refused["error"].(string), "exact mask revision") {
		t.Fatalf("active grant accepted an unpinned mask: %+v", refused)
	}
	delete(args, "active_mask_id")
	args["logical_block_ids"] = []any{}
	if refused := dispatch(ctx, caller, "build_granted_context", args); refused["ok"] != false ||
		!strings.Contains(refused["error"].(string), "requires exact logical_block_ids") {
		t.Fatalf("record-only grant was accepted: %+v", refused)
	}
	args["logical_block_ids"] = []any{"stable_project_facts"}
	args["scope_chain"] = []any{"workspace:foreign"}
	if refused := dispatch(ctx, caller, "build_granted_context", args); refused["ok"] != false {
		t.Fatalf("foreign scope grant was accepted: %+v", refused)
	}
}
