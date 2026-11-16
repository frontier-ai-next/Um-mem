package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"golang.org/x/text/unicode/norm"
)

// Prospective obligations are advisory memory objects. No matching trigger
// authorizes a host action.
type ProspectivePredicate struct {
	PredicateType string `json:"predicate_type"`
	Field         string `json:"field"`
	Operator      string `json:"operator"`
	Value         any    `json:"value"`
}

type ProspectiveTelemetry struct {
	FalsePositiveCount         int `json:"false_positive_count"`
	MissedTriggerCount         int `json:"missed_trigger_count"`
	OverloadedTriggerCount     int `json:"overloaded_trigger_count"`
	AttentionInterruptionCount int `json:"attention_interruption_count"`
	DuplicateFireCount         int `json:"duplicate_fire_count"`
	LateTriggerCount           int `json:"late_trigger_count"`
}

type ProspectiveTelemetryEntry struct {
	ObligationID               string `json:"obligation_id"`
	Status                     string `json:"status"`
	FalseActivationCount       int    `json:"false_activation_count"`
	MissedTriggerCount         int    `json:"missed_trigger_count"`
	OverloadedTriggerCount     int    `json:"overloaded_trigger_count"`
	AttentionInterruptionCount int    `json:"attention_interruption_count"`
	DuplicateFireCount         int    `json:"duplicate_fire_count"`
	LateTriggerCount           int    `json:"late_trigger_count"`
}

type ProspectiveTelemetryReport struct {
	Entries              []ProspectiveTelemetryEntry `json:"entries"`
	Channel              string                      `json:"channel"`
	OrdinaryRecall       bool                        `json:"ordinary_recall"`
	HostActionAuthorized bool                        `json:"host_action_authorized"`
}

type ProspectiveObligation struct {
	ObligationID               string                 `json:"obligation_id"`
	Scope                      string                 `json:"scope"`
	ScopeID                    string                 `json:"scope_id"`
	EvidenceRefs               []EvidenceRef          `json:"evidence_refs"`
	RequiredIntervention       string                 `json:"required_intervention"`
	TriggerPredicates          []ProspectivePredicate `json:"trigger_predicates"`
	SemanticTriggerCues        []string               `json:"semantic_trigger_cues"`
	EarliestAt                 string                 `json:"earliest_at"`
	DeadlineAt                 string                 `json:"deadline_at"`
	ExpiresAt                  string                 `json:"expires_at"`
	RecurrencePolicy           string                 `json:"recurrence_policy"`
	RecurrenceIntervalSeconds  int                    `json:"recurrence_interval_seconds"`
	SuppressPredicates         []ProspectivePredicate `json:"suppress_predicates"`
	CancelPredicates           []ProspectivePredicate `json:"cancel_predicates"`
	SatisfactionVerifier       string                 `json:"satisfaction_verifier"`
	SatisfactionVerifierResult string                 `json:"satisfaction_verifier_result"`
	SatisfactionEvidenceRefs   []EvidenceRef          `json:"satisfaction_evidence_refs"`
	Telemetry                  ProspectiveTelemetry   `json:"telemetry"`
	Status                     string                 `json:"status"`
	TransitionFrom             *string                `json:"transition_from"`
	TransitionEvidenceRefs     []EvidenceRef          `json:"transition_evidence_refs"`
}

type ProspectiveLifecycleEvent struct {
	EventID                    string        `json:"event_id"`
	EventType                  string        `json:"event_type"`
	ExpectedVersion            int           `json:"expected_version"`
	ExpectedStatus             string        `json:"expected_status"`
	EvidenceRefs               []EvidenceRef `json:"evidence_refs"`
	IdempotencyKey             string        `json:"idempotency_key"`
	SatisfactionEvidenceRefs   []EvidenceRef `json:"satisfaction_evidence_refs"`
	SatisfactionVerifier       string        `json:"satisfaction_verifier"`
	SatisfactionVerifierResult string        `json:"satisfaction_verifier_result"`
}

func (event ProspectiveLifecycleEvent) Normalize() (ProspectiveLifecycleEvent, error) {
	event.EventID = trimProspectiveText(event.EventID)
	event.EventType = normalizeProspectiveText(event.EventType)
	event.ExpectedStatus = normalizeProspectiveText(event.ExpectedStatus)
	event.IdempotencyKey = normalizeProspectiveText(event.IdempotencyKey)
	event.SatisfactionVerifier = normalizeProspectiveText(event.SatisfactionVerifier)
	event.SatisfactionVerifierResult = normalizeProspectiveText(event.SatisfactionVerifierResult)
	if event.IdempotencyKey == "" {
		event.IdempotencyKey = event.EventID
	}
	targets := map[string]string{"arm": "armed", "fire": "fired", "satisfy": "satisfied", "cancel": "cancelled", "expire": "expired"}
	status, ok := targets[event.EventType]
	if !ok || event.EventID == "" || event.ExpectedVersion < 0 || !prospectiveTransitions[event.ExpectedStatus][status] {
		return ProspectiveLifecycleEvent{}, errors.New("prospective lifecycle event identity or transition is invalid")
	}
	var err error
	event.EvidenceRefs, err = normalizeProspectiveRefs(event.EvidenceRefs)
	if err != nil || len(event.EvidenceRefs) == 0 {
		return ProspectiveLifecycleEvent{}, errors.New("prospective lifecycle event requires evidence_refs")
	}
	event.SatisfactionEvidenceRefs, err = normalizeProspectiveRefs(event.SatisfactionEvidenceRefs)
	if err != nil {
		return ProspectiveLifecycleEvent{}, err
	}
	if !oneOf(event.SatisfactionVerifierResult, "", "passed", "failed") {
		return ProspectiveLifecycleEvent{}, errors.New("prospective verifier result is invalid")
	}
	if status == "satisfied" {
		if len(event.SatisfactionEvidenceRefs) == 0 || event.SatisfactionVerifier == "" || event.SatisfactionVerifierResult != "passed" {
			return ProspectiveLifecycleEvent{}, errors.New("prospective satisfaction requires passed proof")
		}
	} else if len(event.SatisfactionEvidenceRefs) > 0 || event.SatisfactionVerifierResult != "" {
		return ProspectiveLifecycleEvent{}, errors.New("prospective satisfaction proof requires satisfy event")
	}
	return event, nil
}

func (event ProspectiveLifecycleEvent) TargetStatus() string {
	return map[string]string{"arm": "armed", "fire": "fired", "satisfy": "satisfied", "cancel": "cancelled", "expire": "expired"}[event.EventType]
}

var prospectiveTransitions = map[string]map[string]bool{
	"pending":   {"armed": true, "cancelled": true, "expired": true},
	"armed":     {"fired": true, "cancelled": true, "expired": true},
	"fired":     {"satisfied": true, "cancelled": true, "expired": true},
	"satisfied": {}, "cancelled": {}, "expired": {},
}

func normalizeProspectiveText(value string) string { return norm.NFC.String(strings.TrimSpace(value)) }
func trimProspectiveText(value string) string      { return strings.TrimSpace(value) }

func normalizeProspectivePredicates(values []ProspectivePredicate) ([]ProspectivePredicate, error) {
	result := make([]ProspectivePredicate, 0, len(values))
	for _, value := range values {
		value.PredicateType = normalizeProspectiveText(value.PredicateType)
		value.Field = trimProspectiveText(value.Field)
		value.Operator = normalizeProspectiveText(value.Operator)
		if !oneOf(value.PredicateType, "time", "deadline", "host_event", "tool_state", "test_state", "file_state", "registered_condition") ||
			value.Field == "" || !oneOf(value.Operator, "eq", "neq", "contains", "prefix", "gte", "lte", "exists") {
			return nil, errors.New("prospective trigger predicate is invalid")
		}
		switch typed := value.Value.(type) {
		case string:
			value.Value = normalizeProspectiveText(typed)
			if value.Operator != "exists" && value.Value == "" {
				return nil, errors.New("prospective trigger value is required")
			}
		case bool, int, int64:
		case json.Number:
			if strings.ContainsAny(typed.String(), ".eE") {
				return nil, errors.New("prospective trigger value must be an integer")
			}
			if _, ok := new(big.Int).SetString(typed.String(), 10); !ok {
				return nil, errors.New("prospective trigger value must be an integer")
			}
		default:
			return nil, errors.New("prospective trigger value must be a string, integer, or boolean")
		}
		duplicate := false
		for _, existing := range result {
			if reflect.DeepEqual(existing, value) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result = append(result, value)
		}
	}
	return result, nil
}

func normalizeProspectiveTime(value, field string) (string, time.Time, error) {
	value = normalizeProspectiveText(value)
	if value == "" {
		return "", time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00", "2006-01-02T15:04:05.999999999-0700",
		"2006-01-02T15:04:05-0700"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return value, parsed.UTC(), nil
		}
	}
	return "", time.Time{}, fmt.Errorf("%s must include an ISO-8601 timezone", field)
}

func normalizeProspectiveRefs(values []EvidenceRef) ([]EvidenceRef, error) {
	result := make([]EvidenceRef, 0, len(values))
	for _, value := range values {
		if err := value.Validate(); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func (value ProspectiveObligation) Normalize() (ProspectiveObligation, error) {
	value.ObligationID = trimProspectiveText(value.ObligationID)
	value.Scope = normalizeProspectiveText(value.Scope)
	value.ScopeID = normalizeProspectiveText(value.ScopeID)
	value.RequiredIntervention = normalizeProspectiveText(value.RequiredIntervention)
	value.SatisfactionVerifier = normalizeProspectiveText(value.SatisfactionVerifier)
	value.SatisfactionVerifierResult = normalizeProspectiveText(value.SatisfactionVerifierResult)
	if value.TransitionFrom != nil {
		previous := normalizeProspectiveText(*value.TransitionFrom)
		value.TransitionFrom = &previous
	}
	value.Status = trimProspectiveText(value.Status)
	value.RecurrencePolicy = trimProspectiveText(value.RecurrencePolicy)
	if value.Status == "" {
		value.Status = "pending"
	}
	if value.RecurrencePolicy == "" {
		value.RecurrencePolicy = "once"
	}
	if value.ObligationID == "" || !oneOf(value.Scope, "workspace", "run", "task", "subtask") ||
		(value.Scope == "workspace" && value.ScopeID != "") ||
		(value.Scope != "workspace" && value.ScopeID == "") ||
		!oneOf(value.Status, "pending", "armed", "fired", "satisfied", "cancelled", "expired") ||
		!oneOf(value.RecurrencePolicy, "once", "repeat") || value.RecurrenceIntervalSeconds < 0 ||
		(value.RecurrencePolicy == "once" && value.RecurrenceIntervalSeconds != 0) ||
		(value.RecurrencePolicy == "repeat" && value.RecurrenceIntervalSeconds == 0) {
		return ProspectiveObligation{}, errors.New("prospective obligation identity, scope, status, or recurrence is invalid")
	}
	var err error
	value.EvidenceRefs, err = normalizeProspectiveRefs(value.EvidenceRefs)
	if err != nil || len(value.EvidenceRefs) == 0 {
		return ProspectiveObligation{}, errors.New("prospective obligation requires evidence_refs")
	}
	value.TriggerPredicates, err = normalizeProspectivePredicates(value.TriggerPredicates)
	if err != nil {
		return ProspectiveObligation{}, err
	}
	value.SuppressPredicates, err = normalizeProspectivePredicates(value.SuppressPredicates)
	if err != nil {
		return ProspectiveObligation{}, err
	}
	value.CancelPredicates, err = normalizeProspectivePredicates(value.CancelPredicates)
	if err != nil {
		return ProspectiveObligation{}, err
	}
	value.SemanticTriggerCues = normalizeProspectiveStrings(value.SemanticTriggerCues)
	for _, cue := range value.SemanticTriggerCues {
		if cue == "" || len([]rune(cue)) > 256 {
			return ProspectiveObligation{}, errors.New("prospective semantic trigger cue is invalid")
		}
	}
	if (len(value.TriggerPredicates) > 0 || len(value.SemanticTriggerCues) > 0) && value.RequiredIntervention == "" {
		return ProspectiveObligation{}, errors.New("prospective trigger requires required_intervention")
	}
	value.EarliestAt, _, err = normalizeProspectiveTime(value.EarliestAt, "earliest_at")
	if err != nil {
		return ProspectiveObligation{}, err
	}
	value.DeadlineAt, _, err = normalizeProspectiveTime(value.DeadlineAt, "deadline_at")
	if err != nil {
		return ProspectiveObligation{}, err
	}
	value.ExpiresAt, _, err = normalizeProspectiveTime(value.ExpiresAt, "expires_at")
	if err != nil {
		return ProspectiveObligation{}, err
	}
	_, earliest, _ := normalizeProspectiveTime(value.EarliestAt, "earliest_at")
	_, deadline, _ := normalizeProspectiveTime(value.DeadlineAt, "deadline_at")
	_, expires, _ := normalizeProspectiveTime(value.ExpiresAt, "expires_at")
	if value.RequiredIntervention != "" && len(value.TriggerPredicates) == 0 && len(value.SemanticTriggerCues) == 0 && value.DeadlineAt == "" {
		return ProspectiveObligation{}, errors.New("prospective intervention cannot be unconditioned")
	}
	if !earliest.IsZero() && !deadline.IsZero() && deadline.Before(earliest) ||
		!earliest.IsZero() && !expires.IsZero() && expires.Before(earliest) ||
		!deadline.IsZero() && !expires.IsZero() && expires.Before(deadline) {
		return ProspectiveObligation{}, errors.New("prospective time bounds are inconsistent")
	}
	for _, count := range []int{value.Telemetry.FalsePositiveCount, value.Telemetry.MissedTriggerCount,
		value.Telemetry.OverloadedTriggerCount, value.Telemetry.AttentionInterruptionCount,
		value.Telemetry.DuplicateFireCount, value.Telemetry.LateTriggerCount} {
		if count < 0 {
			return ProspectiveObligation{}, errors.New("prospective telemetry must be non-negative")
		}
	}
	value.SatisfactionEvidenceRefs, err = normalizeProspectiveRefs(value.SatisfactionEvidenceRefs)
	if err != nil {
		return ProspectiveObligation{}, err
	}
	value.TransitionEvidenceRefs, err = normalizeProspectiveRefs(value.TransitionEvidenceRefs)
	if err != nil {
		return ProspectiveObligation{}, err
	}
	if !oneOf(value.SatisfactionVerifierResult, "", "passed", "failed") ||
		(value.Status != "satisfied" && len(value.SatisfactionEvidenceRefs) > 0) {
		return ProspectiveObligation{}, errors.New("prospective satisfaction proof is invalid")
	}
	if value.Status == "pending" {
		if value.TransitionFrom != nil || len(value.TransitionEvidenceRefs) != 0 {
			return ProspectiveObligation{}, errors.New("pending prospective obligation cannot carry a transition")
		}
	} else {
		if value.TransitionFrom == nil || !prospectiveTransitions[*value.TransitionFrom][value.Status] ||
			len(value.TransitionEvidenceRefs) == 0 {
			return ProspectiveObligation{}, errors.New("invalid prospective transition")
		}
		if value.Status == "satisfied" && (len(value.SatisfactionEvidenceRefs) == 0 ||
			value.SatisfactionVerifier == "" || value.SatisfactionVerifierResult != "passed") {
			return ProspectiveObligation{}, errors.New("satisfied prospective obligation requires passed proof")
		}
	}
	return value, nil
}

func normalizeProspectiveStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, raw := range values {
		value := trimProspectiveText(raw)
		found := false
		for _, existing := range result {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			result = append(result, value)
		}
	}
	return result
}

func (value ProspectiveObligation) CanonicalJSON() ([]byte, error) {
	normalized, err := value.Normalize()
	if err != nil {
		return nil, err
	}
	for _, list := range []*[]ProspectivePredicate{&normalized.TriggerPredicates, &normalized.SuppressPredicates, &normalized.CancelPredicates} {
		if *list == nil {
			*list = []ProspectivePredicate{}
		}
	}
	for _, list := range []*[]EvidenceRef{&normalized.EvidenceRefs, &normalized.SatisfactionEvidenceRefs, &normalized.TransitionEvidenceRefs} {
		if *list == nil {
			*list = []EvidenceRef{}
		}
	}
	if normalized.SemanticTriggerCues == nil {
		normalized.SemanticTriggerCues = []string{}
	}
	return jsoncompat.MarshalPython(normalized, false)
}

func (value ProspectiveObligation) TransitionTo(status string, evidence, satisfaction []EvidenceRef,
	verifier, verifierResult string) (ProspectiveObligation, error) {
	current, err := value.Normalize()
	if err != nil {
		return ProspectiveObligation{}, err
	}
	previous := current.Status
	current.Status = status
	current.TransitionFrom = &previous
	current.TransitionEvidenceRefs = evidence
	current.SatisfactionEvidenceRefs = satisfaction
	if verifier != "" {
		current.SatisfactionVerifier = verifier
	}
	current.SatisfactionVerifierResult = verifierResult
	return current.Normalize()
}

// WithTelemetry returns a new obligation with non-negative observation deltas.
func (value ProspectiveObligation) WithTelemetry(delta ProspectiveTelemetry) (ProspectiveObligation, error) {
	normalized, err := value.Normalize()
	if err != nil {
		return ProspectiveObligation{}, err
	}
	add := func(current, change int) (int, error) {
		if change < 0 || current > int(^uint(0)>>1)-change {
			return 0, errors.New("prospective telemetry delta is invalid")
		}
		return current + change, nil
	}
	pairs := [][2]int{
		{normalized.Telemetry.FalsePositiveCount, delta.FalsePositiveCount},
		{normalized.Telemetry.MissedTriggerCount, delta.MissedTriggerCount},
		{normalized.Telemetry.OverloadedTriggerCount, delta.OverloadedTriggerCount},
		{normalized.Telemetry.AttentionInterruptionCount, delta.AttentionInterruptionCount},
		{normalized.Telemetry.DuplicateFireCount, delta.DuplicateFireCount},
		{normalized.Telemetry.LateTriggerCount, delta.LateTriggerCount},
	}
	values := make([]int, 0, len(pairs))
	for _, pair := range pairs {
		count, err := add(pair[0], pair[1])
		if err != nil {
			return ProspectiveObligation{}, err
		}
		values = append(values, count)
	}
	normalized.Telemetry = ProspectiveTelemetry{values[0], values[1], values[2], values[3], values[4], values[5]}
	return normalized, nil
}
