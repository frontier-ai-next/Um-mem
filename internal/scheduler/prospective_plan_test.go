package scheduler

import (
	"testing"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/prospective"
)

func TestProspectivePlanMatchesPythonDecisionIdentity(t *testing.T) {
	event := HostStateEvent{EventID: "codex-host-state:abc", EventType: "prospective_trigger",
		WorkspaceID: "alpha", RunID: "codex-session:abc", StageID: "codex-turn:def",
		StateRevision: "policy-9", OccurredAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	plan, err := ProspectivePlan(event, []TriggerCandidate{
		{ObligationID: "obl-1", Matched: true, MatchedCues: []string{"cue-b", "cue-a"}},
		{ObligationID: "obl-2", Matched: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.DecisionRevision != "policy-9:host-state:8fe6f616bbe58e37117e6fb91a7642a17c7302c49350c6da26e8b4be69842bf6" ||
		plan.ActivationReasonCode != "prospective_match" || len(plan.MatchedRuleSHA256s) != 1 ||
		plan.MatchedRuleSHA256s[0] != "095480698b81321ad0bd9dd6b1cf2b97c956e230ffc9373dcc1dd0b00b0470a3" ||
		len(plan.MatchedActionSHA256s) != 2 ||
		plan.MatchedActionSHA256s[0] != "72ec16a012722c0d89d964193c45c626c72b49c1ed8418b7a4c5f55385b00aaf" ||
		plan.MatchedActionSHA256s[1] != "c63706bbaa0ed5d52236bbb10f1864a5d00e67556b882bf62481893eebf95aeb" {
		t.Fatalf("prospective decision identity differs from Python: %+v", plan)
	}
	record, err := plan.Record()
	if err != nil || record["plan_sha256"] != "828078197b5213cc4bcf778c2c9d3371381ac7f0d9d5deeea9afd27cb8036129" {
		t.Fatalf("prospective plan fingerprint differs from Python: %+v, %v", record, err)
	}
	noMatch, err := ProspectivePlan(event, nil)
	if err != nil || noMatch.ActivationReasonCode != "prospective_no_match" || len(noMatch.MatchedRuleSHA256s) != 0 {
		t.Fatalf("no-match plan is invalid: %+v, %v", noMatch, err)
	}
}

func TestPlanFromObligationsUsesTypedHostStateOnly(t *testing.T) {
	event := HostStateEvent{EventID: "codex-host-state:abc", EventType: "prospective_trigger",
		WorkspaceID: "alpha", RunID: "run:1", StageID: "turn:1", StateRevision: "policy-9",
		OccurredAt:  time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		SignalCodes: []string{"codex_event:PreToolUse"}}
	obligations := []prospective.Obligation{{ID: "obl-1", Status: "armed", Scope: "run", ScopeID: "run:1",
		Trigger: []prospective.Predicate{{Type: "registered_condition", Field: "signals", Operator: "contains",
			Value: "codex_event:PreToolUse"}}},
		{ID: "foreign", Status: "armed", Scope: "run", ScopeID: "run:2",
			Trigger: []prospective.Predicate{{Type: "host_event", Field: "event_type", Operator: "eq",
				Value: "prospective_trigger"}}}}
	plan, err := PlanFromObligations(event, obligations)
	if err != nil || plan.ActivationReasonCode != "prospective_match" || len(plan.MatchedRuleSHA256s) != 1 ||
		plan.MatchedRuleSHA256s[0] != evidenceDigest("prospective-obligation", "obl-1") {
		t.Fatalf("host-state rule evaluation is wrong: %+v, %v", plan, err)
	}
}
