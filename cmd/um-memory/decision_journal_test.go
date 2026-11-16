package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestDecisionUsageCreditCLIJourney(t *testing.T) {
	repo := t.TempDir()
	requestFile := filepath.Join(t.TempDir(), "request.json")
	base := []string{"--repo-root", repo, "--workspace", "ws", "--profile", "team", "--backend", "hindsight"}
	call := func(command string, request any, flags ...string) ([]byte, error) {
		t.Helper()
		args := append([]string{command}, base...)
		if request != nil {
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(requestFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--request-file", requestFile)
		}
		args = append(args, flags...)
		var output bytes.Buffer
		err := run(context.Background(), args, &output)
		return output.Bytes(), err
	}
	hash := strings.Repeat("a", 64)
	ref := memory.EvidenceRef{RefType: "test_run", RefID: "test-1", Hash: &hash,
		ProducedBy: "verifier", ProducerID: "verifier-1"}
	decision := memory.MemoryDecisionEvent{DecisionID: "decision-cli", WorkspaceID: "ws", RunID: "run-1",
		TaskSignature: "repair:test", ActionTraceID: "trace-1", Action: "recall",
		CanonicalSnapshotSHA256: "sha256:" + hash, ContextSnapshotSHA256: "sha256:" + strings.Repeat("b", 64),
		LiveStateSnapshotSHA256: "sha256:" + strings.Repeat("c", 64), LiveTrigger: "test_state:failed",
		CandidateIDs: []string{"memory-1"}, SelectedCandidateIDs: []string{"memory-1"},
		PolicyRevision: "policy-1", CatalogRevision: "catalog-1", CostTokens: 3, CostLatencyMS: 5,
		DownstreamOutcome: "verification_pending", EvidenceRefs: []memory.EvidenceRef{ref}}
	if _, err := call("record-decision", decision); err != nil {
		t.Fatal(err)
	}
	usage := memory.MemoryUsageEvent{EventID: "usage-cli", DecisionID: decision.DecisionID, MemoryID: "memory-1",
		WorkspaceID: "ws", TaskSignature: decision.TaskSignature, ActionTraceID: decision.ActionTraceID,
		UsageType: "applied", Sequence: 0, EvidenceRefs: []memory.EvidenceRef{ref}}
	if _, err := call("record-usage", usage); err != nil {
		t.Fatal(err)
	}
	credit := memory.OutcomeCreditEvent{CreditID: "credit-cli", DecisionID: decision.DecisionID,
		WorkspaceID: "ws", TaskSignature: decision.TaskSignature, ActionTraceID: decision.ActionTraceID,
		MemoryIDs: []string{"memory-1"}, AppliedUsageEventIDs: []string{"usage-cli"}, Outcome: "helped",
		AttributionBasis: "deterministic_verifier", VerifierResult: "passed", TaskFamily: "repair", Stage: "test",
		ProjectID: "project-1", HostID: "host-1", ProfileID: "team", IndependenceDomain: "verifier-1",
		EvidenceRefs: []memory.EvidenceRef{ref}, Reason: "passed test"}
	if _, err := call("record-outcome-credit", credit); err != nil {
		t.Fatal(err)
	}
	raw, err := call("decision-trace", nil, "--decision-id", decision.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	var trace memory.MemoryDecisionTrace
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Channel != "memory_decision_usage_trace" || len(trace.UsageEvents) != 1 ||
		trace.UsageEvents[0].UsageType != "applied" || trace.HostActionAuthorized {
		t.Fatalf("CLI trace: %+v", trace)
	}
	raw, err = call("outcome-credits", nil, "--decision-id", decision.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	var credits []memory.OutcomeCreditEvent
	if err := json.Unmarshal(raw, &credits); err != nil {
		t.Fatal(err)
	}
	if len(credits) != 1 || credits[0].CreditID != "credit-cli" {
		t.Fatalf("CLI credits: %+v", credits)
	}
	raw, err = call("operator-report", nil, "--decision-id", decision.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	var report memory.MemoryOutcomeReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.Channel != "memory_outcome_operator_report" || report.HostActionAuthorized ||
		len(report.Entries) != 1 || report.Entries[0].Disposition != "helped" ||
		report.Entries[0].Reason != "passed test" {
		t.Fatalf("CLI operator report: %+v", report)
	}
}
