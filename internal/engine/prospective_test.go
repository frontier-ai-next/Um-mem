package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/prospective"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestProspectivePublicFireRequiresRevalidatedObservedTrigger(t *testing.T) {
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	ctx := context.Background()
	hash := strings.Repeat("a", 64)
	ref := memory.EvidenceRef{RefType: "session_event", RefID: "evt:1", Hash: &hash,
		ProducedBy: "harness", ProducerID: "host:1"}
	value := memory.ProspectiveObligation{ObligationID: "obl:1", Scope: "run", ScopeID: "run:1",
		EvidenceRefs: []memory.EvidenceRef{ref}, RequiredIntervention: "Review the tool call",
		Telemetry: memory.ProspectiveTelemetry{FalsePositiveCount: 2, LateTriggerCount: 1},
		TriggerPredicates: []memory.ProspectivePredicate{{PredicateType: "registered_condition", Field: "signals",
			Operator: "contains", Value: "codex_event:PreToolUse"}}}
	if _, err := selected.CreateProspectiveObligation(ctx, value, "evt:create", ""); err != nil {
		t.Fatal(err)
	}
	arm := memory.ProspectiveLifecycleEvent{EventID: "evt:arm", EventType: "arm", ExpectedVersion: 0,
		ExpectedStatus: "pending", EvidenceRefs: []memory.EvidenceRef{ref}}
	if _, err := selected.TransitionProspectiveObligation(ctx, value.ObligationID, arm); err != nil {
		t.Fatal(err)
	}
	fire := memory.ProspectiveLifecycleEvent{EventID: "evt:fire", EventType: "fire", ExpectedVersion: 1,
		ExpectedStatus: "armed", EvidenceRefs: []memory.EvidenceRef{ref}}
	if _, err := selected.TransitionProspectiveObligation(ctx, value.ObligationID, fire); err == nil {
		t.Fatal("direct fire was accepted")
	}
	if _, err := selected.ApplyProspectiveLifecycleEvent(ctx, value.ObligationID, fire, nil); err == nil {
		t.Fatal("fire without trigger context was accepted")
	}
	trigger := &prospective.FireContext{Trigger: prospective.Context{Scope: "run", ScopeID: "run:1",
		RegisteredCondition: map[string]any{"signals": "codex_event:PreToolUse"}},
		Origin:           &prospective.Origin{Channel: "host_event", PrincipalID: "host:1", IntegrityClass: "reported"},
		AuthorityCeiling: "advisory"}
	if _, err := selected.ApplyProspectiveLifecycleEvent(ctx, value.ObligationID, fire, trigger); err == nil {
		t.Fatal("reported origin was accepted")
	}
	trigger.Origin.IntegrityClass = "observed"
	trigger.AuthorityCeiling = "directive"
	if _, err := selected.ApplyProspectiveLifecycleEvent(ctx, value.ObligationID, fire, trigger); err == nil {
		t.Fatal("directive authority ceiling was accepted")
	}
	trigger.AuthorityCeiling = "advisory"
	result, err := selected.ApplyProspectiveLifecycleEvent(ctx, value.ObligationID, fire, trigger)
	if err != nil || result.Status != "fired" || result.Version != 2 {
		t.Fatalf("valid fire: %+v %v", result, err)
	}
	intervention, err := selected.ProspectiveIntervention(ctx, value.ObligationID)
	if err != nil || intervention != "Review the tool call" {
		t.Fatalf("intervention: %q %v", intervention, err)
	}
	report, err := selected.ProspectiveTelemetryReport(ctx, 256)
	if err != nil || report.Channel != "prospective_obligation_telemetry" || report.OrdinaryRecall || report.HostActionAuthorized ||
		len(report.Entries) != 1 || report.Entries[0].FalseActivationCount != 2 || report.Entries[0].LateTriggerCount != 1 {
		t.Fatalf("telemetry report: %+v %v", report, err)
	}
	decisions, err := selected.ProspectiveTriggerCandidates(ctx, trigger.Trigger, 50)
	if err != nil || len(decisions) != 1 || decisions[0].Reason != "status_fired_not_armed" {
		t.Fatalf("candidates: %+v %v", decisions, err)
	}
}
