package localservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type verifiedProjector struct{}

func (verifiedProjector) BackendID() string                       { return "hindsight" }
func (verifiedProjector) RequiresNative(memory.MemoryRecord) bool { return true }
func (verifiedProjector) Project(_ context.Context, record memory.MemoryRecord, _ string, _ int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{NativeID: "UM-MEM:block:" + record.BlockID, Verified: true}, nil
}

func TestWorkingCheckpointDispatcherBindsSupervisorAndTask(t *testing.T) {
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	dispatch := EngineDispatcher(selected)
	caller := Caller{Identity: Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"},
		Role: "operator", Lineage: Lineage{WorkspaceID: "alpha", SessionID: "session-1", TaskID: "task-1"}}
	args := map[string]any{"checkpoint": map[string]any{"workspace_id": "alpha", "profile_id": "team",
		"session_id": "session-1", "task_id": "task-1"}}
	if result := dispatch(ctx, caller, "commit_working_checkpoint", args); result["ok"] != false ||
		!strings.Contains(result["error"].(string), "supervisor") {
		t.Fatalf("operator entered checkpoint mutation route: %+v", result)
	}
	caller.Role = "supervisor"
	args["checkpoint"].(map[string]any)["task_id"] = "other-task"
	if result := dispatch(ctx, caller, "commit_working_checkpoint", args); result["ok"] != false ||
		!strings.Contains(result["error"].(string), "host-bound scope") {
		t.Fatalf("checkpoint crossed task lineage: %+v", result)
	}
}

func TestSupervisorCanRebuildIndexesThroughGoDispatcher(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	caller := Caller{Identity: Identity{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}, Role: "supervisor",
		Lineage: Lineage{WorkspaceID: "alpha", SessionID: "session-1"}}
	dispatch := EngineDispatcher(selected)
	rebuilt := dispatch(ctx, caller, "rebuild_indexes", map[string]any{})
	if rebuilt["ok"] != true || rebuilt["workspace_id"] != "alpha" ||
		rebuilt["record_count"] != 0 || rebuilt["index_sha256"] == "" {
		t.Fatalf("Go dispatcher did not rebuild selected index: %+v", rebuilt)
	}
	if rejected := dispatch(ctx, caller, "rebuild_indexes", map[string]any{
		"unexpected": true}); rejected["ok"] != false {
		t.Fatalf("rebuild_indexes accepted unknown argument: %+v", rejected)
	}
}

func TestDispatcherManagerRecallUsesLeaseUser(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	manager, err := engine.Open(engine.Config{RepoRoot: repo,
		WorkspaceID: engine.ManagerWorkspaceID, ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.RememberCandidate(ctx, engine.CandidateRequest{
		Content: "User prefers concise summaries", RequestedBlockID: "user_global_preferences",
		UserID: "user-a", SessionID: "session-1", RunID: "session-1"})
	if err != nil || !created.Saved {
		manager.Close()
		t.Fatalf("manager preference setup failed: %+v, %v", created, err)
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	dispatch := EngineDispatcher(selected)
	caller := Caller{Identity: Identity{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}, Role: "supervisor",
		Lineage: Lineage{WorkspaceID: "alpha", UserID: "user-b", SessionID: "session-1"}}
	args := map[string]any{"query": "concise summaries", "search_all": true,
		"include_manager": true}
	foreign := dispatch(ctx, caller, "recall", args)
	if foreign["ok"] != true || len(foreign["hits"].([]map[string]any)) != 0 {
		t.Fatalf("manager recall crossed lease user: %+v", foreign)
	}
	caller.Lineage.UserID = "user-a"
	allowed := dispatch(ctx, caller, "recall", args)
	if allowed["ok"] != true || len(allowed["hits"].([]map[string]any)) != 1 {
		t.Fatalf("manager recall missed bound user: %+v", allowed)
	}
	args["scope_chain"] = []any{"workspace:alpha"}
	excluded := dispatch(ctx, caller, "recall", args)
	if excluded["ok"] != true || excluded["include_manager"] != false ||
		len(excluded["hits"].([]map[string]any)) != 0 {
		t.Fatalf("explicit workspace scope leaked manager memory: %+v", excluded)
	}
	args["scope_chain"] = []any{"workspace:alpha", "user_global:user-a"}
	explicit := dispatch(ctx, caller, "recall", args)
	if explicit["ok"] != true || len(explicit["hits"].([]map[string]any)) != 1 {
		t.Fatalf("explicit bound manager scope was ignored: %+v", explicit)
	}
	delete(args, "scope_chain")
	caller.Lineage.UserID = ""
	missing := dispatch(ctx, caller, "recall", args)
	if missing["ok"] != false {
		t.Fatalf("manager recall accepted missing lease user: %+v", missing)
	}
}

func TestHostHookObserveKeepsExactHiddenWitness(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server, err := New(Identity{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "mnemosyne"}, map[string]string{"host_hook": "hook-secret"}, EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	lease := frame()
	lease["caller_role"], lease["session_id"] = "host_hook", "session-1"
	lease["bootstrap_secret"] = "hook-secret"
	lease["scope_lineage"] = map[string]any{"workspace_id": "alpha", "session_id": "session-1", "project_id": "project-1"}
	lease["capabilities"] = server.capabilities["host_hook"]
	granted := post(t, server, "/lease", lease)
	token, ok := granted["lease"].(string)
	if granted["ok"] != true || !ok {
		t.Fatalf("host hook could not obtain lease: %+v", granted)
	}
	call := frame()
	call["caller_role"], call["session_id"], call["lease"] = "host_hook", "session-1", token
	call["scope_lineage"] = lease["scope_lineage"]
	call["tool"] = "observe"
	call["arguments"] = map[string]any{"event_id": "turn-1", "event_kind": "conversation_turn",
		"raw_evidence": "  Exact host bytes\n", "observed_at": "2026-10-23T12:00:00Z",
		"producer_id": "host:test", "speaker": "user"}
	result := post(t, server, "/call", call)
	if result["ok"] != true || result["route_outcome"] != "raw_witness_only" ||
		result["source_bytes"] != float64(len([]byte("  Exact host bytes\n"))) {
		t.Fatalf("host observation contract failed: %+v", result)
	}
	record, err := selected.ReadStored(ctx, result["witness_id"].(string))
	if err != nil || record == nil || record.Content != "  Exact host bytes\n" ||
		record.SessionID != "session-1" || record.ProjectID != "project-1" ||
		record.DeliveryPolicy != "hidden" {
		t.Fatalf("host witness lost bytes or bound lineage: %+v, %v", record, err)
	}
	repeated := post(t, server, "/call", call)
	if repeated["ok"] != true || repeated["witness_id"] != result["witness_id"] || repeated["replayed"] != true {
		t.Fatalf("host observation replay changed identity: %+v", repeated)
	}
	changedClock := clone(call["arguments"].(map[string]any))
	changedClock["observed_at"] = "2026-10-23T12:01:00Z"
	call["arguments"] = changedClock
	if retry := post(t, server, "/call", call); retry["ok"] != true ||
		retry["witness_id"] != result["witness_id"] {
		t.Fatalf("host retry clock changed durable operation identity: %+v", retry)
	}
	changedBytes := clone(changedClock)
	changedBytes["raw_evidence"] = "different bytes"
	call["arguments"] = changedBytes
	if conflict := post(t, server, "/call", call); conflict["ok"] != false {
		t.Fatalf("host mutation correlation accepted changed source bytes: %+v", conflict)
	}
	call["arguments"] = map[string]any{"operation_id": "request-1"}
	call["tool"] = "operation_status"
	status := post(t, server, "/call", call)
	if status["ok"] != true || status["record_id"] != result["witness_id"] ||
		status["projection_status"] != "committed" {
		t.Fatalf("host observation receipt lost native projection: %+v", status)
	}
}

func TestHostWitnessBecomesVerifiedLosslessRecordOverHTTP(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "mnemosyne"}, map[string]string{"host_hook": "hook-secret", "operator": "operator-secret"},
		EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	lineage := map[string]any{"workspace_id": "alpha", "session_id": "session-1", "project_id": "project-1"}
	newLease := func(role, secret string) string {
		lease := frame()
		lease["caller_role"], lease["session_id"] = role, "session-1"
		lease["bootstrap_secret"], lease["capabilities"] = secret, server.capabilities[role]
		lease["scope_lineage"] = lineage
		issued := post(t, server, "/lease", lease)
		token, ok := issued["lease"].(string)
		if issued["ok"] != true || !ok {
			t.Fatalf("%s lease failed: %+v", role, issued)
		}
		return token
	}
	hostToken := newLease("host_hook", "hook-secret")
	agentToken := newLease("operator", "operator-secret")
	call := frame()
	call["caller_role"], call["session_id"], call["lease"] = "host_hook", "session-1", hostToken
	call["scope_lineage"], call["tool"] = lineage, "observe"
	call["arguments"] = map[string]any{"event_id": "turn-1", "event_kind": "conversation_turn",
		"raw_evidence": "A🧠B", "observed_at": "2026-10-23T12:00:00Z", "producer_id": "trusted-host"}
	source := post(t, server, "/call", call)
	if source["ok"] != true || source["source_bytes"] != float64(6) {
		t.Fatalf("host source registration failed: %+v", source)
	}
	call["caller_role"], call["lease"], call["tool"] = "operator", agentToken, "remember"
	call["mutation_correlation_id"] = "bound-service-1"
	call["arguments"] = map[string]any{"content": "A🧠", "requested_block_id": "stable_project_facts",
		"write_mode": "lossless_atomic", "operation_id": "bound-service-1",
		"metadata": map[string]any{"source_segments": []any{map[string]any{
			"source_id": "turn-1", "source_sha256": source["source_sha256"],
			"byte_start": 0, "byte_end": 5, "text": "A🧠"}}}}
	written := post(t, server, "/call", call)
	if written["ok"] != true || written["status"] != "accepted" ||
		written["verification_status"] != "evidence_backed" || written["source_coverage_status"] != "registered" {
		t.Fatalf("host witness was not verified through service: %+v", written)
	}
	record, err := selected.ReadStored(ctx, written["memory_id"].(string))
	if err != nil || record == nil || record.ProjectID != "project-1" || record.SessionID != "session-1" {
		t.Fatalf("verified record lost trusted lineage: %+v, %v", record, err)
	}
	replay := post(t, server, "/call", call)
	if replay["ok"] != true || replay["replayed"] != true || replay["memory_id"] != written["memory_id"] {
		t.Fatalf("service lossless receipt did not replay: %+v", replay)
	}
	call["mutation_correlation_id"] = "repair-service-1"
	call["arguments"] = map[string]any{"content": "Repair from host output",
		"operation": "repair", "repair_of": source["witness_id"],
		"requested_block_id": "stable_project_facts", "operation_id": "repair-service-1",
		"evidence_refs": []any{map[string]any{"ref_type": "artifact",
			"ref_id": source["witness_id"], "hash": "sha256:" + source["source_sha256"].(string),
			"produced_by": "watcher", "producer_id": "trusted-host"}}}
	repaired := post(t, server, "/call", call)
	if repaired["ok"] != true || repaired["operation"] != "repair" ||
		repaired["repair_of"] != source["witness_id"] || repaired["status"] != "candidate" {
		t.Fatalf("service repair did not bind source evidence: %+v", repaired)
	}
}

func TestHostTestObservationReturnsVerifierReportOverHTTP(t *testing.T) {
	root := t.TempDir()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "result.txt"), []byte("passed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "mnemosyne"}, map[string]string{"host_hook": "hook-secret"}, EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	lineage := map[string]any{"workspace_id": "alpha", "session_id": "session-1"}
	lease := frame()
	lease["caller_role"], lease["session_id"] = "host_hook", "session-1"
	lease["bootstrap_secret"], lease["scope_lineage"] = "hook-secret", lineage
	lease["capabilities"] = server.capabilities["host_hook"]
	issued := post(t, server, "/lease", lease)
	if issued["ok"] != true {
		t.Fatalf("host lease failed: %+v", issued)
	}
	call := frame()
	call["caller_role"], call["session_id"], call["lease"] = "host_hook", "session-1", issued["lease"]
	call["scope_lineage"], call["tool"] = lineage, "observe"
	call["arguments"] = map[string]any{"event_id": "test-1", "event_kind": "test_passed",
		"raw_evidence": "1 passed", "atomic_claim": "The focused test passed.",
		"observed_at": "2026-10-23T12:00:00Z", "producer_id": "trusted-host",
		"workspace_root": project, "changed_files": []string{"result.txt"}}
	first := post(t, server, "/call", call)
	report, ok := first["verification_report"].(map[string]any)
	if first["ok"] != true || !ok || report["passed"] != true || report["report_id"] == "" ||
		report["workspace_hash"] == "" || report["diff_hash"] == "" {
		t.Fatalf("HTTP test report missing or incomplete: %+v", first)
	}
	replayed := post(t, server, "/call", call)
	replayedReport, ok := replayed["verification_report"].(map[string]any)
	if replayed["ok"] != true || replayed["replayed"] != true || !ok ||
		replayedReport["report_id"] != report["report_id"] {
		t.Fatalf("HTTP test report receipt did not replay: %+v", replayed)
	}
}

func TestExactReadOverAuthenticatedHTTP(t *testing.T) {
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
	record.RunID = "session-1"
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
	server, err := New(Identity{RepoRoot: config.RepoRoot, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "hindsight"}, map[string]string{"operator": "operator-secret"}, EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	leaseRequest := frame()
	leaseRequest["caller_role"], leaseRequest["session_id"] = "operator", "session-1"
	leaseRequest["capabilities"] = server.capabilities["operator"]
	leaseRequest["bootstrap_secret"] = "operator-secret"
	response := post(t, server, "/lease", leaseRequest)
	token, ok := response["lease"].(string)
	if response["ok"] != true || !ok {
		t.Fatalf("read caller could not authenticate: %+v", response)
	}
	request := frame()
	request["lease"], request["caller_role"], request["session_id"] = token, "operator", "session-1"
	request["tool"] = "read_exact_record"
	request["arguments"] = map[string]any{"record_id": record.BlockID}
	response = post(t, server, "/call", request)
	stored, ok := response["record"].(map[string]any)
	if response["ok"] != true || !ok || stored["block_id"] != record.BlockID || stored["content"] != record.Content {
		t.Fatalf("authenticated HTTP read lost canonical payload: %+v", response)
	}
	request["tool"] = "read_exact_records"
	request["arguments"] = map[string]any{"record_ids": []string{record.BlockID}}
	response = post(t, server, "/call", request)
	if response["ok"] != true || len(response["records"].([]any)) != 1 {
		t.Fatalf("authenticated batch read failed: %+v", response)
	}
	request["tool"] = "export_graph"
	request["arguments"] = map[string]any{}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["audit"].(map[string]any)["record_count"] != float64(1) {
		t.Fatalf("authenticated graph export lost canonical record: %+v", response)
	}
	request["tool"] = "projection_barrier"
	request["arguments"] = map[string]any{"record_id": record.BlockID,
		"commit_revision": staged.CommitRevision, "timeout_seconds": 0}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["projection_status"] != "committed" ||
		response["projection_revision"] != float64(staged.CommitRevision) {
		t.Fatalf("authenticated projection barrier lost revision state: %+v", response)
	}
	request["arguments"] = map[string]any{"record_id": record.BlockID,
		"commit_revision": float64(staged.CommitRevision) + 0.5, "timeout_seconds": 0}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "projection_barrier requires a non-negative commit_revision" {
		t.Fatalf("fractional commit revision was accepted: %+v", response)
	}
	request["tool"] = "read_working_set"
	request["arguments"] = map[string]any{"owner_id": "node-a", "generation_id": "generation-1",
		"record_ids": []string{record.BlockID}, "max_tokens": 128, "max_bytes": 512}
	response = post(t, server, "/call", request)
	if response["ok"] != true || len(response["records"].([]any)) != 1 ||
		!strings.Contains(response["rendered_context"].(string), record.Content) {
		t.Fatalf("authenticated working read lost owned candidate: %+v", response)
	}
	request["arguments"] = map[string]any{"owner_id": "node-b", "generation_id": "generation-1",
		"record_ids": []string{record.BlockID}}
	response = post(t, server, "/call", request)
	if response["ok"] != true || len(response["records"].([]any)) != 0 ||
		response["rendered_context"] != "" {
		t.Fatalf("foreign working owner accessed candidate: %+v", response)
	}
	request["arguments"] = map[string]any{"owner_id": "node-a", "generation_id": "generation-1",
		"record_ids": []string{record.BlockID}, "scope_chain": []string{"session:foreign"}}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "scope chain is not bound to caller lineage" {
		t.Fatalf("unbound working scope was accepted: %+v", response)
	}
	request["tool"] = "recall"
	request["arguments"] = map[string]any{"query": "Bound fact", "record_ids": []string{record.BlockID}, "max_tokens": 512}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["run_id"] != "session-1" || len(response["hits"].([]any)) != 1 {
		t.Fatalf("authenticated service recall failed: %+v", response)
	}
	request["arguments"] = map[string]any{"query": "Bound fact", "record_ids": []string{record.BlockID},
		"scope_chain": []string{"session:foreign"}}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "scope chain is not bound to caller lineage" {
		t.Fatalf("unbound recall scope was accepted: %+v", response)
	}
	request["arguments"] = map[string]any{"query": "Bound fact", "record_ids": []string{record.BlockID}, "run_id": "foreign"}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "recall run_id is not bound to caller session" {
		t.Fatalf("unbound recall run ID was accepted: %+v", response)
	}
	request["tool"] = "build_context"
	request["arguments"] = map[string]any{"query": "Bound fact", "record_ids": []string{record.BlockID},
		"max_tokens": 512, "render_context": true}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["authority"] != "advisory" ||
		!strings.Contains(response["rendered_context"].(string), record.Content) ||
		len(response["items"].([]any)) != 1 {
		t.Fatalf("authenticated service build_context lost evidence: %+v", response)
	}
	request["arguments"] = map[string]any{"query": "Bound fact", "record_ids": []string{record.BlockID},
		"scope_chain": []string{"session:foreign"}, "render_context": true}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "scope chain is not bound to caller lineage" {
		t.Fatalf("unbound context scope was accepted: %+v", response)
	}
	request["tool"] = "block_map"
	request["arguments"] = map[string]any{"depth": 1, "include_meta": true}
	response = post(t, server, "/call", request)
	if response["ok"] != true || len(response["roots"].([]any)) != 2 ||
		response["roots"].([]any)[0].(map[string]any)["node_kind"] != "workspace" {
		t.Fatalf("authenticated block map lost workspace tree: %+v", response)
	}
	rootID := response["roots"].([]any)[0].(map[string]any)["block_id"].(string)
	request["tool"] = "create_block"
	request["arguments"] = map[string]any{"name": "Service notes", "synopsis": "Notes from the caller",
		"parent_id": rootID, "reason": "organize notes"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["parent_id"] != rootID || response["backend_partition_id"] == "" {
		t.Fatalf("authenticated service could not create a content block: %+v", response)
	}
	createdID := response["block_id"].(string)
	request["tool"] = "observe_maintenance"
	request["arguments"] = map[string]any{"block_id": createdID,
		"observation_type": "low_usage", "values": map[string]any{"reads": 0},
		"threshold_revision": "threshold-1", "evidence_ids": []string{"service-run"}}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["observation_id"] == "" {
		t.Fatalf("authenticated service did not record maintenance evidence: %+v", response)
	}
	request["tool"] = "navigation_envelope"
	request["arguments"] = map[string]any{"block_id": createdID}
	response = post(t, server, "/call", request)
	if response["ok"] != true || len(response["maintenance_observations"].([]any)) != 1 ||
		response["proposal_options"].([]any)[0] != "propose_merge" {
		t.Fatalf("authenticated service did not expose navigation evidence: %+v", response)
	}
	request["tool"] = "amend_block"
	request["arguments"] = map[string]any{"block_id": createdID, "expected_revision": 1,
		"reason": "reviewed notes", "name": "Reviewed service notes"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["revision"] != float64(2) || response["name"] != "Reviewed service notes" {
		t.Fatalf("authenticated service could not amend its content block: %+v", response)
	}
	request["tool"] = "archive_block"
	request["arguments"] = map[string]any{"block_id": createdID, "expected_revision": 2,
		"reason": "notes retired"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["status"] != "archived" {
		t.Fatalf("authenticated service could not archive its content block: %+v", response)
	}
	request["tool"] = "revive_block"
	request["arguments"] = map[string]any{"block_id": createdID, "expected_revision": 3,
		"reason": "notes restored"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["status"] != "active" {
		t.Fatalf("authenticated service could not revive its content block: %+v", response)
	}
	request["tool"] = "create_block"
	request["arguments"] = map[string]any{"name": "Service index", "synopsis": "Index for service notes",
		"parent_id": rootID, "reason": "reorganize notes", "index_only": true}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["index_only"] != true {
		t.Fatalf("authenticated service could not create an index block: %+v", response)
	}
	newParentID := response["block_id"].(string)
	request["tool"] = "create_block"
	request["arguments"] = map[string]any{"name": "Convertible service leaf",
		"synopsis": "Temporary leaf", "parent_id": rootID, "reason": "test conversion"}
	response = post(t, server, "/call", request)
	if response["ok"] != true {
		t.Fatalf("service conversion setup failed: %+v", response)
	}
	convertID := response["block_id"].(string)
	request["tool"] = "convert_block_to_index"
	request["arguments"] = map[string]any{"block_id": convertID,
		"expected_revision": 1, "reason": "nest service categories"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["index_only"] != true || response["backend_partition_id"] != "" {
		t.Fatalf("authenticated service leaf conversion failed: %+v", response)
	}
	request["tool"] = "move_block"
	request["arguments"] = map[string]any{"block_id": createdID, "parent_id": newParentID,
		"expected_revision": 4, "reason": "reorganize notes"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["parent_id"] != newParentID {
		t.Fatalf("authenticated service could not move its content block: %+v", response)
	}
	request["tool"] = "archive_block"
	request["arguments"] = map[string]any{"block_id": createdID, "expected_revision": 5,
		"reason": "retire moved notes"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["status"] != "archived" {
		t.Fatalf("authenticated service could not archive moved notes: %+v", response)
	}
	request["tool"] = "delete_block"
	request["arguments"] = map[string]any{"block_id": createdID, "expected_revision": 6,
		"reason": "remove empty notes"}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["deleted"] != true {
		t.Fatalf("authenticated service could not delete empty archived notes: %+v", response)
	}
	request["tool"] = "expand_block"
	request["arguments"] = map[string]any{"block_id": "memory_root_index", "limit": 1}
	response = post(t, server, "/call", request)
	if response["ok"] != true || len(response["children"].([]any)) != 1 || response["next_cursor"] == "" {
		t.Fatalf("authenticated service did not page protected taxonomy: %+v", response)
	}
	request["tool"] = "get_active_mask"
	request["arguments"] = map[string]any{}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["mask"] != nil {
		t.Fatalf("authenticated missing active mask was misreported: %+v", response)
	}
	request["tool"] = "resolve_active_mask"
	request["arguments"] = map[string]any{"max_tokens": 512}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["lease"].(map[string]any)["trace"].(map[string]any)["resolution_status"] != "not_found" {
		t.Fatalf("authenticated active mask resolution failed: %+v", response)
	}
	request["arguments"] = map[string]any{"scope_chain": []string{"session:foreign"}}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "scope chain is not bound to caller lineage" {
		t.Fatalf("foreign active mask scope was accepted: %+v", response)
	}
	request["tool"] = "recent"
	request["arguments"] = map[string]any{"limit": 8}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["count"] != float64(1) ||
		response["records"].([]any)[0].(map[string]any)["block_id"] != record.BlockID {
		t.Fatalf("authenticated service recent lost current record: %+v", response)
	}
	request["arguments"] = map[string]any{"scope": "session", "scope_id": "foreign"}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "scope chain is not bound to caller lineage" {
		t.Fatalf("unbound recent scope was accepted: %+v", response)
	}
	request["tool"] = "list_records"
	request["arguments"] = map[string]any{"scope": "workspace", "scope_id": "alpha", "limit": 32}
	response = post(t, server, "/call", request)
	if response["ok"] != true || response["snapshot_count"] != float64(1) ||
		response["records"].([]any)[0].(map[string]any)["record_id"] != record.BlockID {
		t.Fatalf("authenticated service list_records lost canonical ID: %+v", response)
	}
	request["arguments"] = map[string]any{"scope": "workspace", "scope_id": "foreign"}
	response = post(t, server, "/call", request)
	if response["ok"] != false || response["error"] != "scope chain is not bound to caller lineage" {
		t.Fatalf("unbound list_records scope was accepted: %+v", response)
	}
}

func TestRememberOverAuthenticatedHTTPBindsLineageAndReceipt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "mnemosyne"}, map[string]string{"operator": "secret"}, EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	lease := frame()
	lease["caller_role"], lease["session_id"] = "operator", "session-1"
	lease["bootstrap_secret"] = "secret"
	lease["capabilities"] = server.capabilities["operator"]
	lease["scope_lineage"] = map[string]any{"workspace_id": "alpha", "session_id": "session-1",
		"project_id": "project-1", "task_id": "task-1"}
	issued := post(t, server, "/lease", lease)
	token, ok := issued["lease"].(string)
	if issued["ok"] != true || !ok {
		t.Fatalf("operator lease failed: %+v", issued)
	}
	call := frame()
	call["lease"], call["caller_role"], call["session_id"] = token, "operator", "session-1"
	call["tool"], call["mutation_correlation_id"] = "remember", "candidate-1"
	call["arguments"] = map[string]any{"content": "A bounded project fact",
		"requested_block_id": "stable_project_facts", "operation_id": "candidate-1",
		"metadata": map[string]any{"weight": json.Number("1.0")}}
	for name, claim := range map[string]any{"run_id": "foreign-session", "project_id": "foreign-project",
		"task_id": "foreign-task", "parent_session_id": "foreign-parent"} {
		forged := clone(call)
		forged["arguments"] = map[string]any{"content": "A bounded project fact",
			"requested_block_id": "stable_project_facts", "operation_id": "candidate-1", name: claim}
		if response := post(t, server, "/call", forged); response["ok"] != false {
			t.Fatalf("foreign %s was accepted: %+v", name, response)
		}
	}
	invalid := clone(call)
	invalid["mutation_correlation_id"] = "different-operation"
	if response := post(t, server, "/call", invalid); response["ok"] != false {
		t.Fatalf("mismatched mutation correlation was accepted: %+v", response)
	}
	first := post(t, server, "/call", call)
	id, ok := first["memory_id"].(string)
	if first["ok"] != true || !ok || id == "" || first["replayed"] != false ||
		first["projection_status"] != "committed" {
		t.Fatalf("candidate write failed: %+v", first)
	}
	stored, err := selected.ReadStored(ctx, id)
	if err != nil || stored == nil || stored.Content != "A bounded project fact" ||
		stored.RunID != "session-1" || stored.ProjectID != "project-1" || stored.TaskID != "task-1" ||
		stored.Metadata["weight"] != json.Number("1.0") {
		t.Fatalf("candidate did not bind trusted lineage: %+v, %v", stored, err)
	}
	replay := post(t, server, "/call", call)
	if replay["ok"] != true || replay["memory_id"] != id || replay["replayed"] != true {
		t.Fatalf("candidate retry was not idempotent: %+v", replay)
	}
	call["tool"] = "operation_status"
	call["arguments"] = map[string]any{"operation_id": "candidate-1"}
	status := post(t, server, "/call", call)
	if status["ok"] != true || status["record_id"] != id ||
		status["durable_status"] != "committed" {
		t.Fatalf("operator could not observe own durable receipt: %+v", status)
	}
	call["tool"] = "amend"
	call["arguments"] = map[string]any{"memory_id": id, "expected_revision": stored.RevisionID,
		"reason": "correct service fact", "content": "A corrected bounded project fact",
		"operation_id": "service-amend-1"}
	corrected := post(t, server, "/call", call)
	if corrected["ok"] != true || corrected["saved"] != true || corrected["old_id"] != id {
		t.Fatalf("authenticated service did not amend its record: %+v", corrected)
	}
	correctedReplay := post(t, server, "/call", call)
	if correctedReplay["ok"] != true || correctedReplay["replayed"] != true ||
		correctedReplay["new_id"] != corrected["new_id"] {
		t.Fatalf("authenticated amendment receipt did not replay: %+v", correctedReplay)
	}
	call["tool"] = "add_edge"
	call["arguments"] = map[string]any{"src_id": corrected["new_id"], "dst_id": id,
		"edge_type": "derived_from", "reason": "correction lineage",
		"evidence_refs": []map[string]any{{"ref_type": "test_run", "ref_id": "service-edge-run",
			"produced_by": "verifier", "producer_id": "go-test"}}}
	edge := post(t, server, "/call", call)
	if edge["ok"] != true || edge["src_id"] != corrected["new_id"] {
		t.Fatalf("authenticated service did not persist evidenced edge: %+v", edge)
	}
	correctedID := corrected["new_id"].(string)
	correctedRecord, err := selected.ReadStored(ctx, correctedID)
	if err != nil || correctedRecord == nil || correctedRecord.Content != "A corrected bounded project fact" {
		t.Fatalf("service correction is unreadable: %+v, %v", correctedRecord, err)
	}
	call["tool"], call["mutation_correlation_id"] = "remember", "service-correct-1"
	call["arguments"] = map[string]any{"operation": "correct", "memory_id": correctedID,
		"content": "A further corrected project fact", "requested_block_id": "stable_project_facts",
		"expected_revision": correctedRecord.RevisionID, "reason": "updated service evidence",
		"operation_id": "service-correct-1"}
	serviceCorrection := post(t, server, "/call", call)
	if serviceCorrection["ok"] != true || serviceCorrection["saved"] != true ||
		serviceCorrection["pending_host_approval"] != false {
		t.Fatalf("authenticated remember correction failed: %+v", serviceCorrection)
	}
	serviceCorrectionReplay := post(t, server, "/call", call)
	if serviceCorrectionReplay["ok"] != true || serviceCorrectionReplay["memory_id"] != serviceCorrection["memory_id"] {
		t.Fatalf("authenticated remember correction did not replay: %+v", serviceCorrectionReplay)
	}
	correctedID = serviceCorrection["memory_id"].(string)
	correctedRecord, err = selected.ReadStored(ctx, correctedID)
	if err != nil || correctedRecord == nil {
		t.Fatal(err)
	}
	call["tool"] = "forget"
	call["arguments"] = map[string]any{"memory_id": correctedID, "expected_revision": correctedRecord.RevisionID,
		"reason": "withdraw service fact"}
	forgotten := post(t, server, "/call", call)
	if forgotten["ok"] != true || forgotten["saved"] != true || forgotten["workspace_id"] != "alpha" {
		t.Fatalf("authenticated service did not retract its record: %+v", forgotten)
	}
	call["arguments"] = map[string]any{"memory_id": correctedID, "expected_revision": "stale",
		"reason": "different revision"}
	stale := post(t, server, "/call", call)
	if stale["ok"] != false {
		t.Fatalf("authenticated service accepted stale retraction: %+v", stale)
	}
	opened, err := catalog.Open(root, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	blocks, err := opened.ListBlocks(ctx, false)
	if err != nil || len(blocks) != 1 {
		t.Fatalf("catalog root unavailable: %+v, %v", blocks, err)
	}
	leaf := memory.Block{BlockID: "service-focus", BlockType: "semantic_leaf", Temperature: "warm",
		Scope: "workspace", Authority: "advisory", DeliveryPolicy: "on_demand", Retention: "durable",
		Name: "Service focus", ParentID: blocks[0].Block.BlockID, AgentWritable: true,
		Owner: "test", Producer: "test", Consumers: []string{"memory_agent"}, RecallPath: "hybrid_recall",
		LifecyclePolicy: map[string]any{"initial_status": "active", "mutable": true,
			"terminal_statuses": []string{"archived"}},
		IsolationPolicy: map[string]any{"dimension": "workspace", "cross_workspace": false}, TokenBudget: 128}
	if _, err := opened.CreateBlock(ctx, leaf, catalog.CreateOptions{Actor: "test", Reason: "service mask"}); err != nil {
		t.Fatal(err)
	}
	call["tool"] = "set_active_mask"
	call["arguments"] = map[string]any{"selected_block_ids": []string{"service-focus"},
		"token_budget": 256, "activation_reason": "service task"}
	activated := post(t, server, "/call", call)
	mask, ok := activated["mask"].(map[string]any)
	if activated["ok"] != true || !ok || mask["session_id"] != "session-1" || mask["task_id"] != "task-1" {
		t.Fatalf("service mask did not bind caller lineage: %+v", activated)
	}
	maskID := mask["mask_id"].(string)
	call["tool"] = "resolve_active_mask"
	call["arguments"] = map[string]any{"max_tokens": 256}
	resolved := post(t, server, "/call", call)
	if resolved["ok"] != true || resolved["lease"].(map[string]any)["trace"].(map[string]any)["resolution_status"] != "resolved" {
		t.Fatalf("service active mask did not resolve: %+v", resolved)
	}
	call["tool"] = "amend_active_mask"
	call["arguments"] = map[string]any{"mask_id": maskID, "expected_revision": 1,
		"activation_reason": "smaller service focus", "token_budget": 128}
	amended := post(t, server, "/call", call)
	if amended["ok"] != true || amended["mask"].(map[string]any)["revision"] != float64(2) {
		t.Fatalf("service mask was not amended: %+v", amended)
	}
	foreignLease := frame()
	foreignLease["caller_role"], foreignLease["session_id"] = "operator", "session-2"
	foreignLease["bootstrap_secret"] = "secret"
	foreignLease["capabilities"] = server.capabilities["operator"]
	foreignLease["scope_lineage"] = map[string]any{"workspace_id": "alpha", "session_id": "session-2",
		"task_id": "task-2"}
	foreignIssued := post(t, server, "/lease", foreignLease)
	foreign := frame()
	foreign["lease"], foreign["caller_role"], foreign["session_id"] = foreignIssued["lease"], "operator", "session-2"
	foreign["tool"] = "transition_active_mask"
	foreign["arguments"] = map[string]any{"mask_id": maskID, "expected_revision": 2,
		"transition": "deactivate", "reason": "foreign task"}
	if denied := post(t, server, "/call", foreign); denied["ok"] != false {
		t.Fatalf("foreign lease transitioned mask: %+v", denied)
	}
	call["tool"] = "transition_active_mask"
	call["arguments"] = map[string]any{"mask_id": maskID, "expected_revision": 2,
		"transition": "deactivate", "reason": "task completed"}
	deactivated := post(t, server, "/call", call)
	if deactivated["ok"] != true || deactivated["mask"].(map[string]any)["status"] != "expired" {
		t.Fatalf("service mask did not deactivate: %+v", deactivated)
	}
}
