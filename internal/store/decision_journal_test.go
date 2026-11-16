package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func testDecisionRef(id string) memory.EvidenceRef {
	hash := strings.Repeat("a", 64)
	return memory.EvidenceRef{RefType: "test_run", RefID: id, Hash: &hash,
		ProducedBy: "verifier", ProducerID: "verifier:" + id}
}

func testDecision() memory.MemoryDecisionEvent {
	return memory.MemoryDecisionEvent{DecisionID: "decision-1", WorkspaceID: "ws", RunID: "run-1",
		TaskSignature: "repair:python:unit-test", ActionTraceID: "action-trace-1", Action: "recall",
		CanonicalSnapshotSHA256: "sha256:" + strings.Repeat("a", 64),
		ContextSnapshotSHA256:   "sha256:" + strings.Repeat("b", 64),
		LiveStateSnapshotSHA256: "sha256:" + strings.Repeat("c", 64),
		LiveTrigger:             "test_state:required_suite_failed", CandidateIDs: []string{"memory-1", "memory-2"},
		SelectedCandidateIDs: []string{"memory-1"}, PolicyRevision: "policy-7", CatalogRevision: "catalog-11",
		CostTokens: 321, CostLatencyMS: 12, DownstreamOutcome: "verification_pending",
		EvidenceRefs: []memory.EvidenceRef{testDecisionRef("decision-proof")}}
}

func testUsage(kind string, sequence int, memoryID string) memory.MemoryUsageEvent {
	return memory.MemoryUsageEvent{EventID: "usage-" + kind, DecisionID: "decision-1", MemoryID: memoryID,
		WorkspaceID: "ws", TaskSignature: "repair:python:unit-test", ActionTraceID: "action-trace-1",
		UsageType: kind, Sequence: sequence, EvidenceRefs: []memory.EvidenceRef{testDecisionRef("usage-" + kind)}}
}

func TestDecisionUsageJournalRoundTripAndFences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "ws", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "ws", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	decision := testDecision()
	if _, err := writer.RecordMemoryUsageEvent(ctx, testUsage("candidate", 0, "memory-1")); err == nil {
		t.Fatal("usage without decision accepted")
	}
	created, err := writer.RecordMemoryDecision(ctx, decision)
	if err != nil || created.Replayed || created.DecisionID != decision.DecisionID {
		t.Fatalf("decision: %+v %v", created, err)
	}
	replay, err := writer.RecordMemoryDecision(ctx, decision)
	if err != nil || !replay.Replayed {
		t.Fatalf("decision replay: %+v %v", replay, err)
	}
	changed := decision
	changed.DownstreamOutcome = "different"
	if _, err := writer.RecordMemoryDecision(ctx, changed); err == nil {
		t.Fatal("changed decision replay accepted")
	}
	foreign := decision
	foreign.DecisionID, foreign.WorkspaceID = "foreign", "other"
	if _, err := writer.RecordMemoryDecision(ctx, foreign); err == nil {
		t.Fatal("foreign workspace decision accepted")
	}
	wrongTask := testUsage("candidate", 0, "memory-1")
	wrongTask.TaskSignature = "wrong"
	if _, err := writer.RecordMemoryUsageEvent(ctx, wrongTask); err == nil {
		t.Fatal("usage with wrong task signature accepted")
	}
	unknown := testUsage("candidate", 0, "not-a-candidate")
	if _, err := writer.RecordMemoryUsageEvent(ctx, unknown); err == nil {
		t.Fatal("unknown candidate accepted")
	}
	unselected := testUsage("applied", 0, "memory-2")
	if _, err := writer.RecordMemoryUsageEvent(ctx, unselected); err == nil {
		t.Fatal("unselected applied memory accepted")
	}
	sequence := []string{"candidate", "selected", "rendered", "cited", "applied"}
	for position, kind := range sequence {
		result, err := writer.RecordMemoryUsageEvent(ctx, testUsage(kind, position, "memory-1"))
		if err != nil || result.Replayed {
			t.Fatalf("usage %s: %+v %v", kind, result, err)
		}
	}
	again, err := writer.RecordMemoryUsageEvent(ctx, testUsage("candidate", 0, "memory-1"))
	if err != nil || !again.Replayed {
		t.Fatalf("usage replay: %+v %v", again, err)
	}
	sameSequence := testUsage("candidate", 0, "memory-1")
	sameSequence.EventID = "another-event"
	if _, err := writer.RecordMemoryUsageEvent(ctx, sameSequence); err == nil {
		t.Fatal("duplicate sequence accepted")
	}
	contradiction := testUsage("not_applied", 5, "memory-1")
	if _, err := writer.RecordMemoryUsageEvent(ctx, contradiction); err == nil {
		t.Fatal("contradictory action observation accepted")
	}
	blocked := testUsage("blocked", 5, "memory-2")
	if _, err := writer.RecordMemoryUsageEvent(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	credit := memory.OutcomeCreditEvent{CreditID: "credit-1", DecisionID: decision.DecisionID, WorkspaceID: "ws",
		TaskSignature: decision.TaskSignature, ActionTraceID: decision.ActionTraceID,
		MemoryIDs: []string{"memory-1"}, AppliedUsageEventIDs: []string{"usage-applied"},
		Outcome: "helped", AttributionBasis: "deterministic_verifier", VerifierResult: "passed",
		TaskFamily: "repair", Stage: "test", ProjectID: "project-1", HostID: "host-1", ProfileID: "team",
		IndependenceDomain: "verifier-1", EvidenceRefs: []memory.EvidenceRef{testDecisionRef("credit-proof")},
		Reason: "verified test result"}
	credited, err := writer.RecordOutcomeCredit(ctx, credit)
	if err != nil || credited.CreditID != "credit-1" || credited.Replayed {
		t.Fatalf("credit: %+v %v", credited, err)
	}
	creditReplay, err := writer.RecordOutcomeCredit(ctx, credit)
	if err != nil || !creditReplay.Replayed {
		t.Fatalf("credit replay: %+v %v", creditReplay, err)
	}
	changedCredit := credit
	changedCredit.Reason = "changed"
	if _, err := writer.RecordOutcomeCredit(ctx, changedCredit); err == nil {
		t.Fatal("changed credit replay accepted")
	}
	duplicateCredit := credit
	duplicateCredit.CreditID = "credit-duplicate"
	if _, err := writer.RecordOutcomeCredit(ctx, duplicateCredit); err == nil {
		t.Fatal("duplicate applied usage credit accepted")
	}
	neutral := credit
	neutral.CreditID = "credit-neutral"
	neutral.MemoryIDs = []string{"memory-2"}
	neutral.AppliedUsageEventIDs = []string{}
	neutral.Outcome = "neutral"
	neutral.AttributionBasis = "insufficient_linkage"
	neutral.VerifierResult = ""
	neutral.Reason = "no verified outcome"
	if _, err := writer.RecordOutcomeCredit(ctx, neutral); err != nil {
		t.Fatal(err)
	}
	credits, err := index.OutcomeCredits(ctx, decision.DecisionID)
	if err != nil || len(credits) != 2 || credits[0].CreditID != "credit-1" || credits[1].CreditID != "credit-neutral" {
		t.Fatalf("credits: %+v %v", credits, err)
	}
	byMemory, err := index.OutcomeCreditsForMemory(ctx, "memory-1")
	if err != nil || len(byMemory) != 1 || byMemory[0].CreditID != "credit-1" {
		t.Fatalf("credits by memory: %+v %v", byMemory, err)
	}
	trace, err := index.MemoryDecisionTrace(ctx, decision.DecisionID)
	if err != nil || len(trace.UsageEvents) != 6 || trace.Channel != "memory_decision_usage_trace" ||
		trace.OrdinaryRecall || trace.HostActionAuthorized || trace.UsageEvents[5].UsageType != "blocked" {
		t.Fatalf("decision trace: %+v %v", trace, err)
	}
	report, err := memory.BuildMemoryOutcomeReport("operator-report:decision-1", trace, credits)
	if err != nil || report.Entries[0].Disposition != "helped" || report.Entries[1].Disposition != "unknown" {
		t.Fatalf("neutral credit inferred a causal outcome: %+v %v", report, err)
	}
	if _, err := index.GetMemoryDecision(ctx, "missing"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := writer.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage WHERE workspace_id='ws'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 9 {
		t.Fatalf("journal count %d", count)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE usage SET payload_json=? WHERE block_id=?`,
		`{"payload":{"decision_id":"decision-1"},"payload_sha256":"bad"}`, "memory-decision-event:decision-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := index.MemoryDecisionTrace(ctx, decision.DecisionID); err == nil {
		t.Fatal("forged decision hash accepted")
	}
}

func TestDecisionEnvelopeUsesPythonCanonicalShape(t *testing.T) {
	event := testDecision()
	event.CreatedAt = "2026-10-24T12:00:00Z"
	payload, err := event.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	envelope := decisionEnvelope(payload)
	decoded, err := decodedDecision(envelope)
	if err != nil || decoded.CreatedAt != event.CreatedAt || decoded.CostTokens != 321 {
		t.Fatalf("envelope: %+v %v", decoded, err)
	}
	if !strings.HasPrefix(envelope, `{"payload":{"action":"recall"`) {
		t.Fatalf("envelope key order: %s", envelope)
	}
}
