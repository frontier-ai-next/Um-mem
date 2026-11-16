package memory

import (
	"errors"
	"reflect"
	"slices"
	"strings"
)

type MemoryOutcomeReportEntry struct {
	MemoryID      string        `json:"memory_id"`
	Disposition   string        `json:"disposition"`
	UsageEventIDs []string      `json:"usage_event_ids"`
	CreditIDs     []string      `json:"credit_ids"`
	Reason        string        `json:"reason"`
	EvidenceRefs  []EvidenceRef `json:"evidence_refs"`
}

type MemoryOutcomeReport struct {
	ReportID             string                     `json:"report_id"`
	WorkspaceID          string                     `json:"workspace_id"`
	DecisionID           string                     `json:"decision_id"`
	TaskSignature        string                     `json:"task_signature"`
	ActionTraceID        string                     `json:"action_trace_id"`
	Entries              []MemoryOutcomeReportEntry `json:"entries"`
	Channel              string                     `json:"channel"`
	OrdinaryRecall       bool                       `json:"ordinary_recall"`
	HostActionAuthorized bool                       `json:"host_action_authorized"`
	AffectsAuthority     bool                       `json:"affects_authority"`
}

func uniqueOutcomeEvidence(values []EvidenceRef) []EvidenceRef {
	result := make([]EvidenceRef, 0, len(values))
	for _, ref := range values {
		found := false
		for _, existing := range result {
			if reflect.DeepEqual(existing, ref) {
				found = true
				break
			}
		}
		if !found {
			result = append(result, ref)
		}
	}
	return result
}

// BuildMemoryOutcomeReport gives each candidate an evidence-bounded outcome.
// A missing action-use callback remains unknown, never inferred nonuse.
func BuildMemoryOutcomeReport(reportID string, trace MemoryDecisionTrace,
	credits []OutcomeCreditEvent) (MemoryOutcomeReport, error) {
	if strings.TrimSpace(reportID) == "" {
		return MemoryOutcomeReport{}, errors.New("report_id is required")
	}
	if err := trace.Validate(); err != nil {
		return MemoryOutcomeReport{}, err
	}
	decision := trace.Decision
	for _, credit := range credits {
		if err := credit.Validate(); err != nil {
			return MemoryOutcomeReport{}, err
		}
		if credit.DecisionID != decision.DecisionID || credit.WorkspaceID != decision.WorkspaceID ||
			credit.TaskSignature != decision.TaskSignature || credit.ActionTraceID != decision.ActionTraceID {
			return MemoryOutcomeReport{}, errors.New("operator report credit linkage is invalid")
		}
	}
	if len(decision.CandidateIDs) == 0 {
		return MemoryOutcomeReport{}, errors.New("memory outcome report requires entries")
	}
	report := MemoryOutcomeReport{ReportID: strings.TrimSpace(reportID), WorkspaceID: decision.WorkspaceID,
		DecisionID: decision.DecisionID, TaskSignature: decision.TaskSignature, ActionTraceID: decision.ActionTraceID,
		Entries: make([]MemoryOutcomeReportEntry, 0, len(decision.CandidateIDs)), Channel: "memory_outcome_operator_report"}
	for _, memoryID := range decision.CandidateIDs {
		usageEvents := make([]MemoryUsageEvent, 0)
		for _, event := range trace.UsageEvents {
			if event.MemoryID == memoryID {
				usageEvents = append(usageEvents, event)
			}
		}
		matchingCredits := make([]OutcomeCreditEvent, 0)
		for _, credit := range credits {
			if slices.Contains(credit.MemoryIDs, memoryID) {
				matchingCredits = append(matchingCredits, credit)
			}
		}
		entry := MemoryOutcomeReportEntry{MemoryID: memoryID, UsageEventIDs: []string{}, CreditIDs: []string{}, EvidenceRefs: []EvidenceRef{}}
		applied, notApplied := false, false
		for _, event := range usageEvents {
			entry.UsageEventIDs = append(entry.UsageEventIDs, event.EventID)
			entry.EvidenceRefs = append(entry.EvidenceRefs, event.EvidenceRefs...)
			if event.UsageType == "applied" {
				applied = true
			}
			if event.UsageType == "not_applied" {
				notApplied = true
			}
		}
		var hurt, helped *OutcomeCreditEvent
		for i := range matchingCredits {
			credit := &matchingCredits[i]
			entry.CreditIDs = append(entry.CreditIDs, credit.CreditID)
			entry.EvidenceRefs = append(entry.EvidenceRefs, credit.EvidenceRefs...)
			if credit.Outcome == "hurt" && hurt == nil {
				hurt = credit
			}
			if credit.Outcome == "helped" && helped == nil {
				helped = credit
			}
		}
		switch {
		case hurt != nil:
			entry.Disposition, entry.Reason = "hurt", hurt.Reason
		case helped != nil:
			entry.Disposition, entry.Reason = "helped", helped.Reason
		case !applied && notApplied:
			entry.Disposition, entry.Reason = "unnecessary", "completed action-use observation explicitly records nonuse"
		case !applied:
			entry.Disposition, entry.Reason = "unknown", "action-use observation is incomplete; absent applied events do not prove nonuse"
		default:
			entry.Disposition, entry.Reason = "neutral", "applied record has no explicit verified causal outcome"
		}
		if len(usageEvents) == 0 && len(matchingCredits) == 0 {
			entry.EvidenceRefs = append(entry.EvidenceRefs, decision.EvidenceRefs...)
		}
		entry.EvidenceRefs = uniqueOutcomeEvidence(entry.EvidenceRefs)
		if len(entry.EvidenceRefs) == 0 {
			return MemoryOutcomeReport{}, errors.New("memory outcome report entry requires evidence_refs")
		}
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}
