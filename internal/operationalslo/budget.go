package operationalslo

import (
	"errors"
	"math"
)

type RecallBudget struct {
	MaxLatencyMS  int `json:"max_latency_ms"`
	MaxSteps      int `json:"max_steps"`
	MaxTokens     int `json:"max_tokens"`
	MaxCandidates int `json:"max_candidates"`
}

type RecallUsage struct {
	LatencyMS  float64 `json:"latency_ms"`
	Steps      int     `json:"steps"`
	Tokens     int     `json:"tokens"`
	Candidates int     `json:"candidates"`
}

type RecallBudgetDecision struct {
	Action              string  `json:"action"`
	Reason              string  `json:"reason"`
	RemainingLatencyMS  float64 `json:"remaining_latency_ms"`
	RemainingSteps      int     `json:"remaining_steps"`
	RemainingTokens     int     `json:"remaining_tokens"`
	RemainingCandidates int     `json:"remaining_candidates"`
}

var DefaultOneHopRecallBudget = RecallBudget{MaxLatencyMS: 1000, MaxSteps: 2, MaxTokens: 2048, MaxCandidates: 32}
var DefaultReconstructiveRecallBudget = RecallBudget{MaxLatencyMS: 3000, MaxSteps: 6, MaxTokens: 4096, MaxCandidates: 64}

func validUsage(usage RecallUsage) bool {
	return usage.LatencyMS >= 0 && !math.IsNaN(usage.LatencyMS) && !math.IsInf(usage.LatencyMS, 0) &&
		usage.Steps >= 0 && usage.Tokens >= 0 && usage.Candidates >= 0
}

func DecideRecallBudget(budget RecallBudget, usage RecallUsage, wideningCost *RecallUsage) (RecallBudgetDecision, error) {
	if budget.MaxLatencyMS <= 0 || budget.MaxSteps <= 0 || budget.MaxTokens <= 0 || budget.MaxCandidates <= 0 {
		return RecallBudgetDecision{}, errors.New("recall budget limits must be positive")
	}
	if !validUsage(usage) || wideningCost != nil && !validUsage(*wideningCost) {
		return RecallBudgetDecision{}, errors.New("recall usage must be finite and non-negative")
	}
	decision := RecallBudgetDecision{RemainingLatencyMS: float64(budget.MaxLatencyMS) - usage.LatencyMS,
		RemainingSteps: budget.MaxSteps - usage.Steps, RemainingTokens: budget.MaxTokens - usage.Tokens,
		RemainingCandidates: budget.MaxCandidates - usage.Candidates}
	if decision.RemainingLatencyMS < 0 || decision.RemainingSteps < 0 ||
		decision.RemainingTokens < 0 || decision.RemainingCandidates < 0 {
		decision.Action, decision.Reason = "abstain", "hard_budget_exhausted"
		return decision, nil
	}
	if wideningCost == nil {
		decision.Action, decision.Reason = "continue", "within_budget"
		return decision, nil
	}
	if decision.RemainingLatencyMS >= wideningCost.LatencyMS && decision.RemainingSteps >= wideningCost.Steps &&
		decision.RemainingTokens >= wideningCost.Tokens && decision.RemainingCandidates >= wideningCost.Candidates {
		decision.Action, decision.Reason = "widen", "bounded_widening_available"
	} else {
		decision.Action, decision.Reason = "abstain", "widening_budget_unavailable"
	}
	return decision, nil
}
