package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/prospective"
)

// TriggerCandidate is the already evaluated result of a prospective rule.
// Evaluation is separate from durable scheduling, which records evidence and
// never authorizes host execution.
type TriggerCandidate struct {
	ObligationID string
	Matched      bool
	MatchedCues  []string
}

func evidenceDigest(domain, value string) string {
	sum := sha256.Sum256([]byte(domain + ":" + value))
	return hex.EncodeToString(sum[:])
}

// ProspectivePlan matches the listener's decision-plan identity and reason
// codes. The caller must provide the verified rule results for this event.
func ProspectivePlan(event HostStateEvent, candidates []TriggerCandidate) (AdvisoryDecisionPlan, error) {
	event, err := event.Normalized()
	if err != nil {
		return AdvisoryDecisionPlan{}, err
	}
	rules := map[string]bool{}
	actions := map[string]bool{}
	for _, candidate := range candidates {
		if !candidate.Matched {
			continue
		}
		if strings.TrimSpace(candidate.ObligationID) == "" {
			return AdvisoryDecisionPlan{}, errors.New("prospective obligation ID is empty")
		}
		rules[evidenceDigest("prospective-obligation", candidate.ObligationID)] = true
		for _, cue := range candidate.MatchedCues {
			if cue == "" {
				return AdvisoryDecisionPlan{}, errors.New("matched cue is empty")
			}
			actions[evidenceDigest("prospective-cue", cue)] = true
		}
	}
	matchedRules := make([]string, 0, len(rules))
	for digest := range rules {
		matchedRules = append(matchedRules, digest)
	}
	sort.Strings(matchedRules)
	matchedActions := make([]string, 0, len(actions))
	for digest := range actions {
		matchedActions = append(matchedActions, digest)
	}
	sort.Strings(matchedActions)
	if len(matchedRules) > 64 || len(matchedActions) > 64 {
		return AdvisoryDecisionPlan{}, errors.New("prospective match set exceeds scheduler limit")
	}
	encoded, err := jsoncompat.MarshalPython(map[string]any{
		"policy_bundle_revision":      event.StateRevision,
		"matched_rule_sha256s":        matchedRules,
		"matched_action_sha256s":      matchedActions,
		"deferred_trigger_owner_refs": []string{},
	}, false)
	if err != nil {
		return AdvisoryDecisionPlan{}, err
	}
	activationReason := "prospective_no_match"
	if len(matchedRules) > 0 {
		activationReason = "prospective_match"
	}
	return AdvisoryDecisionPlan{
		DecisionRevision: event.StateRevision + ":host-state:" + hash(string(encoded)),
		ActivationDueAt:  event.OccurredAt, ActivationReasonCode: activationReason,
		ConsolidationDueAt: event.OccurredAt, ConsolidationReasonCode: "consolidation_assessment",
		ConsolidationDisposition: "assess", MatchedRuleSHA256s: matchedRules,
		MatchedActionSHA256s: matchedActions,
	}, nil
}

// PlanFromObligations evaluates loaded obligations against only the typed
// event fields admitted by the host-state protocol. The caller is responsible
// for selecting obligations from the authenticated workspace index and
// checking their versions before calling this function.
func PlanFromObligations(event HostStateEvent, obligations []prospective.Obligation) (AdvisoryDecisionPlan, error) {
	event, err := event.Normalized()
	if err != nil {
		return AdvisoryDecisionPlan{}, err
	}
	context := prospective.Context{
		HostEvent: map[string]any{
			"event_id": event.EventID, "event_type": event.EventType,
			"principal_id": "host_state", "channel_id": "local-service",
			"stage_id": event.StageID, "run_id": event.RunID,
			"agent_kind": event.AgentKind, "changed_paths": event.ChangedPaths,
			"artifacts": event.Artifacts, "run_state_flags": event.SignalCodes,
			"previous_stage_id":  event.PreviousStageID,
			"failure_signatures": event.FailureSignatures,
			"tools_available":    event.CapabilityCodes,
			"env_flags":          event.EnvFlags, "retry_count": event.RetryCount,
		},
		ToolState:           map[string]any{"available": event.CapabilityCodes},
		TestState:           map[string]any{"failure_signatures": event.FailureSignatures},
		FileState:           map[string]any{"changed_paths": event.ChangedPaths, "artifacts": event.Artifacts},
		RegisteredCondition: map[string]any{"signals": event.SignalCodes, "env_flags": event.EnvFlags},
		Scope:               "run", ScopeID: event.RunID, ObservedAt: event.OccurredAt,
	}
	candidates := make([]TriggerCandidate, 0, len(obligations))
	for _, obligation := range obligations {
		decision, err := prospective.Evaluate(obligation, context)
		if err != nil {
			return AdvisoryDecisionPlan{}, err
		}
		if decision.Matched {
			candidates = append(candidates, TriggerCandidate{ObligationID: decision.ObligationID,
				Matched: true, MatchedCues: decision.MatchedCues})
		}
	}
	return ProspectivePlan(event, candidates)
}
