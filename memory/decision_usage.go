package memory

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"golang.org/x/text/unicode/norm"
)

type MemoryDecisionEvent struct {
	DecisionID              string        `json:"decision_id"`
	WorkspaceID             string        `json:"workspace_id"`
	RunID                   string        `json:"run_id"`
	TaskSignature           string        `json:"task_signature"`
	ActionTraceID           string        `json:"action_trace_id"`
	Action                  string        `json:"action"`
	CanonicalSnapshotSHA256 string        `json:"canonical_snapshot_sha256"`
	ContextSnapshotSHA256   string        `json:"context_snapshot_sha256"`
	LiveStateSnapshotSHA256 string        `json:"live_state_snapshot_sha256"`
	LiveTrigger             string        `json:"live_trigger"`
	CandidateIDs            []string      `json:"candidate_ids"`
	SelectedCandidateIDs    []string      `json:"selected_candidate_ids"`
	PolicyRevision          string        `json:"policy_revision"`
	CatalogRevision         string        `json:"catalog_revision"`
	CostTokens              int           `json:"cost_tokens"`
	CostLatencyMS           int           `json:"cost_latency_ms"`
	DownstreamOutcome       string        `json:"downstream_outcome"`
	EvidenceRefs            []EvidenceRef `json:"evidence_refs"`
	CreatedAt               string        `json:"created_at"`
}

type MemoryUsageEvent struct {
	EventID       string        `json:"event_id"`
	DecisionID    string        `json:"decision_id"`
	MemoryID      string        `json:"memory_id"`
	WorkspaceID   string        `json:"workspace_id"`
	TaskSignature string        `json:"task_signature"`
	ActionTraceID string        `json:"action_trace_id"`
	UsageType     string        `json:"usage_type"`
	Sequence      int           `json:"sequence"`
	EvidenceRefs  []EvidenceRef `json:"evidence_refs"`
	CreatedAt     string        `json:"created_at"`
}

type MemoryDecisionTrace struct {
	Decision             MemoryDecisionEvent `json:"decision"`
	UsageEvents          []MemoryUsageEvent  `json:"usage_events"`
	Channel              string              `json:"channel"`
	OrdinaryRecall       bool                `json:"ordinary_recall"`
	HostActionAuthorized bool                `json:"host_action_authorized"`
}

func normalizeDecisionIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || utf8.RuneCountInString(value) > 256 || seen[value] {
			return nil, errors.New("memory decision candidate IDs must be unique nonempty strings of at most 256 characters")
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func normalizeDecisionRefs(values []EvidenceRef) ([]EvidenceRef, error) {
	if len(values) == 0 {
		return nil, errors.New("memory decision event requires evidence_refs")
	}
	result := make([]EvidenceRef, 0, len(values))
	for _, ref := range values {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, nil
}

func validDecisionSHA(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func decisionCreatedAt(value string) string {
	value = norm.NFC.String(strings.TrimSpace(value))
	if value == "" {
		return time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
	return value
}

func (event MemoryDecisionEvent) Normalize() (MemoryDecisionEvent, error) {
	event.DecisionID = strings.TrimSpace(event.DecisionID)
	event.WorkspaceID = strings.TrimSpace(event.WorkspaceID)
	event.RunID = strings.TrimSpace(event.RunID)
	event.TaskSignature = strings.TrimSpace(event.TaskSignature)
	event.ActionTraceID = strings.TrimSpace(event.ActionTraceID)
	event.Action = strings.TrimSpace(event.Action)
	event.LiveTrigger = strings.TrimSpace(event.LiveTrigger)
	event.PolicyRevision = strings.TrimSpace(event.PolicyRevision)
	event.CatalogRevision = strings.TrimSpace(event.CatalogRevision)
	event.DownstreamOutcome = strings.TrimSpace(event.DownstreamOutcome)
	if event.DecisionID == "" || event.WorkspaceID == "" || event.RunID == "" ||
		event.TaskSignature == "" || event.ActionTraceID == "" || event.LiveTrigger == "" ||
		event.PolicyRevision == "" || event.CatalogRevision == "" || event.DownstreamOutcome == "" ||
		!oneOf(event.Action, "recall", "defer", "skip") ||
		!validDecisionSHA(event.CanonicalSnapshotSHA256) || !validDecisionSHA(event.ContextSnapshotSHA256) ||
		!validDecisionSHA(event.LiveStateSnapshotSHA256) || event.CostTokens < 0 || event.CostLatencyMS < 0 {
		return MemoryDecisionEvent{}, errors.New("memory decision identity, action, snapshots, or cost is invalid")
	}
	var err error
	event.CandidateIDs, err = normalizeDecisionIDs(event.CandidateIDs)
	if err != nil {
		return MemoryDecisionEvent{}, err
	}
	event.SelectedCandidateIDs, err = normalizeDecisionIDs(event.SelectedCandidateIDs)
	if err != nil {
		return MemoryDecisionEvent{}, err
	}
	available := make(map[string]bool, len(event.CandidateIDs))
	for _, id := range event.CandidateIDs {
		available[id] = true
	}
	for _, id := range event.SelectedCandidateIDs {
		if !available[id] {
			return MemoryDecisionEvent{}, errors.New("selected_candidate_ids must be available candidates")
		}
	}
	event.EvidenceRefs, err = normalizeDecisionRefs(event.EvidenceRefs)
	if err != nil {
		return MemoryDecisionEvent{}, err
	}
	event.CreatedAt = decisionCreatedAt(event.CreatedAt)
	return event, nil
}

func (event MemoryUsageEvent) Normalize() (MemoryUsageEvent, error) {
	event.EventID = strings.TrimSpace(event.EventID)
	event.DecisionID = strings.TrimSpace(event.DecisionID)
	event.MemoryID = strings.TrimSpace(event.MemoryID)
	event.WorkspaceID = strings.TrimSpace(event.WorkspaceID)
	event.TaskSignature = strings.TrimSpace(event.TaskSignature)
	event.ActionTraceID = strings.TrimSpace(event.ActionTraceID)
	event.UsageType = strings.TrimSpace(event.UsageType)
	if event.EventID == "" || event.DecisionID == "" || event.MemoryID == "" ||
		event.WorkspaceID == "" || event.TaskSignature == "" || event.ActionTraceID == "" ||
		!oneOf(event.UsageType, "candidate", "selected", "rendered", "cited", "applied", "not_applied", "blocked") ||
		event.Sequence < 0 {
		return MemoryUsageEvent{}, errors.New("memory usage identity, type, or sequence is invalid")
	}
	var err error
	event.EvidenceRefs, err = normalizeDecisionRefs(event.EvidenceRefs)
	if err != nil {
		return MemoryUsageEvent{}, err
	}
	event.CreatedAt = decisionCreatedAt(event.CreatedAt)
	return event, nil
}

func (trace MemoryDecisionTrace) Validate() error {
	decision, err := trace.Decision.Normalize()
	if err != nil {
		return err
	}
	if trace.Channel != "memory_decision_usage_trace" || trace.OrdinaryRecall || trace.HostActionAuthorized {
		return errors.New("memory decision trace channel is invalid")
	}
	seen := make(map[int]bool, len(trace.UsageEvents))
	previous := -1
	actionUse := make(map[string]string)
	for _, raw := range trace.UsageEvents {
		event, err := raw.Normalize()
		if err != nil {
			return err
		}
		if event.Sequence <= previous || seen[event.Sequence] || event.DecisionID != decision.DecisionID ||
			event.WorkspaceID != decision.WorkspaceID || event.TaskSignature != decision.TaskSignature ||
			event.ActionTraceID != decision.ActionTraceID {
			return errors.New("memory decision trace usage sequence or linkage is invalid")
		}
		if event.UsageType == "applied" || event.UsageType == "not_applied" {
			if actionUse[event.MemoryID] != "" && actionUse[event.MemoryID] != event.UsageType {
				return errors.New("memory decision trace contains contradictory action-use observations")
			}
			actionUse[event.MemoryID] = event.UsageType
		}
		seen[event.Sequence] = true
		previous = event.Sequence
	}
	return nil
}

func (event MemoryDecisionEvent) CanonicalJSON() ([]byte, error) {
	normalized, err := event.Normalize()
	if err != nil {
		return nil, err
	}
	if normalized.CandidateIDs == nil {
		normalized.CandidateIDs = []string{}
	}
	if normalized.SelectedCandidateIDs == nil {
		normalized.SelectedCandidateIDs = []string{}
	}
	return jsoncompat.MarshalPython(normalized, false)
}

func (event MemoryUsageEvent) CanonicalJSON() ([]byte, error) {
	normalized, err := event.Normalize()
	if err != nil {
		return nil, err
	}
	return jsoncompat.MarshalPython(normalized, false)
}
