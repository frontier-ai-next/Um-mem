package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var eventTypes = map[string]bool{"compaction": true, "high_risk_boundary": true,
	"plan_revision": true, "prospective_trigger": true, "session_boundary": true,
	"source_change": true, "subgoal_transition": true, "turn_completion": true,
	"tool_result": true, "verification_result": true}

type HostStateEvent struct {
	EventID           string
	EventType         string
	WorkspaceID       string
	RunID             string
	StageID           string
	StateRevision     string
	OccurredAt        time.Time
	SignalCodes       []string
	CapabilityCodes   []string
	RetryCount        int
	AgentKind         string
	ChangedPaths      []string
	Artifacts         []string
	PreviousStageID   string
	FailureSignatures []string
	EnvFlags          []string
}

func token(value, name string) error {
	if !tokenPattern.MatchString(value) {
		return fmt.Errorf("%s must be a content-free identifier", name)
	}
	return nil
}

func tokenSet(values []string, name string) ([]string, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("%s must be a bounded sequence", name)
	}
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if err := token(value, name); err != nil {
			return nil, err
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func digestSet(values []string, name string) ([]string, error) {
	if len(values) > 64 {
		return nil, fmt.Errorf("%s must be a bounded sequence", name)
	}
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if !digestPattern.MatchString(value) {
			return nil, fmt.Errorf("%s must contain SHA-256 digests", name)
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func (event HostStateEvent) Normalized() (HostStateEvent, error) {
	for _, pair := range [][2]string{{"event_id", event.EventID}, {"event_type", event.EventType},
		{"workspace_id", event.WorkspaceID}, {"run_id", event.RunID}, {"stage_id", event.StageID},
		{"state_revision", event.StateRevision}} {
		if err := token(pair[1], pair[0]); err != nil {
			return HostStateEvent{}, err
		}
	}
	if !eventTypes[event.EventType] {
		return HostStateEvent{}, errors.New("event_type is not supported")
	}
	if event.OccurredAt.IsZero() {
		return HostStateEvent{}, errors.New("occurred_at must be an aware timestamp")
	}
	event.OccurredAt = event.OccurredAt.UTC().Truncate(time.Microsecond)
	if event.RetryCount < 0 || event.RetryCount > 1_000_000 {
		return HostStateEvent{}, errors.New("retry_count is out of bounds")
	}
	if event.AgentKind == "" {
		event.AgentKind = "generic_agent"
	}
	if err := token(event.AgentKind, "agent_kind"); err != nil {
		return HostStateEvent{}, err
	}
	if event.PreviousStageID != "" {
		if err := token(event.PreviousStageID, "previous_stage_id"); err != nil {
			return HostStateEvent{}, err
		}
	}
	var err error
	for _, field := range []struct {
		name  string
		value *[]string
	}{
		{"signal_codes", &event.SignalCodes}, {"capability_codes", &event.CapabilityCodes},
		{"changed_paths", &event.ChangedPaths}, {"artifacts", &event.Artifacts},
		{"failure_signatures", &event.FailureSignatures}, {"env_flags", &event.EnvFlags}} {
		*field.value, err = tokenSet(*field.value, field.name)
		if err != nil {
			return HostStateEvent{}, err
		}
	}
	return event, nil
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (event HostStateEvent) Fingerprint() (string, error) {
	normalized, err := event.Normalized()
	if err != nil {
		return "", err
	}
	record := map[string]any{"event_id": normalized.EventID, "event_type": normalized.EventType,
		"workspace_id": normalized.WorkspaceID, "run_id": normalized.RunID,
		"stage_id": normalized.StageID, "state_revision": normalized.StateRevision,
		"signal_codes": normalized.SignalCodes, "capability_codes": normalized.CapabilityCodes,
		"retry_count": normalized.RetryCount, "agent_kind": normalized.AgentKind,
		"changed_paths": normalized.ChangedPaths, "artifacts": normalized.Artifacts,
		"previous_stage_id":  normalized.PreviousStageID,
		"failure_signatures": normalized.FailureSignatures, "env_flags": normalized.EnvFlags}
	encoded, err := jsoncompat.MarshalPython(record, false)
	if err != nil {
		return "", err
	}
	return hash(string(encoded)), nil
}

func EventIDHash(eventID string) (string, error) {
	if err := token(eventID, "event_id"); err != nil {
		return "", err
	}
	return hash("host-event-id:" + eventID), nil
}

type AdvisoryDecisionPlan struct {
	DecisionRevision         string
	ActivationDueAt          time.Time
	ActivationReasonCode     string
	ConsolidationDueAt       time.Time
	ConsolidationReasonCode  string
	ConsolidationDisposition string
	MatchedRuleSHA256s       []string
	MatchedActionSHA256s     []string
	DeferredTriggerOwnerRefs []string
}

func DefaultPlan(event HostStateEvent) (AdvisoryDecisionPlan, error) {
	event, err := event.Normalized()
	if err != nil {
		return AdvisoryDecisionPlan{}, err
	}
	return AdvisoryDecisionPlan{DecisionRevision: "service_scheduler.default@1",
		ActivationDueAt: event.OccurredAt, ActivationReasonCode: event.EventType + "_activation",
		ConsolidationDueAt: event.OccurredAt, ConsolidationReasonCode: event.EventType + "_consolidation",
		ConsolidationDisposition: "assess"}, nil
}

func timestamp(value time.Time) string {
	value = value.UTC().Truncate(time.Microsecond)
	if value.Nanosecond() == 0 {
		return value.Format("2006-01-02T15:04:05-07:00")
	}
	return value.Format("2006-01-02T15:04:05.000000-07:00")
}

func (plan AdvisoryDecisionPlan) Record() (map[string]any, error) {
	for _, pair := range [][2]string{{"decision_revision", plan.DecisionRevision},
		{"activation_reason_code", plan.ActivationReasonCode},
		{"consolidation_reason_code", plan.ConsolidationReasonCode}} {
		if err := token(pair[1], pair[0]); err != nil {
			return nil, err
		}
	}
	if plan.ActivationDueAt.IsZero() || plan.ConsolidationDueAt.IsZero() {
		return nil, errors.New("plan due times are required")
	}
	if plan.ConsolidationDisposition == "" {
		plan.ConsolidationDisposition = "assess"
	}
	if plan.ConsolidationDisposition != "assess" && plan.ConsolidationDisposition != "defer" {
		return nil, errors.New("consolidation disposition is invalid")
	}
	rules, err := digestSet(plan.MatchedRuleSHA256s, "matched_rule_sha256s")
	if err != nil {
		return nil, err
	}
	actions, err := digestSet(plan.MatchedActionSHA256s, "matched_action_sha256s")
	if err != nil {
		return nil, err
	}
	owners, err := tokenSet(plan.DeferredTriggerOwnerRefs, "deferred_trigger_owner_refs")
	if err != nil {
		return nil, err
	}
	record := map[string]any{"decision_revision": plan.DecisionRevision,
		"activation_due_at": timestamp(plan.ActivationDueAt), "activation_reason_code": plan.ActivationReasonCode,
		"consolidation_due_at": timestamp(plan.ConsolidationDueAt), "consolidation_reason_code": plan.ConsolidationReasonCode,
		"consolidation_disposition": plan.ConsolidationDisposition,
		"matched_rule_sha256s":      rules, "matched_action_sha256s": actions,
		"deferred_trigger_owner_refs": owners}
	encoded, err := jsoncompat.MarshalPython(record, false)
	if err != nil {
		return nil, err
	}
	record["plan_sha256"] = hash(string(encoded))
	return record, nil
}

func JobID(eventFingerprint, kind string) (string, error) {
	if !digestPattern.MatchString(eventFingerprint) {
		return "", errors.New("event fingerprint is invalid")
	}
	if kind != "activation_opportunity" && kind != "consolidation_assessment" {
		return "", errors.New("scheduler job kind is invalid")
	}
	return hash(strings.Join([]string{"scheduler-job", eventFingerprint, kind}, ":")), nil
}
