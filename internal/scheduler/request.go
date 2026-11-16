package scheduler

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

var hostStateRequestFields = map[string]bool{
	"event_id": true, "event_type": true, "run_id": true, "stage_id": true,
	"occurred_at": true, "signal_codes": true, "capability_codes": true,
	"retry_count": true, "agent_kind": true, "changed_paths": true,
	"artifacts": true, "previous_stage_id": true, "failure_signatures": true,
	"env_flags": true,
}

// EventFromRequest binds an untrusted host event to the authenticated
// listener's workspace and policy revision. It rejects any extra field,
// including raw prompts, tool arguments, and claimed authority.
func EventFromRequest(request map[string]any, workspaceID, policyRevision string, now time.Time) (HostStateEvent, error) {
	if request == nil {
		return HostStateEvent{}, errors.New("local service host state is invalid")
	}
	for key := range request {
		if !hostStateRequestFields[key] {
			return HostStateEvent{}, errors.New("local service host state has unsupported fields")
		}
	}
	required := func(key string) (string, error) {
		value, ok := request[key].(string)
		if !ok {
			return "", errors.New("local service host state is invalid")
		}
		return value, nil
	}
	var event HostStateEvent
	var err error
	if event.EventID, err = required("event_id"); err != nil {
		return HostStateEvent{}, err
	}
	if event.EventType, err = required("event_type"); err != nil {
		return HostStateEvent{}, err
	}
	if event.RunID, err = required("run_id"); err != nil {
		return HostStateEvent{}, err
	}
	if event.StageID, err = required("stage_id"); err != nil {
		return HostStateEvent{}, err
	}
	event.WorkspaceID, event.StateRevision = workspaceID, policyRevision
	event.OccurredAt = now
	if value, exists := request["occurred_at"]; exists && value != nil {
		raw, ok := value.(string)
		if !ok {
			return HostStateEvent{}, errors.New("local service host state occurred_at is invalid")
		}
		event.OccurredAt, err = parseAwareTime(raw)
		if err != nil {
			return HostStateEvent{}, errors.New("local service host state occurred_at is invalid")
		}
	}
	for _, field := range []struct {
		name   string
		target *[]string
	}{
		{"signal_codes", &event.SignalCodes}, {"capability_codes", &event.CapabilityCodes},
		{"changed_paths", &event.ChangedPaths}, {"artifacts", &event.Artifacts},
		{"failure_signatures", &event.FailureSignatures}, {"env_flags", &event.EnvFlags},
	} {
		value, exists := request[field.name]
		if !exists {
			continue
		}
		items, ok := value.([]any)
		if !ok || len(items) > 64 {
			return HostStateEvent{}, errors.New("local service host state is invalid")
		}
		result := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return HostStateEvent{}, errors.New("local service host state is invalid")
			}
			result = append(result, text)
		}
		*field.target = result
	}
	if value, exists := request["retry_count"]; exists {
		number, ok := value.(json.Number)
		if !ok {
			return HostStateEvent{}, errors.New("local service host state is invalid")
		}
		event.RetryCount, err = strconv.Atoi(number.String())
		if err != nil {
			return HostStateEvent{}, errors.New("local service host state is invalid")
		}
	}
	event.AgentKind = "generic_agent"
	if _, exists := request["agent_kind"]; exists {
		event.AgentKind, err = required("agent_kind")
		if err != nil || event.AgentKind == "" {
			return HostStateEvent{}, errors.New("local service host state is invalid")
		}
	}
	if _, exists := request["previous_stage_id"]; exists {
		event.PreviousStageID, err = required("previous_stage_id")
		if err != nil {
			return HostStateEvent{}, err
		}
	}
	return event.Normalized()
}

func parseAwareTime(raw string) (time.Time, error) {
	if strings.HasSuffix(raw, "Z") {
		raw = strings.TrimSuffix(raw, "Z") + "+00:00"
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00"} {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("timestamp must carry a timezone")
}
