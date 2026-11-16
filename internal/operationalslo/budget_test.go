package operationalslo

import (
	"math"
	"testing"
)

func TestRecallBudgetBoundsWideningAndAbstainsAtLimit(t *testing.T) {
	budget := RecallBudget{MaxLatencyMS: 1000, MaxSteps: 4, MaxTokens: 1000, MaxCandidates: 20}
	usage := RecallUsage{LatencyMS: 300, Steps: 1, Tokens: 300, Candidates: 4}
	decide := func(widening *RecallUsage) RecallBudgetDecision {
		t.Helper()
		decision, err := DecideRecallBudget(budget, usage, widening)
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}
	if decision := decide(nil); decision.Action != "continue" || decision.RemainingLatencyMS != 700 {
		t.Fatalf("unbounded continuation: %+v", decision)
	}
	if decision := decide(&RecallUsage{LatencyMS: 700, Steps: 3, Tokens: 700, Candidates: 16}); decision.Action != "widen" {
		t.Fatalf("exactly fitting widening was refused: %+v", decision)
	}
	if decision := decide(&RecallUsage{LatencyMS: 701}); decision.Action != "abstain" || decision.Reason != "widening_budget_unavailable" {
		t.Fatalf("over-budget widening was allowed: %+v", decision)
	}
	usage.Steps = 5
	if decision := decide(nil); decision.Action != "abstain" || decision.Reason != "hard_budget_exhausted" {
		t.Fatalf("hard budget exhaustion was hidden: %+v", decision)
	}
	usage.LatencyMS = math.NaN()
	if _, err := DecideRecallBudget(budget, usage, nil); err == nil {
		t.Fatal("non-finite usage was accepted")
	}
}
