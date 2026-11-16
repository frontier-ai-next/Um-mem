// Package prospective evaluates advisory obligations against typed host state.
// A match is evidence for a memory suggestion, never permission to act.
package prospective

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/cases"
)

type Predicate struct {
	Type     string
	Field    string
	Operator string
	Value    any
}

type Obligation struct {
	ID           string
	Scope        string
	ScopeID      string
	Status       string
	Trigger      []Predicate
	Suppress     []Predicate
	Cancel       []Predicate
	SemanticCues []string
	EarliestAt   string
	DeadlineAt   string
	ExpiresAt    string
}

type Context struct {
	Time                map[string]any `json:"time"`
	Deadline            map[string]any `json:"deadline"`
	HostEvent           map[string]any `json:"host_event"`
	ToolState           map[string]any `json:"tool_state"`
	TestState           map[string]any `json:"test_state"`
	FileState           map[string]any `json:"file_state"`
	RegisteredCondition map[string]any `json:"registered_condition"`
	Scope               string         `json:"scope"`
	ScopeID             string         `json:"scope_id"`
	SemanticText        string         `json:"semantic_text"`
	ObservedAt          time.Time      `json:"observed_at"`
}

type Origin struct {
	Channel        string `json:"channel"`
	PrincipalID    string `json:"principal_id"`
	IntegrityClass string `json:"integrity_class"`
}

type FireContext struct {
	Trigger          Context `json:"trigger"`
	Origin           *Origin `json:"origin"`
	AuthorityCeiling string  `json:"authority_ceiling"`
}

// RevalidateFire guards a new fire at the public client boundary. The
// resulting match is still only advisory and never authorizes a host action.
func RevalidateFire(obligation Obligation, context FireContext) (Decision, error) {
	decision, err := Evaluate(obligation, context.Trigger)
	if err != nil {
		return Decision{}, err
	}
	if decision.MatchKind != "typed" || !decision.DeliveryEligible {
		if decision.MatchKind == "semantic" {
			return decision, errors.New("prospective fire requires a delivery-eligible typed trigger")
		}
		return decision, errors.New(decision.Reason)
	}
	if context.Origin == nil || !map[string]bool{"observed": true, "verified": true, "endorsed": true}[context.Origin.IntegrityClass] ||
		strings.TrimSpace(context.Origin.PrincipalID) == "" {
		return decision, errors.New("prospective fire requires observed origin integrity")
	}
	if context.Trigger.HostEvent != nil {
		principal, ok := context.Trigger.HostEvent["principal_id"].(string)
		if !ok || principal == "" || context.Origin.Channel != "host_event" || context.Origin.PrincipalID != principal {
			return decision, errors.New("prospective host-event origin does not match authenticated envelope")
		}
	}
	if context.AuthorityCeiling != "advisory" {
		return decision, errors.New("prospective fire requires advisory authority ceiling")
	}
	return decision, nil
}

type Decision struct {
	ObligationID          string   `json:"obligation_id"`
	Matched               bool     `json:"matched"`
	MatchKind             string   `json:"match_kind"`
	Reason                string   `json:"reason"`
	TriggerType           string   `json:"trigger_type"`
	CandidateClass        string   `json:"candidate_class"`
	Advertised            bool     `json:"advertised"`
	DeliveryEligible      bool     `json:"delivery_eligible"`
	RecommendedTransition string   `json:"recommended_transition"`
	MatchedCues           []string `json:"matched_cues"`
	HostActionAuthorized  bool     `json:"host_action_authorized"`
}

func valuesFor(context Context, kind string) map[string]any {
	switch kind {
	case "time":
		return context.Time
	case "deadline":
		return context.Deadline
	case "host_event":
		return context.HostEvent
	case "tool_state":
		return context.ToolState
	case "test_state":
		return context.TestState
	case "file_state":
		return context.FileState
	case "registered_condition":
		return context.RegisteredCondition
	default:
		return nil
	}
}

func pythonType(value any) string {
	switch item := value.(type) {
	case bool:
		return "bool"
	case json.Number:
		if strings.ContainsAny(item.String(), ".eE") {
			return "float"
		}
		return "int"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "int"
	case float32, float64:
		return "float"
	case string:
		return "str"
	default:
		return "other"
	}
}

func asInteger(value any) (int64, bool) {
	switch item := value.(type) {
	case int:
		return int64(item), true
	case int8:
		return int64(item), true
	case int16:
		return int64(item), true
	case int32:
		return int64(item), true
	case int64:
		return item, true
	case uint:
		return int64(item), uint64(item) <= uint64(^uint64(0)>>1)
	case uint8:
		return int64(item), true
	case uint16:
		return int64(item), true
	case uint32:
		return int64(item), true
	case uint64:
		return int64(item), item <= uint64(^uint64(0)>>1)
	case json.Number:
		result, err := item.Int64()
		return result, err == nil
	default:
		return 0, false
	}
}

func asBigInteger(value any) (*big.Int, bool) {
	if pythonType(value) != "int" {
		return nil, false
	}
	var raw string
	switch item := value.(type) {
	case json.Number:
		raw = item.String()
	case int:
		raw = strconv.FormatInt(int64(item), 10)
	case int8:
		raw = strconv.FormatInt(int64(item), 10)
	case int16:
		raw = strconv.FormatInt(int64(item), 10)
	case int32:
		raw = strconv.FormatInt(int64(item), 10)
	case int64:
		raw = strconv.FormatInt(item, 10)
	case uint:
		raw = strconv.FormatUint(uint64(item), 10)
	case uint8:
		raw = strconv.FormatUint(uint64(item), 10)
	case uint16:
		raw = strconv.FormatUint(uint64(item), 10)
	case uint32:
		raw = strconv.FormatUint(uint64(item), 10)
	case uint64:
		raw = strconv.FormatUint(item, 10)
	default:
		return nil, false
	}
	result, ok := new(big.Int).SetString(raw, 10)
	return result, ok
}

func asFloat(value any) (float64, bool) {
	switch item := value.(type) {
	case float32:
		return float64(item), true
	case float64:
		return item, true
	case json.Number:
		result, err := strconv.ParseFloat(item.String(), 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func equalSameType(actual, expected any) bool {
	if pythonType(actual) != pythonType(expected) {
		return false
	}
	if pythonType(actual) == "int" {
		a, aOK := asBigInteger(actual)
		b, bOK := asBigInteger(expected)
		return aOK && bOK && a.Cmp(b) == 0
	}
	if pythonType(actual) == "float" {
		a, aOK := asFloat(actual)
		b, bOK := asFloat(expected)
		return aOK && bOK && a == b
	}
	return reflect.DeepEqual(actual, expected)
}

func pythonTruth(value any) bool {
	switch item := value.(type) {
	case bool:
		return item
	case string:
		return item != ""
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		if pythonType(item) == "float" {
			floating, ok := asFloat(item)
			return ok && floating != 0
		}
		integer, ok := asBigInteger(item)
		return ok && integer.Sign() != 0
	default:
		return value != nil
	}
}

func parseTime(raw string) (time.Time, bool) {
	if strings.HasSuffix(raw, "Z") {
		raw = strings.TrimSuffix(raw, "Z") + "+00:00"
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00"} {
		value, err := time.Parse(layout, raw)
		if err == nil {
			return value.UTC(), true
		}
	}
	return time.Time{}, false
}

func ordered(value any) (time.Time, int64, float64, string) {
	switch item := value.(type) {
	case string:
		parsed, ok := parseTime(item)
		if ok {
			return parsed, 0, 0, "time"
		}
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		if pythonType(item) == "float" {
			floating, ok := asFloat(item)
			if ok {
				return time.Time{}, 0, floating, "float"
			}
			return time.Time{}, 0, 0, ""
		}
		integer, ok := asInteger(item)
		if ok {
			return time.Time{}, integer, 0, "int"
		}
	case float32, float64:
		floating, _ := asFloat(item)
		return time.Time{}, 0, floating, "float"
	}
	return time.Time{}, 0, 0, ""
}

func PredicateMatches(predicate Predicate, context Context) bool {
	values := valuesFor(context, predicate.Type)
	actual, present := values[predicate.Field]
	if predicate.Operator == "exists" {
		return present == pythonTruth(predicate.Value)
	}
	if !present {
		return false
	}
	switch predicate.Operator {
	case "eq":
		return equalSameType(actual, predicate.Value)
	case "neq":
		return pythonType(actual) == pythonType(predicate.Value) && !equalSameType(actual, predicate.Value)
	case "contains":
		if text, ok := actual.(string); ok {
			needle, ok := predicate.Value.(string)
			return ok && strings.Contains(text, needle)
		}
		return sequenceMatch(actual, func(value any) bool { return reflect.DeepEqual(value, predicate.Value) })
	case "prefix":
		prefix, ok := predicate.Value.(string)
		if !ok {
			return false
		}
		if text, ok := actual.(string); ok {
			return strings.HasPrefix(text, prefix)
		}
		return sequenceMatch(actual, func(value any) bool {
			text, ok := value.(string)
			return ok && strings.HasPrefix(text, prefix)
		})
	case "gte", "lte":
		if pythonType(actual) == "int" && pythonType(predicate.Value) == "int" {
			left, leftOK := asBigInteger(actual)
			right, rightOK := asBigInteger(predicate.Value)
			if !leftOK || !rightOK {
				return false
			}
			comparison := left.Cmp(right)
			if predicate.Operator == "gte" {
				return comparison >= 0
			}
			return comparison <= 0
		}
		aTime, aInt, aFloat, aType := ordered(actual)
		bTime, bInt, bFloat, bType := ordered(predicate.Value)
		if aType == "" || aType != bType {
			return false
		}
		if aType == "time" {
			if predicate.Operator == "gte" {
				return !aTime.Before(bTime)
			}
			return !aTime.After(bTime)
		}
		if aType == "int" {
			if predicate.Operator == "gte" {
				return aInt >= bInt
			}
			return aInt <= bInt
		}
		if predicate.Operator == "gte" {
			return aFloat >= bFloat
		}
		return aFloat <= bFloat
	default:
		return false
	}
}

func sequenceMatch(value any, test func(any) bool) bool {
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			if test(item) {
				return true
			}
		}
	case []string:
		for _, item := range values {
			if test(item) {
				return true
			}
		}
	}
	return false
}

func allMatch(predicates []Predicate, context Context) bool {
	if len(predicates) == 0 {
		return false
	}
	for _, predicate := range predicates {
		if !PredicateMatches(predicate, context) {
			return false
		}
	}
	return true
}

func normalizedWords(value string) string {
	value = cases.Fold().String(value)
	var words []string
	var word strings.Builder
	flush := func() {
		if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' || char == '.' || char == '-' {
			word.WriteRune(char)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(words, " ")
}

func Evaluate(obligation Obligation, context Context) (Decision, error) {
	if strings.TrimSpace(obligation.ID) == "" {
		return Decision{}, errors.New("prospective obligation ID is required")
	}
	decision := Decision{ObligationID: obligation.ID, MatchKind: "none", CandidateClass: "none",
		MatchedCues: []string{}}
	if obligation.Status != "armed" {
		decision.Reason = "status_" + obligation.Status + "_not_armed"
		return decision, nil
	}
	if obligation.Scope != "workspace" && (context.Scope != obligation.Scope || context.ScopeID != obligation.ScopeID) {
		decision.Reason = "scope_mismatch"
		return decision, nil
	}
	observed := context.ObservedAt
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	parseBoundary := func(raw string) (time.Time, error) {
		parsed, ok := parseTime(raw)
		if !ok {
			return time.Time{}, errors.New("prospective timestamp must include a timezone")
		}
		return parsed, nil
	}
	if obligation.ExpiresAt != "" {
		boundary, err := parseBoundary(obligation.ExpiresAt)
		if err != nil {
			return Decision{}, err
		}
		if !observed.Before(boundary) {
			decision.Matched, decision.MatchKind, decision.Reason = true, "lifecycle", "expiry_reached"
			decision.RecommendedTransition = "expired"
			return decision, nil
		}
	}
	if allMatch(obligation.Cancel, context) {
		decision.Matched, decision.MatchKind, decision.Reason = true, "lifecycle", "cancel_condition_matched"
		decision.RecommendedTransition = "cancelled"
		return decision, nil
	}
	if allMatch(obligation.Suppress, context) {
		decision.Reason = "suppressed_by_current_state"
		return decision, nil
	}
	if obligation.EarliestAt != "" {
		boundary, err := parseBoundary(obligation.EarliestAt)
		if err != nil {
			return Decision{}, err
		}
		if observed.Before(boundary) {
			decision.Reason = "before_earliest_at"
			return decision, nil
		}
	}
	if allMatch(obligation.Trigger, context) {
		decision.Matched, decision.MatchKind, decision.Reason = true, "typed", "typed_predicates_matched"
		decision.CandidateClass, decision.Advertised, decision.DeliveryEligible = "release", true, true
		decision.TriggerType = obligation.Trigger[0].Type
		for _, predicate := range obligation.Trigger[1:] {
			if predicate.Type != decision.TriggerType {
				decision.TriggerType = "multiple"
				break
			}
		}
		return decision, nil
	}
	if obligation.DeadlineAt != "" {
		boundary, err := parseBoundary(obligation.DeadlineAt)
		if err != nil {
			return Decision{}, err
		}
		if !observed.Before(boundary) {
			decision.Matched, decision.MatchKind, decision.Reason = true, "typed", "deadline_reached"
			decision.TriggerType, decision.CandidateClass = "deadline", "release"
			decision.Advertised, decision.DeliveryEligible = true, true
			return decision, nil
		}
	}
	semanticText := normalizedWords(context.SemanticText)
	for _, cue := range obligation.SemanticCues {
		normalized := normalizedWords(cue)
		if normalized != "" && strings.Contains(semanticText, normalized) {
			decision.MatchedCues = append(decision.MatchedCues, cue)
		}
	}
	if len(decision.MatchedCues) > 0 {
		decision.Matched, decision.MatchKind, decision.Reason = true, "semantic", "semantic_candidate_requires_typed_gate"
		decision.CandidateClass = "experimental"
		return decision, nil
	}
	decision.Reason = "no_trigger_match"
	return decision, nil
}
