package prospective

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTypedHostStateRuleMatchesPython(t *testing.T) {
	obligation := Obligation{ID: "obl-1", Scope: "run", ScopeID: "run:1", Status: "armed",
		Trigger: []Predicate{{Type: "registered_condition", Field: "signals", Operator: "contains",
			Value: "codex_event:PreToolUse"}}}
	context := Context{Scope: "run", ScopeID: "run:1", ObservedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC),
		RegisteredCondition: map[string]any{"signals": []string{"codex_event:PreToolUse"}}}
	decision, err := Evaluate(obligation, context)
	if err != nil || !decision.Matched || decision.MatchKind != "typed" ||
		decision.Reason != "typed_predicates_matched" || decision.TriggerType != "registered_condition" ||
		decision.CandidateClass != "release" || !decision.Advertised || !decision.DeliveryEligible ||
		decision.RecommendedTransition != "" || decision.HostActionAuthorized {
		t.Fatalf("typed decision differs from Python: %+v, %v", decision, err)
	}
	context.ScopeID = "foreign-run"
	decision, err = Evaluate(obligation, context)
	if err != nil || decision.Matched || decision.Reason != "scope_mismatch" {
		t.Fatalf("foreign run matched rule: %+v, %v", decision, err)
	}
}

func TestPredicateSupportsPythonSizedIntegers(t *testing.T) {
	large := json.Number("999999999999999999999999999999999999999999")
	context := Context{ToolState: map[string]any{"count": large}}
	if !PredicateMatches(Predicate{Type: "tool_state", Field: "count", Operator: "eq", Value: large}, context) ||
		!PredicateMatches(Predicate{Type: "tool_state", Field: "count", Operator: "gte", Value: json.Number("999999999999999999999999999999999999999998")}, context) ||
		PredicateMatches(Predicate{Type: "tool_state", Field: "count", Operator: "lte", Value: json.Number("999999999999999999999999999999999999999998")}, context) {
		t.Fatal("arbitrary-size Python integer comparison changed")
	}
}

func TestRulePrecedenceAndAdvisorySemanticCandidate(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	context := Context{Scope: "workspace", ObservedAt: now,
		RegisteredCondition: map[string]any{"signals": []string{"ready"}},
		SemanticText:        "Review the ALPHA-BETA deployment"}
	obligation := Obligation{ID: "obl", Scope: "workspace", Status: "armed",
		Trigger:      []Predicate{{Type: "registered_condition", Field: "signals", Operator: "contains", Value: "ready"}},
		Suppress:     []Predicate{{Type: "registered_condition", Field: "signals", Operator: "contains", Value: "ready"}},
		SemanticCues: []string{"alpha-beta"}}
	decision, err := Evaluate(obligation, context)
	if err != nil || decision.Matched || decision.Reason != "suppressed_by_current_state" {
		t.Fatalf("suppression lost precedence: %+v, %v", decision, err)
	}
	obligation.Suppress = nil
	obligation.Trigger = nil
	decision, err = Evaluate(obligation, context)
	if err != nil || !decision.Matched || decision.MatchKind != "semantic" || decision.CandidateClass != "experimental" ||
		decision.Advertised || decision.DeliveryEligible || len(decision.MatchedCues) != 1 {
		t.Fatalf("semantic candidate crossed typed gate: %+v, %v", decision, err)
	}
	obligation.ExpiresAt = "2026-10-24T12:00:00+00:00"
	decision, err = Evaluate(obligation, context)
	if err != nil || !decision.Matched || decision.MatchKind != "lifecycle" ||
		decision.RecommendedTransition != "expired" || decision.DeliveryEligible {
		t.Fatalf("expiry did not precede semantic match: %+v, %v", decision, err)
	}
}

func TestPredicateTypeAndTimeBoundaries(t *testing.T) {
	context := Context{ToolState: map[string]any{"count": 2, "ready": true,
		"deadline": "2026-10-24T12:00:00+00:00"}}
	if PredicateMatches(Predicate{Type: "tool_state", Field: "count", Operator: "eq", Value: true}, context) ||
		!PredicateMatches(Predicate{Type: "tool_state", Field: "count", Operator: "gte", Value: 2}, context) ||
		!PredicateMatches(Predicate{Type: "tool_state", Field: "deadline", Operator: "lte",
			Value: "2026-10-24T12:00:01+00:00"}, context) ||
		!PredicateMatches(Predicate{Type: "tool_state", Field: "missing", Operator: "exists", Value: false}, context) ||
		!PredicateMatches(Predicate{Type: "tool_state", Field: "ready", Operator: "exists", Value: "yes"}, context) {
		t.Fatal("typed predicate boundary differs from Python")
	}
}
