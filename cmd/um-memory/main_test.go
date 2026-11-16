package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/internal/telemetry"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestCommandRebuildsSelectedIndex(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"rebuild-indexes", "--repo-root", t.TempDir(),
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne"},
		&output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil ||
		result["ok"] != true || result["record_count"] != float64(0) ||
		result["workspace_id"] != "alpha" || result["backend"] != "mnemosyne" {
		t.Fatalf("CLI index rebuild returned the wrong scope or count: %+v, %v", result, err)
	}
}

func TestCommandExpiresOnlyExplicitlyDueBlocks(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"expire-due-blocks", "--repo-root", t.TempDir(),
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--as-of", "2099-01-01T00:00:00Z"}, &output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["ok"] != true ||
		result["backend"] != "mnemosyne" || len(result["archived_block_ids"].([]any)) != 0 {
		t.Fatalf("expiry command returned an invalid report: %+v %v", result, err)
	}
}

func TestProfileReportCommandReadsVerifiedGoSpans(t *testing.T) {
	repo := t.TempDir()
	_, span := telemetry.StartProfile(context.Background(), repo, telemetry.ProfileOptions{
		Operation: "backend_recall", Component: "mnemosyne", WorkspaceID: "alpha",
		Backend: "mnemosyne", Input: map[string]any{"query": "secret query"}})
	span.End(nil)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"profile-report", "--repo-root", repo}, &output); err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal(output.Bytes(), &report); err != nil ||
		report["profiled_operation_count"] != float64(1) {
		t.Fatalf("profile-report lost the Go span: %+v %v", report, err)
	}
	if strings.Contains(output.String(), "secret query") {
		t.Fatal("profile-report disclosed query content")
	}
}

func TestDashboardStopIsIdempotentWithoutListener(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"dashboard", "--repo-root", t.TempDir(),
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--stop", "--no-browser"}, &output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["ok"] != true ||
		result["state"] != "stopped" {
		t.Fatalf("dashboard stop without listener: %+v %v", result, err)
	}
}

func TestCommandInstallsGoMCPServerWithoutChangingOtherCodexSettings(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "config.toml")
	if err := os.WriteFile(settings, []byte("# keep\nmodel = 'test'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"install-mcp", "--repo-root", filepath.Join(root, "store"),
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--host", "codex", "--settings-path", settings, "--mcp-executable", executable}, &output); err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status["configured"] != true {
		t.Fatalf("Go MCP install status: %+v %v", status, err)
	}
	contents, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(contents), "# keep\nmodel = 'test'") ||
		strings.Contains(string(contents), "um_mem.memory.mcp_server") {
		t.Fatalf("Codex settings lost comment or retained Python launcher: %s %v", contents, err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"mcp-status", "--host", "codex",
		"--settings-path", settings}, &output); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status["configured"] != true {
		t.Fatalf("Go MCP status command: %+v %v", status, err)
	}
}

func TestMCPInstallDefaultsToUserDataIdentity(t *testing.T) {
	root := t.TempDir()
	settings := filepath.Join(root, "claude.json")
	t.Setenv("UM_MEM_WORKSPACE", "project")
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"install-mcp", "--host", "claude",
		"--settings-path", settings, "--mcp-executable", executable}, &output); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Configured bool              `json:"configured"`
		Identity   map[string]string `json:"identity"`
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || !status.Configured ||
		status.Identity["workspace"] != "project" || status.Identity["profile"] != "default" ||
		status.Identity["backend"] != "mempalace" || !filepath.IsAbs(status.Identity["repo_root"]) {
		t.Fatalf("MCP default identity = %+v, %v", status, err)
	}
}

func TestLegacyObsidianExportCommandWritesVault(t *testing.T) {
	repo := t.TempDir()
	vault := filepath.Join(repo, "vault")
	args := []string{"obsidian-export", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--vault-dir", vault,
		"--scope", "session:session-1", "--scope", "workspace:alpha"}
	var output bytes.Buffer
	if err := run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Workspace      string   `json:"workspace"`
		BlocksExported int      `json:"blocks_exported"`
		IndexPath      string   `json:"index_path"`
		FilesWritten   []string `json:"files_written"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Workspace != "alpha" ||
		result.BlocksExported != 0 || len(result.FilesWritten) == 0 {
		t.Fatalf("legacy Obsidian command contract changed: %+v, %v", result, err)
	}
	if _, err := os.Stat(result.IndexPath); err != nil {
		t.Fatal(err)
	}
}

func TestCommandReadsPendingTopologyJournal(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	path, err := catalog.Path(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"assignments":{},"operation":"split","sources":{},"targets":[]}`
	digest := sha256.Sum256([]byte(payload))
	if _, err := db.ExecContext(ctx, `INSERT INTO topology_evolutions(
		operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,
		abort_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,'',?,?)`,
		"cli-split-1", "alpha", "team", "prepared", payload,
		hex.EncodeToString(digest[:]), "2026-01-01", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"pending-topology-evolutions", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne"},
		&output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK      bool `json:"ok"`
		Pending []struct {
			OperationID string `json:"operation_id"`
			Operation   string `json:"operation"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.OK ||
		len(result.Pending) != 1 || result.Pending[0].OperationID != "cli-split-1" ||
		result.Pending[0].Operation != "split" {
		t.Fatalf("CLI pending topology journal differs: %+v, %v", result, err)
	}
	requestPath := filepath.Join(t.TempDir(), "abort.json")
	if err := os.WriteFile(requestPath, []byte(`{"operation_id":"cli-split-1","reason":"native projection failed"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(ctx, []string{"abort-topology-evolution", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var aborted map[string]any
	if err := json.Unmarshal(output.Bytes(), &aborted); err != nil ||
		aborted["ok"] != true || aborted["state"] != "aborted" ||
		aborted["reason"] != "native projection failed" {
		t.Fatalf("CLI topology abort differs: %+v, %v", aborted, err)
	}
	resumePath := filepath.Join(t.TempDir(), "resume.json")
	if err := os.WriteFile(resumePath, []byte(`{"operation_id":"cli-split-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(ctx, []string{"resume-topology-evolution", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", resumePath}, &output); err == nil ||
		!strings.Contains(err.Error(), "aborted") {
		t.Fatalf("CLI resumed an aborted topology mapping: %v", err)
	}
	for _, route := range []struct{ command, payload, expected string }{
		{"split-block", `{"block_id":"missing-source","expected_revision":1,"targets":[{"name":"Target","synopsis":"Target","record_ids":["missing-record"]}],"reason":"reorganize"}`, "split source"},
		{"merge-blocks", `{"sources":[{"block_id":"source-a","expected_revision":1},{"block_id":"source-b","expected_revision":1}],"target":{"name":"Target","synopsis":"Target","record_ids":["missing-record"]},"parent_id":"missing-parent","reason":"reorganize"}`, "merge target parent"},
	} {
		path := filepath.Join(t.TempDir(), route.command+".json")
		if err := os.WriteFile(path, []byte(route.payload), 0o600); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		if err := run(ctx, []string{route.command, "--repo-root", repo,
			"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
			"--request-file", path}, &output); err == nil ||
			!strings.Contains(err.Error(), route.expected) {
			t.Fatalf("CLI %s did not enforce its source/parent: %v", route.command, err)
		}
	}
}

func TestHealthCommandProbesSelectedBackend(t *testing.T) {
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			t.Errorf("unexpected Hindsight health request: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer native.Close()
	var output bytes.Buffer
	if err := run(context.Background(), []string{"health", "--repo-root", t.TempDir(),
		"--workspace", "alpha", "--profile", "team", "--backend", "hindsight",
		"--base-url", native.URL}, &output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil ||
		result["ok"] != true || result["native_ok"] != true ||
		result["backend"] != "hindsight" {
		t.Fatalf("CLI health differs: %+v, %v", result, err)
	}
}

func TestCommandVerificationSubjectAndPromote(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "CLI promotion proof",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	base := []string{"--repo-root", repo, "--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne"}
	request := engine.PromoteRequest{PromotionReviewRequest: engine.PromotionReviewRequest{
		VerificationSubjectRequest: engine.VerificationSubjectRequest{MemoryID: source.BlockID,
			ExpectedSourceRevision: source.RevisionID}}, Reason: "verified for CLI",
		OperationID: "cli-promotion-1"}
	requestPath := filepath.Join(t.TempDir(), "promotion.json")
	writeRequest := func(value any) {
		t.Helper()
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(requestPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRequest(request.VerificationSubjectRequest)
	var output bytes.Buffer
	args := append(append([]string{"verification-subject"}, base...), "--request-file", requestPath)
	if err := run(ctx, args, &output); err != nil {
		t.Fatal(err)
	}
	var subjectResult struct {
		Subject memory.CandidateVerificationSubject `json:"subject"`
	}
	if err := json.Unmarshal(output.Bytes(), &subjectResult); err != nil ||
		subjectResult.Subject.MemoryID != source.BlockID {
		t.Fatalf("CLI verification subject differs: %+v, %v", subjectResult, err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: repo, Subject: subjectResult.Subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"The CLI candidate passed independent review"}})
	if err != nil {
		t.Fatal(err)
	}
	request.Reports = []ledger.VerificationReport{report}
	writeRequest(request)
	args[0] = "promote"
	output.Reset()
	if err := run(ctx, args, &output); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result["saved"] != true ||
		result["status"] != "accepted" {
		t.Fatalf("CLI promotion failed: %+v, %v", result, err)
	}
	output.Reset()
	if err := run(ctx, args, &output); err != nil {
		t.Fatal(err)
	}
	var replay map[string]any
	if err := json.Unmarshal(output.Bytes(), &replay); err != nil || replay["replayed"] != true ||
		replay["block_id"] != result["block_id"] {
		t.Fatalf("CLI promotion retry failed: %+v, %v", replay, err)
	}
}

func TestCommandCreatesRoutedCandidateWithoutCanonicalJSON(t *testing.T) {
	var nativeDocument map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/documents/"):
			if nativeDocument == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(nativeDocument)
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/banks/"):
			w.Write([]byte(`{}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/memories"):
			var body struct {
				Items []struct {
					Content    string         `json:"content"`
					DocumentID string         `json:"document_id"`
					Metadata   map[string]any `json:"metadata"`
				} `json:"items"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Items) != 1 ||
				body.Items[0].Content != "Go routed fact" {
				t.Errorf("routed candidate changed native content: %+v, %v", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			item := body.Items[0]
			nativeDocument = map[string]any{"id": item.DocumentID, "bank_id": item.Metadata["native_partition_id"],
				"original_text": item.Content, "document_metadata": item.Metadata}
			w.Write([]byte(`{"success":true}`))
		default:
			t.Errorf("unexpected native request: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	requestPath := filepath.Join(t.TempDir(), "candidate.json")
	if err := os.WriteFile(requestPath, []byte(`{"operation_id":"cli-operation-1","content":"Go routed fact","requested_block_id":"stable_project_facts","tags":["fact"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	base := []string{"--repo-root", repo, "--workspace", "alpha", "--profile", "team", "--backend", "hindsight", "--base-url", server.URL}
	args := append([]string{"remember-candidate"}, base...)
	args = append(args, "--request-file", requestPath)
	var output bytes.Buffer
	if err := run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var result engine.CandidateResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || !result.Saved || result.RecordID == "" || result.Receipt == nil || result.Receipt.ProjectionPending {
		t.Fatalf("Go candidate CLI response differs: %+v, %v", result, err)
	}
	output.Reset()
	if err := run(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	var replay engine.CandidateResult
	if err := json.Unmarshal(output.Bytes(), &replay); err != nil || !replay.Replayed || replay.RecordID != result.RecordID {
		t.Fatalf("Go candidate CLI operation did not replay: %+v, %v", replay, err)
	}
	output.Reset()
	statusArgs := append([]string{"operation-status"}, base...)
	statusArgs = append(statusArgs, "--operation-id", "cli-operation-1")
	if err := run(context.Background(), statusArgs, &output); err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status["record_id"] != result.RecordID ||
		status["durable_status"] != "committed" || status["projection_status"] != "committed" {
		t.Fatalf("Go candidate CLI operation status differs: %+v, %v", status, err)
	}
	output.Reset()
	barrierArgs := append([]string{"projection-barrier"}, base...)
	barrierArgs = append(barrierArgs, "--record-id", result.RecordID, "--timeout-seconds", "0")
	if err := run(context.Background(), barrierArgs, &output); err != nil {
		t.Fatal(err)
	}
	var barrier map[string]any
	if err := json.Unmarshal(output.Bytes(), &barrier); err != nil || barrier["ok"] != true ||
		barrier["projection_status"] != "committed" || barrier["projection_revision"] != status["commit_revision"] {
		t.Fatalf("Go candidate CLI projection barrier differs: %+v, %v", barrier, err)
	}
	output.Reset()
	readArgs := append([]string{"read-record"}, base...)
	readArgs = append(readArgs, "--record-id", result.RecordID)
	if err := run(context.Background(), readArgs, &output); err != nil {
		t.Fatal(err)
	}
	var record memory.MemoryRecord
	if err := json.Unmarshal(output.Bytes(), &record); err != nil || record.Content != "Go routed fact" ||
		record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID != "stable_project_facts" {
		t.Fatalf("Go routed CLI record differs: %+v, %v", record, err)
	}
	output.Reset()
	if err := run(context.Background(), append([]string{"export-graph"}, base...), &output); err != nil {
		t.Fatal(err)
	}
	var graph map[string]any
	if err := json.Unmarshal(output.Bytes(), &graph); err != nil ||
		graph["audit"].(map[string]any)["record_count"] != float64(1) {
		t.Fatalf("Go CLI graph export lost canonical record: %+v, %v", graph, err)
	}
}

func TestBlockMapCommandReadsWorkspaceAndMetaRoots(t *testing.T) {
	repo := t.TempDir()
	var output bytes.Buffer
	err := run(context.Background(), []string{"block-map", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "hindsight",
		"--depth", "1", "--include-meta"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK    bool `json:"ok"`
		Roots []struct {
			BlockID  string `json:"block_id"`
			NodeKind string `json:"node_kind"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || !response.OK ||
		len(response.Roots) != 2 || response.Roots[0].NodeKind != "workspace" ||
		response.Roots[1].BlockID != "memory_root_index" {
		t.Fatalf("Go block-map CLI lost catalog roots: %+v, %v", response, err)
	}
}

func TestConvertBlockToIndexCommand(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	leaf, err := selected.CreateBlock(ctx, engine.CreateBlockRequest{Name: "CLI future topics",
		Synopsis: "Unused leaf", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(repo, "convert-request.json")
	request, _ := json.Marshal(engine.ConvertBlockToIndexRequest{BlockID: leaf["block_id"].(string),
		ExpectedRevision: 1, Reason: "nest topics"})
	if err := os.WriteFile(requestPath, request, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"convert-block-to-index", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var converted map[string]any
	if err := json.Unmarshal(output.Bytes(), &converted); err != nil || converted["ok"] != true ||
		converted["index_only"] != true || converted["backend_partition_id"] != "" {
		t.Fatalf("CLI leaf conversion failed: %+v, %v", converted, err)
	}
}

func TestForgetCommandWritesRetractedSuccessor(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "A temporary CLI fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1"})
	if err != nil || !result.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", result, err)
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil {
		t.Fatalf("candidate record missing: %+v, %v", record, err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(engine.ForgetRequest{MemoryID: record.BlockID,
		ExpectedRevision: record.RevisionID, Reason: "CLI withdrawal"})
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(repo, "forget-request.json")
	if err := os.WriteFile(requestPath, request, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"forget", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil || payload["saved"] != true ||
		payload["revision_of"] != record.BlockID || payload["projection_status"] != "committed" {
		t.Fatalf("CLI forget response is invalid: %+v, %v", payload, err)
	}
}

func TestDeprecateCommandArchivesCanonicalSuccessor(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "Old CLI fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	old, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(engine.DeprecateRequest{MemoryID: old.BlockID,
		ExpectedRevision: old.RevisionID, Reason: "newer source"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "deprecate-request.json")
	if err := os.WriteFile(path, request, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"deprecate", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--request-file", path}, &output); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil ||
		payload["saved"] != true || payload["revision_of"] != old.BlockID {
		t.Fatalf("CLI deprecate response is invalid: %+v, %v", payload, err)
	}
}

func TestRollbackCommandRestoresHistoricalRevision(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "Original CLI fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	old, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	newContent := "Changed CLI fact"
	changed, err := selected.Amend(ctx, engine.AmendRequest{MemoryID: old.BlockID,
		ExpectedRevision: old.RevisionID, Reason: "correction", Content: &newContent,
		RequestedBlockID: "stable_project_facts"})
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := json.Marshal(engine.RollbackRequest{MemoryID: changed["new_id"].(string),
		RevisionID: old.RevisionID, Reason: "restore known fact"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "rollback-request.json")
	if err := os.WriteFile(path, request, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"rollback", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--request-file", path}, &output); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil ||
		payload["saved"] != true || payload["rollback_target_id"] != old.BlockID {
		t.Fatalf("CLI rollback response is invalid: %+v, %v", payload, err)
	}
}

func TestAmendCommandWritesCorrectedSuccessor(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "Original CLI fact",
		Title: "CLI fact", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	original, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || original == nil {
		t.Fatalf("candidate record missing: %+v, %v", original, err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	content := "Corrected CLI fact"
	request, err := json.Marshal(engine.AmendRequest{MemoryID: original.BlockID,
		ExpectedRevision: original.RevisionID, Reason: "CLI correction", Content: &content,
		OperationID: "cli-amend-1"})
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(repo, "amend-request.json")
	if err := os.WriteFile(requestPath, request, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(ctx, []string{"amend", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil || payload["saved"] != true ||
		payload["old_id"] != original.BlockID || payload["projection_status"] != "committed" {
		t.Fatalf("CLI amend response is invalid: %+v, %v", payload, err)
	}
	output.Reset()
	if err := run(ctx, []string{"amend", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var replay map[string]any
	if err := json.Unmarshal(output.Bytes(), &replay); err != nil || replay["replayed"] != true ||
		replay["new_id"] != payload["new_id"] {
		t.Fatalf("CLI amend receipt did not replay: %+v, %v", replay, err)
	}
	edgeRequest, err := json.Marshal(engine.AddEdgeRequest{SrcID: payload["new_id"].(string),
		DstID: original.BlockID, EdgeType: "derived_from", Reason: "CLI correction lineage",
		EvidenceRefs: []memory.EvidenceRef{{RefType: "test_run", RefID: "cli-edge-run",
			ProducedBy: "verifier", ProducerID: "go-test"}}})
	if err != nil {
		t.Fatal(err)
	}
	edgePath := filepath.Join(repo, "edge-request.json")
	if err := os.WriteFile(edgePath, edgeRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(ctx, []string{"add-edge", "--repo-root", repo, "--workspace", "alpha",
		"--profile", "team", "--backend", "mnemosyne", "--request-file", edgePath}, &output); err != nil {
		t.Fatal(err)
	}
	var edge map[string]any
	if err := json.Unmarshal(output.Bytes(), &edge); err != nil || edge["ok"] != true ||
		edge["src_id"] != payload["new_id"] {
		t.Fatalf("CLI add-edge response is invalid: %+v, %v", edge, err)
	}
}

func TestExpandBlockCommandPagesProtectedTaxonomy(t *testing.T) {
	repo := t.TempDir()
	var output bytes.Buffer
	err := run(context.Background(), []string{"expand-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--block", "memory_root_index", "--limit", "1"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		OK         bool   `json:"ok"`
		NextCursor string `json:"next_cursor"`
		Children   []struct {
			BlockID string `json:"block_id"`
		} `json:"children"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || !response.OK ||
		len(response.Children) != 1 || response.NextCursor == "" {
		t.Fatalf("Go expand-block CLI lost protected taxonomy page: %+v, %v", response, err)
	}
}

func TestCreateBlockCommandPublishesNewWorkspaceLeaf(t *testing.T) {
	repo := t.TempDir()
	var overview bytes.Buffer
	if err := run(context.Background(), []string{"block-map", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne"}, &overview); err != nil {
		t.Fatal(err)
	}
	var mapped struct {
		Roots []struct {
			BlockID string `json:"block_id"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(overview.Bytes(), &mapped); err != nil || len(mapped.Roots) != 1 {
		t.Fatalf("workspace root lookup failed: %+v, %v", mapped, err)
	}
	requestPath := filepath.Join(repo, "create-request.json")
	request, _ := json.Marshal(engine.CreateBlockRequest{Name: "CLI notes", Synopsis: "Durable CLI notes",
		ParentID: mapped.Roots[0].BlockID, Reason: "new category"})
	if err := os.WriteFile(requestPath, request, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), []string{"create-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var created struct {
		OK        bool   `json:"ok"`
		BlockID   string `json:"block_id"`
		ParentID  string `json:"parent_id"`
		Partition string `json:"backend_partition_id"`
	}
	if err := json.Unmarshal(output.Bytes(), &created); err != nil || !created.OK ||
		created.BlockID == "" || created.ParentID != mapped.Roots[0].BlockID || created.Partition == "" {
		t.Fatalf("CLI block creation is invalid: %+v, %v", created, err)
	}
	observationRequest, _ := json.Marshal(map[string]any{"block_id": created.BlockID,
		"observation_type": "low_usage", "values": map[string]any{"reads": 0},
		"threshold_revision": "threshold-1", "evidence_ids": []string{"cli-run"}})
	if err := os.WriteFile(requestPath, observationRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"observe-maintenance", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var observed map[string]any
	if err := json.Unmarshal(output.Bytes(), &observed); err != nil || observed["ok"] != true {
		t.Fatalf("CLI maintenance observation failed: %+v, %v", observed, err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"navigation-envelope", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--block", created.BlockID}, &output); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || envelope["ok"] != true ||
		len(envelope["maintenance_observations"].([]any)) != 1 {
		t.Fatalf("CLI navigation envelope lost maintenance facts: %+v, %v", envelope, err)
	}
	updatedName := "Reviewed CLI notes"
	amendRequest, _ := json.Marshal(engine.AmendBlockRequest{BlockID: created.BlockID,
		ExpectedRevision: 1, Reason: "review", Name: &updatedName})
	if err := os.WriteFile(requestPath, amendRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"amend-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var amended struct {
		OK       bool   `json:"ok"`
		Name     string `json:"name"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(output.Bytes(), &amended); err != nil || !amended.OK ||
		amended.Name != updatedName || amended.Revision != 2 {
		t.Fatalf("CLI block amendment is invalid: %+v, %v", amended, err)
	}
	archiveRequest, _ := json.Marshal(engine.BlockTransitionRequest{BlockID: created.BlockID,
		ExpectedRevision: 2, Reason: "retire CLI notes"})
	if err := os.WriteFile(requestPath, archiveRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"archive-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var archived struct {
		OK       bool   `json:"ok"`
		Status   string `json:"status"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(output.Bytes(), &archived); err != nil || !archived.OK ||
		archived.Status != "archived" || archived.Revision != 3 {
		t.Fatalf("CLI block archive is invalid: %+v, %v", archived, err)
	}
	reviveRequest, _ := json.Marshal(engine.BlockTransitionRequest{BlockID: created.BlockID,
		ExpectedRevision: 3, Reason: "restore CLI notes"})
	if err := os.WriteFile(requestPath, reviveRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"revive-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var revived struct {
		OK       bool   `json:"ok"`
		Status   string `json:"status"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(output.Bytes(), &revived); err != nil || !revived.OK ||
		revived.Status != "active" || revived.Revision != 4 {
		t.Fatalf("CLI block revival is invalid: %+v, %v", revived, err)
	}
	indexRequest, _ := json.Marshal(engine.CreateBlockRequest{Name: "CLI index", Synopsis: "New parent",
		ParentID: mapped.Roots[0].BlockID, Reason: "reorganize", IndexOnly: true})
	if err := os.WriteFile(requestPath, indexRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"create-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var parent struct {
		BlockID string `json:"block_id"`
	}
	if err := json.Unmarshal(output.Bytes(), &parent); err != nil || parent.BlockID == "" {
		t.Fatalf("CLI index creation is invalid: %+v, %v", parent, err)
	}
	moveRequest, _ := json.Marshal(engine.MoveBlockRequest{BlockID: created.BlockID,
		ParentID: parent.BlockID, ExpectedRevision: 4, Reason: "new parent"})
	if err := os.WriteFile(requestPath, moveRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"move-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var moved struct {
		OK       bool   `json:"ok"`
		ParentID string `json:"parent_id"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(output.Bytes(), &moved); err != nil || !moved.OK ||
		moved.ParentID != parent.BlockID || moved.Revision != 5 {
		t.Fatalf("CLI block move is invalid: %+v, %v", moved, err)
	}
	archiveMoved, _ := json.Marshal(engine.BlockTransitionRequest{BlockID: created.BlockID,
		ExpectedRevision: 5, Reason: "delete CLI notes"})
	if err := os.WriteFile(requestPath, archiveMoved, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"archive-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	deleteRequest, _ := json.Marshal(engine.DeleteBlockRequest{BlockID: created.BlockID,
		ExpectedRevision: 6, Reason: "remove CLI notes"})
	if err := os.WriteFile(requestPath, deleteRequest, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), []string{"delete-block", "--repo-root", repo,
		"--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne",
		"--request-file", requestPath}, &output); err != nil {
		t.Fatal(err)
	}
	var deleted struct {
		OK      bool `json:"ok"`
		Deleted bool `json:"deleted"`
	}
	if err := json.Unmarshal(output.Bytes(), &deleted); err != nil || !deleted.OK || !deleted.Deleted {
		t.Fatalf("CLI block deletion is invalid: %+v, %v", deleted, err)
	}
}

func TestCommandWritesReadsAndRecallsThroughNativeHindsight(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	record.CanonicalKey = "alpha:fact"
	nativeDocuments := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/documents/"):
			id := request.URL.Path[strings.LastIndex(request.URL.Path, "/documents/")+len("/documents/"):]
			document, found := nativeDocuments[id]
			if !found {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(document)
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/banks/"):
			w.Write([]byte(`{}`))
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/memories"):
			var body struct {
				Items []struct {
					Content    string         `json:"content"`
					DocumentID string         `json:"document_id"`
					Metadata   map[string]any `json:"metadata"`
					Tags       []string       `json:"tags"`
				} `json:"items"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Items) != 1 ||
				len(body.Items[0].Tags) == 0 || body.Items[0].Tags[0] != "um_mem:block:stable_project_facts" ||
				map[string]string{"UM-MEM:block:mem_bound": record.Content,
					"UM-MEM:block:mem_corrected": "Corrected fact"}[body.Items[0].DocumentID] != body.Items[0].Content {
				t.Errorf("native write identity or content differs: document=%q", body.Items[0].DocumentID)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			item := body.Items[0]
			bankID := item.Metadata["native_partition_id"]
			nativeDocuments[item.DocumentID] = map[string]any{"id": item.DocumentID, "bank_id": bankID,
				"original_text": item.Content, "document_metadata": item.Metadata}
			w.Write([]byte(`{"success":true}`))
		default:
			t.Errorf("unexpected native request: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	repo := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "record.json")
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--repo-root", repo, "--workspace", "alpha", "--profile", "team", "--backend", "hindsight", "--base-url", server.URL}
	var output bytes.Buffer
	putArgs := append([]string{"put-root"}, base...)
	putArgs = append(putArgs, "--record-file", recordPath, "--block", "stable_project_facts")
	if err := run(context.Background(), putArgs, &output); err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		RecordID          string `json:"record_id"`
		ProjectionPending bool   `json:"projection_pending"`
	}
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil || receipt.RecordID != record.BlockID || receipt.ProjectionPending {
		t.Fatalf("CLI write receipt differs: %+v, %v", receipt, err)
	}
	output.Reset()
	readArgs := append([]string{"read-record"}, base...)
	readArgs = append(readArgs, "--record-id", record.BlockID)
	if err := run(context.Background(), readArgs, &output); err != nil {
		t.Fatal(err)
	}
	var read memory.MemoryRecord
	if err := json.Unmarshal(output.Bytes(), &read); err != nil || read.Content != record.Content {
		t.Fatalf("CLI read did not return committed record: %+v, %v", read, err)
	}
	output.Reset()
	recentArgs := append([]string{"recent"}, base...)
	recentArgs = append(recentArgs, "--limit", "8", "--logical-block-id", "stable_project_facts")
	if err := run(context.Background(), recentArgs, &output); err != nil {
		t.Fatal(err)
	}
	var recent []memory.MemoryRecord
	if err := json.Unmarshal(output.Bytes(), &recent); err != nil || len(recent) != 1 || recent[0].BlockID != record.BlockID {
		t.Fatalf("CLI recent lost the committed record: %+v, %v", recent, err)
	}
	output.Reset()
	listArgs := append([]string{"list-records"}, base...)
	listArgs = append(listArgs, "--scope", "workspace", "--scope-id", "alpha")
	if err := run(context.Background(), listArgs, &output); err != nil {
		t.Fatal(err)
	}
	var page engine.RecordPage
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || page.SnapshotCount != 1 ||
		len(page.Records) != 1 || page.Records[0]["record_id"] != record.BlockID {
		t.Fatalf("CLI list-records lost the canonical ID: %+v, %v", page, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.RecordIDs = []string{record.BlockID}
	plan.SearchAll = true
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	planPath := filepath.Join(t.TempDir(), "plan.json")
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	recallArgs := append([]string{"recall"}, base...)
	recallArgs = append(recallArgs, "--plan-file", planPath)
	if err := run(context.Background(), recallArgs, &output); err != nil {
		t.Fatal(err)
	}
	var hits []memory.MemoryRecord
	if err := json.Unmarshal(output.Bytes(), &hits); err != nil || len(hits) != 1 || hits[0].BlockID != record.BlockID {
		t.Fatalf("CLI exact recall differs: %+v, %v", hits, err)
	}
	successor := record
	successor.BlockID = "mem_corrected"
	successor.RevisionID = "rev_2"
	successor.RevisionOf = record.BlockID
	successor.SupersedesIDs = []string{record.BlockID}
	successor.Content = "Corrected fact"
	successor.ValidFrom = "2024-02-01T00:00:00Z"
	successor.RecordedAt, successor.UpdatedAt = successor.ValidFrom, successor.ValidFrom
	successorPath := filepath.Join(t.TempDir(), "successor.json")
	successorJSON, err := json.Marshal(successor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(successorPath, successorJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	amendArgs := append([]string{"put-successor"}, base...)
	amendArgs = append(amendArgs, "--record-file", successorPath, "--block", "stable_project_facts", "--action", "correction")
	if err := run(context.Background(), amendArgs, &output); err != nil {
		t.Fatal(err)
	}
	plan.RecordIDs = []string{record.BlockID, successor.BlockID}
	planJSON, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), recallArgs, &output); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &hits); err != nil || len(hits) != 1 || hits[0].BlockID != successor.BlockID {
		t.Fatalf("CLI correction did not replace current head: %+v, %v", hits, err)
	}
	stale := successor
	stale.BlockID = "mem_stale"
	stale.RevisionID = "rev_stale"
	stale.RevisionOf = record.BlockID
	stale.SupersedesIDs = []string{record.BlockID}
	stalePath := filepath.Join(t.TempDir(), "stale.json")
	staleJSON, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, staleJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	staleArgs := append([]string{"put-successor"}, base...)
	staleArgs = append(staleArgs, "--record-file", stalePath, "--block", "stable_project_facts", "--action", "correction")
	output.Reset()
	if err := run(context.Background(), staleArgs, &output); err == nil {
		t.Fatal("stale correction crossed canonical-head compare-and-swap")
	}
	if len(nativeDocuments) != 2 {
		t.Fatal("stale correction reached native Hindsight")
	}
	retracted := successor
	retracted.BlockID = "mem_retracted"
	retracted.RevisionID = "rev_3"
	retracted.RevisionOf = successor.BlockID
	retracted.SupersedesIDs = []string{successor.BlockID}
	retracted.Status = "retracted"
	retracted.Retention = "archive"
	retracted.DeliveryPolicy = "hidden"
	retracted.ValidFrom = "2024-03-01T00:00:00Z"
	retracted.RecordedAt, retracted.UpdatedAt = retracted.ValidFrom, retracted.ValidFrom
	retractedPath := filepath.Join(t.TempDir(), "retracted.json")
	retractedJSON, err := json.Marshal(retracted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retractedPath, retractedJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	forgetArgs := append([]string{"put-successor"}, base...)
	forgetArgs = append(forgetArgs, "--record-file", retractedPath, "--block", "stable_project_facts", "--action", "retract")
	if err := run(context.Background(), forgetArgs, &output); err != nil {
		t.Fatal(err)
	}
	plan.RecordIDs = []string{record.BlockID, successor.BlockID, retracted.BlockID}
	planJSON, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), recallArgs, &output); err != nil {
		t.Fatal(err)
	}
	hits = nil
	if err := json.Unmarshal(output.Bytes(), &hits); err != nil || len(hits) != 0 {
		t.Fatalf("CLI retraction left a visible head: %+v, %v", hits, err)
	}
	plan.TemporalMode = "historical"
	plan.AsOf = "2024-02-15T00:00:00Z"
	planJSON, err = json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, planJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(context.Background(), recallArgs, &output); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(output.Bytes(), &hits); err != nil || len(hits) != 1 || hits[0].BlockID != successor.BlockID {
		t.Fatalf("CLI lost historical revision after retraction: %+v, %v", hits, err)
	}
}
