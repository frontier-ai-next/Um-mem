package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/erasure"
	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/internal/native/mempalace"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/internal/telemetry"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func authorizedEraseRequest(t *testing.T, repo string, request memory.MemoryEraseRequest) memory.MemoryEraseRequest {
	t.Helper()
	approval, err := ledger.Append(repo, request.WorkspaceID, "supervisor", "privacy",
		"authorize_memory_erase", map[string]any{"action": "erase_memory", "approved": true,
			"request_id": request.RequestID, "memory_id_sha256": memory.EraseSHA256(request.MemoryID),
			"reason_sha256": memory.EraseSHA256(request.Reason)})
	if err != nil {
		t.Fatal(err)
	}
	request.Authorization = memory.EvidenceRef{RefType: "ledger_event", RefID: approval.ID,
		Hash: &approval.Hash, ProducedBy: "supervisor", ProducerID: "supervisor:" + approval.ID}
	return request
}

func TestEraseRequiresAuthorizationAndRejectsUnsupportedNative(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "private evidence",
		RequestedBlockID: "stable_project_facts", SessionID: "erase-test", RunID: "erase-test"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v %v", created, err)
	}
	request := memory.MemoryEraseRequest{RequestID: "erase-1", MemoryID: created.RecordID,
		WorkspaceID: "alpha", Reason: "privacy"}
	denied, err := selected.Erase(ctx, request)
	if err != nil || denied["error"] != "invalid_erase_authorization" {
		t.Fatalf("unapproved erase was accepted: %+v %v", denied, err)
	}
	if _, err := os.Stat(erasure.JournalPath(repo)); !os.IsNotExist(err) {
		t.Fatalf("unapproved erase created journal: %v", err)
	}
	trace, err := os.ReadFile(telemetry.Path(repo))
	if err != nil || strings.Contains(string(trace), request.MemoryID) ||
		strings.Contains(string(trace), "private evidence") || !strings.Contains(string(trace), "memory_erase_rejected") {
		t.Fatalf("denied erase trace leaked content or disappeared: %q %v", trace, err)
	}
	request = authorizedEraseRequest(t, repo, request)
	unsupported, err := selected.Erase(ctx, request)
	if err != nil || unsupported["error"] != "native_purge_unsupported" ||
		unsupported["erase_request"].(erasure.RequestState).Status != "unavailable" {
		t.Fatalf("unsupported backend did not fail closed: %+v %v", unsupported, err)
	}
	replay, err := selected.Erase(ctx, request)
	if err != nil || replay["error"] != unsupported["error"] {
		t.Fatalf("unavailable erase replay changed: %+v %v", replay, err)
	}
	record, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || record == nil || record.Status == "retracted" {
		t.Fatalf("unsupported erase changed sidecar: %+v %v", record, err)
	}
}

func TestEraseMemPalaceVerifiesNativeClosureAndReplay(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	ctx := context.Background()
	repo := t.TempDir()
	config := Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", MiniLMModelDir: modelDir}
	selected, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "private palace evidence",
		RequestedBlockID: "stable_project_facts", SessionID: "erase-test", RunID: "erase-test"})
	if err != nil || !created.Saved {
		t.Fatalf("MemPalace candidate setup failed: %+v %v", created, err)
	}
	neighbor, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "neighbor evidence must remain",
		RequestedBlockID: "stable_project_facts", SessionID: "neighbor-test", RunID: "neighbor-test"})
	if err != nil || !neighbor.Saved || neighbor.RecordID == created.RecordID {
		t.Fatalf("independent neighbor setup failed: %+v %v", neighbor, err)
	}
	before, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || before == nil {
		t.Fatalf("canonical record missing: %+v %v", before, err)
	}
	request := authorizedEraseRequest(t, repo, memory.MemoryEraseRequest{RequestID: "erase-mempalace",
		MemoryID: created.RecordID, WorkspaceID: "alpha", Reason: "privacy"})
	result, err := selected.Erase(ctx, request)
	if err != nil || result["ok"] != true {
		t.Fatalf("governed erase failed: %+v %v", result, err)
	}
	state, ok := result["erase_request"].(erasure.RequestState)
	if !ok || state.Status != "completed" || state.TargetIDSHA256 != memory.EraseSHA256(request.MemoryID) {
		t.Fatalf("erase request did not close with the target hash: %+v", result)
	}
	certificate, ok := result["certificate"].(map[string]any)
	if !ok || len(certificate["certificate_sha256"].(string)) != 64 {
		t.Fatalf("erase certificate is absent: %+v", result)
	}
	proof, err := json.Marshal(map[string]any{"erase_request": state, "certificate": certificate})
	if err != nil || strings.Contains(string(proof), request.Reason) ||
		strings.Contains(string(proof), request.MemoryID) || strings.Contains(string(proof), before.Content) {
		t.Fatalf("erase proof retained plaintext: %s %v", proof, err)
	}
	trace, err := os.ReadFile(telemetry.Path(repo))
	if err != nil || !strings.Contains(string(trace), "memory_erase_completed") ||
		strings.Contains(string(trace), before.Content) || strings.Contains(string(trace), request.Reason) ||
		strings.Contains(string(trace), request.MemoryID) {
		t.Fatalf("completed erase trace leaked content or disappeared: %q %v", trace, err)
	}
	retainedID, _ := result["block_id"].(string)
	retained, err := selected.ReadStored(ctx, retainedID)
	if err != nil || retained == nil || retained.Status != "retracted" ||
		retained.Retention != "archive" || retained.DeliveryPolicy != "hidden" ||
		retained.Content != before.Content {
		t.Fatalf("canonical raw evidence was not retained: %+v %v", retained, err)
	}
	client, err := mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []memory.MemoryRecord{*before, *retained} {
		rows, err := client.Get(ctx, record, []string{record.BlockID})
		if err != nil || len(rows) != 0 {
			t.Fatalf("erased native ID remained: %+v %v", rows, err)
		}
	}
	neighborRecord, err := selected.ReadStored(ctx, neighbor.RecordID)
	if err != nil || neighborRecord == nil {
		t.Fatalf("neighbor sidecar disappeared: %+v %v", neighborRecord, err)
	}
	neighborRows, err := client.Get(ctx, *neighborRecord, []string{neighbor.RecordID})
	if err != nil || len(neighborRows) != 1 || neighborRows[0].Document != neighborRecord.Content {
		t.Fatalf("exact purge removed a neighbor: %+v %v", neighborRows, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.Erase(ctx, request)
	if err != nil || replay["ok"] != true || replay["block_id"] != retainedID ||
		replay["certificate"].(map[string]any)["certificate_sha256"] != certificate["certificate_sha256"] {
		t.Fatalf("completed erase did not replay: %+v %v", replay, err)
	}
	trace, err = os.ReadFile(telemetry.Path(repo))
	if err != nil || strings.Count(string(trace), "memory_erase_completed") != 1 {
		t.Fatalf("completed erase replay duplicated completion event: %q %v", trace, err)
	}
}

func TestEraseMemPalaceResumesVerifiedNativeAbsence(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "resume purge evidence",
		RequestedBlockID: "stable_project_facts", SessionID: "resume-test", RunID: "resume-test"})
	if err != nil || !created.Saved {
		t.Fatalf("MemPalace candidate setup failed: %+v %v", created, err)
	}
	before, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || before == nil {
		t.Fatalf("canonical record missing: %+v %v", before, err)
	}
	client, err := mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if purged, err := client.PurgeExact(ctx, *before, false); err != nil || !purged.OK {
		t.Fatalf("could not simulate interrupted native purge: %+v %v", purged, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	request := authorizedEraseRequest(t, repo, memory.MemoryEraseRequest{RequestID: "erase-resume",
		MemoryID: created.RecordID, WorkspaceID: "alpha", Reason: "privacy"})
	first, err := selected.Erase(ctx, request)
	if err != nil || first["ok"] != false || first["error"] != "native_exact_id_not_found" ||
		first["erase_request"].(erasure.RequestState).Status != "purging" || first["certificate"] != nil {
		t.Fatalf("missing native ID was certified on first attempt: %+v %v", first, err)
	}
	resumed, err := selected.Erase(ctx, request)
	if err != nil || resumed["ok"] != true ||
		resumed["erase_request"].(erasure.RequestState).Status != "completed" {
		t.Fatalf("resumed erase did not verify native absence: %+v %v", resumed, err)
	}
}

func TestEraseMemPalaceClosesLongRevisionChainAndBlocksRollback(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	longContent := strings.Repeat("sensitive memorandum with a distinct factual clause. ", 90)
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: longContent,
		RequestedBlockID: "stable_project_facts", SessionID: "long-erase", RunID: "long-erase"})
	if err != nil || !created.Saved {
		t.Fatalf("long candidate setup failed: %+v %v", created, err)
	}
	original, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || original == nil {
		t.Fatalf("original long record missing: %+v %v", original, err)
	}
	newContent := "amended private memorandum"
	amendRequest := AmendRequest{MemoryID: original.BlockID,
		ExpectedRevision: original.RevisionID, Reason: "corrected source", Content: &newContent,
		OperationID: "erase-chain-amend"}
	amended, err := selected.Amend(ctx, amendRequest)
	if err != nil || amended["saved"] != true {
		t.Fatalf("long revision amendment failed: %+v %v", amended, err)
	}
	amendedID := amended["new_id"].(string)
	current, err := selected.ReadStored(ctx, amendedID)
	if err != nil || current == nil {
		t.Fatalf("amended head missing: %+v %v", current, err)
	}
	client, err := mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.Get(ctx, *original, []string{original.BlockID, original.BlockID + ":window:0000"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("long record did not project canonical and window rows: %+v %v", rows, err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	request := authorizedEraseRequest(t, repo, memory.MemoryEraseRequest{RequestID: "erase-long-chain",
		MemoryID: original.BlockID, WorkspaceID: "alpha", Reason: "privacy"})
	result, err := selected.Erase(ctx, request)
	if err != nil || result["ok"] != true {
		t.Fatalf("long chain erase failed: %+v %v", result, err)
	}
	retained, err := selected.ReadStored(ctx, result["block_id"].(string))
	if err != nil || retained == nil || retained.Content != current.Content {
		t.Fatalf("retracted sidecar did not retain current evidence: %+v %v", retained, err)
	}
	client, err = mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []memory.MemoryRecord{*original, *current, *retained} {
		rows, err := client.Get(ctx, record, []string{record.BlockID, record.BlockID + ":window:0000"})
		if err != nil || len(rows) != 0 {
			t.Fatalf("erased revision retains native artifacts: %+v %v", rows, err)
		}
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.Rollback(ctx, RollbackRequest{MemoryID: original.BlockID,
		RevisionID: original.RevisionID, Reason: "restore"}); err == nil {
		t.Fatal("rollback restored an erased canonical chain")
	}
	replacement := "reproject hidden source"
	if _, err := selected.Amend(ctx, AmendRequest{MemoryID: retained.BlockID,
		ExpectedRevision: retained.RevisionID, Reason: "restore", Content: &replacement}); err == nil || !strings.Contains(err.Error(), "completed erase certificate") {
		t.Fatalf("amend accepted an erased canonical chain: %v", err)
	}
	if _, err := selected.Amend(ctx, amendRequest); err == nil ||
		!strings.Contains(err.Error(), "completed erase certificate") {
		t.Fatalf("committed amendment receipt replay exposed an erased chain: %v", err)
	}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: original.BlockID,
		Reason: "retry old lifecycle"}); err == nil || !strings.Contains(err.Error(), "completed erase certificate") {
		t.Fatalf("forget accepted an erased canonical chain: %v", err)
	}
	if _, err := selected.Deprecate(ctx, DeprecateRequest{MemoryID: original.BlockID,
		Reason: "reclassify"}); err == nil || !strings.Contains(err.Error(), "completed erase certificate") {
		t.Fatalf("deprecate accepted an erased canonical chain: %v", err)
	}
	if _, err := selected.CorrectFor(ctx, CorrectRequest{MemoryID: original.BlockID,
		RequestedBlockID: "stable_project_facts", Content: "new advisory copy",
		SessionID: "long-erase"}, "agent_mcp", 2); err == nil ||
		!strings.Contains(err.Error(), "completed erase certificate") {
		t.Fatalf("correction accepted an erased canonical chain: %v", err)
	}
}

func TestEraseMemPalaceConcurrentReplayKeepsCompletedCertificate(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "concurrent private source",
		RequestedBlockID: "stable_project_facts", SessionID: "race-erase", RunID: "race-erase"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v %v", created, err)
	}
	request := authorizedEraseRequest(t, repo, memory.MemoryEraseRequest{RequestID: "erase-race",
		MemoryID: created.RecordID, WorkspaceID: "alpha", Reason: "privacy"})
	results := make([]map[string]any, 2)
	errorsFound := make([]error, 2)
	var workers sync.WaitGroup
	for i := range results {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			results[index], errorsFound[index] = selected.Erase(ctx, request)
		}(i)
	}
	workers.Wait()
	for i, result := range results {
		if errorsFound[i] != nil || result["ok"] != true ||
			result["erase_request"].(erasure.RequestState).Status != "completed" {
			t.Fatalf("concurrent replay %d lost completion: %+v %v", i, result, errorsFound[i])
		}
	}
	first := results[0]["certificate"].(map[string]any)["certificate_sha256"]
	second := results[1]["certificate"].(map[string]any)["certificate_sha256"]
	if first != second {
		t.Fatalf("concurrent erase returned different certificates: %v %v", first, second)
	}
}

func TestEraseMemPalaceRemovesUnpublishedTopologyCopy(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	ctx := context.Background()
	repo := t.TempDir()
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team",
		BackendID: "mempalace", MiniLMModelDir: modelDir})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "unpublished topology private source",
		RequestedBlockID: "stable_project_facts", SessionID: "staged-erase", RunID: "staged-erase"})
	if err != nil || !created.Saved {
		t.Fatalf("source setup failed: %+v %v", created, err)
	}
	rows, err := selected.index.ProjectionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var source *store.ProjectionRow
	for i := range rows {
		if rows[i].Record.BlockID == created.RecordID {
			source = &rows[i]
			break
		}
	}
	if source == nil {
		t.Fatal("source projection row is unavailable")
	}
	targetID, err := newCatalogUUID()
	if err != nil {
		t.Fatal(err)
	}
	partitionID, err := newCatalogUUID()
	if err != nil {
		t.Fatal(err)
	}
	if err := selected.stageTopologyProjection(ctx, *source, targetID, partitionID, "abandoned"); err != nil {
		t.Fatal(err)
	}
	staged := source.Record
	membership := *staged.BlockMembership
	membership.PrimaryBlockID = targetID
	membership.BackendPartitionID = partitionID
	membership.CatalogRevisionID = "topology:abandoned"
	staged.BlockMembership = &membership
	request := authorizedEraseRequest(t, repo, memory.MemoryEraseRequest{RequestID: "erase-staged",
		MemoryID: created.RecordID, WorkspaceID: "alpha", Reason: "privacy"})
	erased, err := selected.Erase(ctx, request)
	if err != nil || erased["ok"] != true {
		t.Fatalf("erase with unpublished copy failed: %+v %v", erased, err)
	}
	client, err := mempalace.OpenNativeClient(repo, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, record := range []memory.MemoryRecord{source.Record, staged} {
		found, err := client.Get(ctx, record, []string{created.RecordID})
		if err != nil || len(found) != 0 {
			t.Fatalf("unpublished native copy survived erase: %+v %v", found, err)
		}
	}
}
