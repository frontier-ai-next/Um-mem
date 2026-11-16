package hosthook

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type Event struct {
	HostSlug       string
	HostLabel      string
	AdapterID      string
	CapabilityCode string
	SchemaVersion  int
	EventName      string
	SessionID      string
	IdentityFields [][2]string
	StageKind      string
	StageID        string
	SubtaskKind    string
	SubtaskID      string
}

func sha(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (event Event) Digest() (string, error) {
	fields := event.IdentityFields
	if fields == nil {
		fields = [][2]string{}
	}
	record := map[string]any{"event_name": event.EventName,
		"identity_fields": fields, "schema_version": event.SchemaVersion,
		"session_id": event.SessionID, "stage_id": event.StageID,
		"subtask_id": event.SubtaskID}
	encoded, err := jsoncompat.MarshalPython(record, false)
	if err != nil {
		return "", err
	}
	return sha(string(encoded)), nil
}

func scopedIdentifier(host, kind, value string) string {
	if kind == "" || value == "" {
		return ""
	}
	return fmt.Sprintf("%s-%s:%s", host, kind, sha(value)[:24])
}

type StatePayload struct {
	EventID         string   `json:"event_id"`
	EventType       string   `json:"event_type"`
	RunID           string   `json:"run_id"`
	StageID         string   `json:"stage_id"`
	SignalCodes     []string `json:"signal_codes"`
	CapabilityCodes []string `json:"capability_codes"`
	RetryCount      int      `json:"retry_count"`
}

func (event Event) HostStatePayload(schedulerEventType string) (StatePayload, error) {
	digest, err := event.Digest()
	if err != nil {
		return StatePayload{}, err
	}
	stage := scopedIdentifier(event.HostSlug, event.StageKind, event.StageID)
	if stage == "" {
		stage = event.HostSlug + "-session-boundary"
	}
	return StatePayload{EventID: event.HostSlug + "-host-state:" + digest,
		EventType: schedulerEventType,
		RunID:     event.HostSlug + "-session:" + sha(event.SessionID)[:24], StageID: stage,
		SignalCodes:     []string{event.HostSlug + "_event:" + event.EventName, fmt.Sprintf("schema:%d", event.SchemaVersion)},
		CapabilityCodes: []string{event.CapabilityCode, "typed_host_state"}, RetryCount: 0}, nil
}
