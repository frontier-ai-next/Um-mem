package engine

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/internal/receipts"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func metadataNumber(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case float64:
		return typed
	case json.Number:
		parsed, _ := strconv.ParseFloat(typed.String(), 64)
		return parsed
	default:
		return -1
	}
}

func TestLosslessAtomicBoundSourceVerifiesBytesAndReplays(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	lineage := HostLineage{SessionID: "session-1", ProjectID: "project-1"}
	source, err := selected.RegisterSource(ctx, "turn-1", "A🧠B", "trusted-host", "Ada", "", lineage)
	if err != nil {
		t.Fatal(err)
	}
	segment := map[string]any{"source_id": "turn-1", "source_sha256": source["source_sha256"],
		"byte_start": 0, "byte_end": 5, "text": "A🧠"}
	request := CandidateRequest{OperationID: "segment-1", WriteMode: "lossless_atomic",
		Content: "A🧠", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", ProjectID: "project-1", RunID: "session-1",
		Metadata: map[string]any{"source_segments": []any{segment}}}
	first, err := selected.RememberCandidate(ctx, request)
	if err != nil || !first.Saved || first.RecordID == "" {
		t.Fatalf("bound write failed: %+v, %v", first, err)
	}
	record, err := selected.ReadStored(ctx, first.RecordID)
	if err != nil || record == nil || record.Status != "accepted" ||
		record.VerificationStatus != "evidence_backed" ||
		metadataNumber(record.Metadata["exact_source_coverage"]) != float64(5)/6 {
		t.Fatalf("bound source was not verified: %+v, %v", record, err)
	}
	var reportID, reportHash string
	for _, ref := range record.EvidenceRefs {
		if ref.RefType == "verification_report" {
			reportID = ref.RefID
			if ref.Hash != nil {
				reportHash = *ref.Hash
			}
		}
	}
	if reportID == "" || reportHash == "" {
		t.Fatalf("verifier ledger reference missing: %+v", record.EvidenceRefs)
	}
	_, edges, _, err := selected.index.ExportGraphSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundWitnessEdge := false
	for _, edge := range edges {
		if edge.SrcID == record.BlockID && edge.DstID == source["witness_id"] &&
			edge.EdgeType == "derived_from" && edge.Reason == "agent_authored_from_raw_witness" {
			foundWitnessEdge = true
		}
	}
	if !foundWitnessEdge || len(first.ProvenanceEdges) != 1 {
		t.Fatalf("raw witness provenance edge missing: %+v, %+v", edges, first.ProvenanceEdges)
	}
	path, err := ledger.Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(data[:len(data)-1], &row); err != nil ||
		row["event_id"] != reportID || row["event_hash"] != reportHash ||
		row["actor"] != "verifier" || row["tool"] != "exact_source_fidelity" {
		t.Fatalf("verifier event differs from reference: %+v, %v", row, err)
	}
	replayed, err := selected.RememberCandidate(ctx, request)
	if err != nil || !replayed.Replayed || replayed.RecordID != first.RecordID {
		t.Fatalf("lossless receipt replay failed: %+v, %v", replayed, err)
	}
	secondData, err := os.ReadFile(path)
	if err != nil || string(secondData) != string(data) {
		t.Fatalf("replay appended a second verifier event: %v", err)
	}
	coverage, err := selected.SourceCoverage(ctx, []string{"turn-1"})
	if err != nil || coverage["covered_bytes"] != 5 {
		t.Fatalf("bound coverage differs: %+v, %v", coverage, err)
	}
	changed := request
	changed.Content = "A"
	if _, err := selected.RememberCandidate(ctx, changed); err == nil {
		t.Fatal("changed operation reused lossless receipt")
	}
}

func TestLosslessAtomicRejectsForgedBinding(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "turn-1", "A🧠B", "trusted-host", "Ada", "",
		HostLineage{SessionID: "session-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"source_id": "turn-1", "source_sha256": source["source_sha256"],
		"byte_start": 0, "byte_end": 5, "text": "A🧠"}
	request := CandidateRequest{WriteMode: "lossless_atomic", Content: "A🧠",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1",
		ProjectID: "project-1", RunID: "session-1"}
	for _, scenario := range []struct {
		name, session, project string
		segment                map[string]any
	}{
		{"foreign session", "other", "project-1", base},
		{"foreign project", "session-1", "other", base},
		{"spoofed speaker", "session-1", "project-1", map[string]any{
			"source_id": "turn-1", "source_sha256": source["source_sha256"],
			"byte_start": 0, "byte_end": 5, "text": "A🧠", "speaker": "Bob"}},
		{"split utf8", "session-1", "project-1", map[string]any{
			"source_id": "turn-1", "source_sha256": source["source_sha256"],
			"byte_start": 0, "byte_end": 2, "text": "A🧠"}},
		{"hash mismatch", "session-1", "project-1", map[string]any{
			"source_id": "turn-1", "source_sha256": strings.Repeat("0", 64),
			"byte_start": 0, "byte_end": 5, "text": "A🧠"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			attempt := request
			attempt.SessionID, attempt.ProjectID = scenario.session, scenario.project
			attempt.Metadata = map[string]any{"source_segments": []any{scenario.segment}}
			if _, err := selected.RememberCandidate(ctx, attempt); err == nil {
				t.Fatal("forged registered source was accepted")
			}
		})
	}
}

func TestLosslessAtomicUnregisteredStaysCandidate(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	result, err := selected.RememberCandidate(ctx, CandidateRequest{OperationID: "free-1",
		WriteMode: "lossless_atomic", Content: "exact 🧠 text",
		RequestedBlockID: "stable_project_facts", SourceID: "turn-free"})
	if err != nil || !result.Saved {
		t.Fatalf("unregistered lossless write failed: %+v, %v", result, err)
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil || record.Status != "candidate" ||
		record.VerificationStatus != "unverified" ||
		record.Metadata["source_coverage_status"] != "unregistered" ||
		metadataNumber(record.Metadata["exact_segment_fidelity"]) != 1 ||
		metadataNumber(record.Metadata["source_bytes"]) != float64(len([]byte("exact 🧠 text"))) {
		t.Fatalf("unregistered source claim was wrong: %+v, %v", record, err)
	}
}

func TestLosslessAtomicRecoversVerifierEventAfterCanonicalCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "turn-retry", "🧠", "trusted-host", "", "",
		HostLineage{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	extra, err := selected.RegisterSource(ctx, "turn-extra", "context", "trusted-host", "", "",
		HostLineage{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	extraHash := "sha256:" + extra["source_sha256"].(string)
	request := CandidateRequest{OperationID: "bound-crash", WriteMode: "lossless_atomic",
		Content: "🧠", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1",
		EvidenceRefs: []memory.EvidenceRef{{RefType: "artifact", RefID: extra["witness_id"].(string),
			Hash: &extraHash, ProducedBy: "watcher", ProducerID: "trusted-host"}},
		Metadata: map[string]any{"source_segments": []any{map[string]any{
			"source_id": "turn-retry", "source_sha256": source["source_sha256"],
			"byte_start": 0, "byte_end": 4, "text": "🧠"}}}}
	identity := receipts.Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne",
		CallerRole: "go_cli", ProtocolVersion: 2}
	fingerprint := request
	fingerprint.OperationID = ""
	arguments := map[string]any{"tool": "remember-candidate", "request": fingerprint}
	claim, err := receipts.Claim(ctx, root, identity, request.OperationID, arguments)
	if err != nil || claim.Status != "new" {
		t.Fatalf("creation receipt claim failed: %+v, %v", claim, err)
	}
	request.operationCallerRole, request.operationProtocolVersion = "go_cli", 2
	request.operationFingerprint, err = receipts.Fingerprint(arguments)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := selected.rememberCandidateOnce(ctx, request, claim.CreatedAt, false)
	if err != nil || !committed.Saved {
		t.Fatalf("canonical write before receipt failed: %+v, %v", committed, err)
	}
	path, err := ledger.Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: source["witness_id"].(string)}); err != nil {
		t.Fatal(err)
	}
	recovered, err := selected.RememberCandidate(ctx, request)
	if err != nil || !recovered.Saved || recovered.RecordID != committed.RecordID {
		t.Fatalf("lossless receipt recovery failed: %+v, %v", recovered, err)
	}
	if len(committed.ProvenanceEdges) != 2 || len(recovered.ProvenanceEdges) != 2 {
		t.Fatalf("recovered receipt lost additional raw witness provenance: %+v, %+v",
			committed.ProvenanceEdges, recovered.ProvenanceEdges)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("receipt recovery duplicated verifier event: %v", err)
	}
}

func TestLosslessAtomicRecoversVerifierEventBeforeCanonicalCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "turn-before-record", "exact", "trusted-host", "", "",
		HostLineage{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	request := CandidateRequest{OperationID: "before-record", WriteMode: "lossless_atomic",
		Content: "exact", RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"source_segments": []any{map[string]any{"source_id": "turn-before-record",
			"source_sha256": source["source_sha256"], "byte_start": 0, "byte_end": 5, "text": "exact"}}}}
	proof, err := selected.prepareLossless(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	route, err := memory.RouteMemoryWrite(memory.WriteProposal{Content: request.Content,
		WriteMode: request.WriteMode, RequestedBlockID: request.RequestedBlockID,
		WorkspaceID: "alpha", RunID: request.RunID, Metadata: proof.Metadata}, selected.Blocks())
	if err != nil || !route.Accepted {
		t.Fatalf("route failed: %+v, %v", route, err)
	}
	beforeRefs, err := selected.losslessEvidence(ctx, request, proof, route, "observation")
	if err != nil {
		t.Fatal(err)
	}
	path, err := ledger.Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := selected.RememberCandidate(ctx, request)
	if err != nil || !result.Saved {
		t.Fatalf("record did not recover after verifier fsync: %+v, %v", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("retry duplicated verifier event: %v", err)
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil {
		t.Fatalf("recovered record missing: %v", err)
	}
	if record.EvidenceRefs[len(record.EvidenceRefs)-1].RefID != beforeRefs[len(beforeRefs)-1].RefID {
		t.Fatal("record did not reuse the fsynced verifier event")
	}
}

func TestRetractedRawWitnessCannotVerifyNewLosslessRecord(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	lineage := HostLineage{SessionID: "session-1"}
	source, err := selected.RegisterSource(ctx, "turn-retracted", "exact", "trusted-host", "", "", lineage)
	if err != nil {
		t.Fatal(err)
	}
	witnessID := source["witness_id"].(string)
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: witnessID, Reason: "withdraw"}); err != nil {
		t.Fatal(err)
	}
	request := CandidateRequest{WriteMode: "lossless_atomic", Content: "exact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"source_segments": []any{map[string]any{"source_id": "turn-retracted",
			"source_sha256": source["source_sha256"], "byte_start": 0, "byte_end": 5, "text": "exact"}}}}
	if _, err := selected.RememberCandidate(ctx, request); err == nil {
		t.Fatal("retracted witness still verified a new record")
	}
	if _, err := selected.RegisterSource(ctx, "turn-retracted", "exact", "trusted-host", "", "", lineage); err == nil {
		t.Fatal("retracted source registration still replayed as current")
	}
	coverage, err := selected.SourceCoverage(ctx, []string{"turn-retracted"})
	if err != nil || coverage["source_bytes"] != nil {
		t.Fatalf("retracted source remained registered: %+v, %v", coverage, err)
	}
}

func TestGenericEvidenceCannotCrossRawWitnessScope(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "turn-a", "exact", "host:a", "", "",
		HostLineage{SessionID: "session-a", ProjectID: "project-a"})
	if err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + source["source_sha256"].(string)
	ref := memory.EvidenceRef{RefType: "artifact", RefID: source["witness_id"].(string),
		Hash: &hash, ProducedBy: "watcher", ProducerID: "host:a"}
	for _, lineage := range []struct{ session, project string }{
		{"session-b", "project-a"}, {"session-a", "project-b"},
	} {
		_, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "derived",
			RequestedBlockID: "stable_project_facts", SessionID: lineage.session,
			ProjectID: lineage.project, RunID: lineage.session, EvidenceRefs: []memory.EvidenceRef{ref}})
		if err == nil {
			t.Fatalf("raw witness crossed scope %+v", lineage)
		}
	}
	valid, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "derived",
		RequestedBlockID: "stable_project_facts", SessionID: "session-a",
		ProjectID: "project-a", RunID: "session-a", EvidenceRefs: []memory.EvidenceRef{ref}})
	if err != nil || !valid.Saved || len(valid.ProvenanceEdges) != 1 {
		t.Fatalf("same-scope raw witness provenance failed: %+v, %v", valid, err)
	}
}

func TestRetractionBeforePublicationCannotCommitBoundRecord(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "turn-pending", "bound", "host:a", "", "",
		HostLineage{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	request := CandidateRequest{WriteMode: "lossless_atomic", Content: "bound",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"source_segments": []any{map[string]any{"source_id": "turn-pending",
			"source_sha256": source["source_sha256"], "byte_start": 0, "byte_end": 5, "text": "bound"}}}}
	projector := selected.projector
	selected.projector = failingForgetProjector{}
	if _, err := selected.RememberCandidate(ctx, request); err == nil {
		t.Fatal("projection failure did not leave a staged bound record")
	}
	selected.projector = projector
	rows, err := selected.index.ProjectionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var pendingRevision int64
	var pendingID string
	for _, row := range rows {
		if row.Status == "pending" && row.Record.Content == "bound" {
			pendingRevision, pendingID = row.Revision, row.Record.BlockID
		}
	}
	if pendingRevision == 0 {
		t.Fatal("bound record has no pending projection")
	}
	if _, err := selected.Forget(ctx, ForgetRequest{MemoryID: source["witness_id"].(string)}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.writer.CompleteProjection(ctx, pendingRevision, projector); err == nil {
		t.Fatal("staged accepted record committed after its witness was retracted")
	}
	stored, err := selected.ReadStored(ctx, pendingID)
	if err != nil || stored != nil {
		t.Fatalf("retracted witness produced a visible accepted record: %+v, %v", stored, err)
	}
}

func TestConcurrentRetractionNeverPublishesUnlinkedAcceptedRecord(t *testing.T) {
	ctx := context.Background()
	for attempt := 0; attempt < 6; attempt++ {
		selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
			ProfileID: "team", BackendID: "mnemosyne"})
		if err != nil {
			t.Fatal(err)
		}
		source, err := selected.RegisterSource(ctx, "race-turn", "race-bound", "host:a", "", "",
			HostLineage{SessionID: "session-1"})
		if err != nil {
			selected.Close()
			t.Fatal(err)
		}
		request := CandidateRequest{WriteMode: "lossless_atomic", Content: "race-bound",
			RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
			Metadata: map[string]any{"source_segments": []any{map[string]any{"source_id": "race-turn",
				"source_sha256": source["source_sha256"], "byte_start": 0, "byte_end": 10,
				"text": "race-bound"}}}}
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var forgetErr error
		go func() {
			defer group.Done()
			<-start
			_, forgetErr = selected.Forget(ctx, ForgetRequest{MemoryID: source["witness_id"].(string)})
		}()
		go func() {
			defer group.Done()
			<-start
			_, _ = selected.RememberCandidate(ctx, request)
		}()
		close(start)
		group.Wait()
		if forgetErr != nil {
			selected.Close()
			t.Fatalf("concurrent witness retraction failed: %v", forgetErr)
		}
		rows, err := selected.index.ProjectionRows(ctx)
		if err != nil {
			selected.Close()
			t.Fatal(err)
		}
		_, edges, _, err := selected.index.ExportGraphSnapshot(ctx)
		if err != nil {
			selected.Close()
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Status != "committed" || row.Record.Content != "race-bound" ||
				row.Record.Status != "accepted" {
				continue
			}
			linked := false
			for _, edge := range edges {
				if edge.SrcID == row.Record.BlockID && edge.DstID == source["witness_id"] &&
					edge.EdgeType == "derived_from" {
					linked = true
				}
			}
			if !linked {
				selected.Close()
				t.Fatal("concurrent retraction left a committed accepted record without provenance")
			}
		}
		if err := selected.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLosslessRecoveryCannotAdoptAnotherRolesSourceProof(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	source, err := selected.RegisterSource(ctx, "role-a-turn", "exact", "host:a", "", "",
		HostLineage{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	base := CandidateRequest{OperationID: "shared-operation", WriteMode: "lossless_atomic",
		Content: "exact", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"source_segments": []any{map[string]any{"source_id": "role-a-turn",
			"source_sha256": source["source_sha256"], "byte_start": 0,
			"byte_end": 5, "text": "exact"}}}}
	first, err := selected.RememberCandidateFor(ctx, base, "go_cli", 2)
	if err != nil || !first.Saved {
		t.Fatalf("original bound operation failed: %+v, %v", first, err)
	}
	foreign := base
	foreign.Metadata = map[string]any{"source_segments": []any{map[string]any{
		"source_id": "unregistered", "text": "exact"}}}
	for attempt := 0; attempt < 2; attempt++ {
		if result, err := selected.RememberCandidateFor(ctx, foreign, "mcp_direct", 2); err == nil || result.Saved {
			t.Fatalf("foreign role adopted another source proof on attempt %d: %+v, %v",
				attempt, result, err)
		}
	}
}

func TestHostTestObservationAppendsReusableVerificationReport(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	project := t.TempDir()
	if err := os.WriteFile(project+string(os.PathSeparator)+"result.txt", []byte("passed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := Open(Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	observation := HostObservation{EventID: "test-result-1", EventKind: "test_passed",
		RawEvidence: "1 passed", AtomicClaim: "The regression test passed.",
		ObservedAt: "2026-10-23T12:00:00Z", ProducerID: "trusted-host",
		WorkspaceRoot: project, ChangedFiles: []string{"result.txt"}}
	first, err := selected.ObserveHost(ctx, observation, HostLineage{SessionID: "session-1"})
	if err != nil || first["memory_id"] != "" || first["route_outcome"] != "needs_agent_choice" {
		t.Fatalf("test observation routed incorrectly: %+v, %v", first, err)
	}
	report, ok := first["verification_report"].(ledger.VerificationReport)
	if !ok || report.ReportID == "" || !report.Passed || report.WorkspaceHash == "" || report.DiffHash == "" {
		t.Fatalf("test observation lacked verifier report: %+v", first)
	}
	second, err := selected.ObserveHost(ctx, observation, HostLineage{SessionID: "session-1"})
	if err != nil || second["verification_report"] != report || second["witness_id"] != first["witness_id"] {
		t.Fatalf("test observation retry changed proof: %+v, %v", second, err)
	}
	path, err := ledger.Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("test report was duplicated: %v, %q", err, data)
	}
	failed := observation
	failed.EventID, failed.EventKind = "test-result-2", "test_failed"
	failed.RawEvidence = "1 failed"
	failedReport, err := selected.ObserveHost(ctx, failed, HostLineage{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	if failedReport["verification_report"].(ledger.VerificationReport).Passed {
		t.Fatal("failed test observation was reported as passed")
	}
}
