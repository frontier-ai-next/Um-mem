package memory

import (
	"slices"
	"strings"
	"time"
)

func metadataTruthy(value any) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return oneOf(strings.ToLower(strings.TrimSpace(toString(value))), "1", "true", "yes", "on")
}

func toString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func (r MemoryRecord) isIndexOnly() bool {
	return r.Kind == "skill_index" || metadataTruthy(r.Metadata["index_only"]) ||
		metadataTruthy(r.Metadata["is_index_only"]) || metadataTruthy(r.Metadata["routing_only"])
}

func (r MemoryRecord) crossRunCandidateAllowed() bool {
	evidenceKind := strings.TrimSpace(toString(r.Metadata["evidence_kind"]))
	backed := oneOf(evidenceKind, "agent_tool_claim", "user_statement", "user_stated") ||
		evidenceKind == "tool_output" && slices.ContainsFunc(r.EvidenceRefs, EvidenceRef.IsHarnessToolReceipt)
	return backed &&
		r.Retention == "durable" && oneOf(r.Scope, "workspace", "project", "user_global") &&
		r.Authority == "advisory" && r.TrustLevel == "agent_reported" &&
		r.VerificationStatus == "unverified" && r.PromotionDecision == "not_requested" &&
		oneOf(r.DeliveryPolicy, "working_set", "on_demand")
}

func parseRecordTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func (r MemoryRecord) validityMatches(plan RecallPlan, now time.Time) bool {
	historical := plan.TemporalMode == "historical" || plan.KnownAsOf != ""
	if historical && plan.AsOf == "" {
		return true
	}
	bound := now.UTC()
	if plan.AsOf != "" {
		parsed, ok := parseRecordTime(plan.AsOf)
		if !ok {
			return false
		}
		bound = parsed
	}
	if expiry, ok := parseRecordTime(r.ExpiresAt); ok && !expiry.After(bound) {
		return false
	}
	if start, ok := parseRecordTime(r.ValidFrom); ok && start.After(bound) {
		return false
	}
	if end, ok := parseRecordTime(r.ValidTo); ok && !bound.Before(end) {
		return false
	}
	return true
}

func inStrings(value string, values []string) bool {
	for _, item := range values {
		if value == item {
			return true
		}
	}
	return false
}

// EligibleForRecall is the safety filter applied after a selected backend has
// returned its native-ranked candidates. It never grants authority.
func EligibleForRecall(record MemoryRecord, plan RecallPlan, now time.Time) bool {
	if err := record.ValidateSafety(); err != nil {
		return false
	}
	if record.WorkspaceID != plan.WorkspaceID || record.ProfileID != plan.ProfileID || record.IsProtected() || record.isIndexOnly() {
		return false
	}
	scopeKey, err := record.ScopeKey()
	if err != nil || !inStrings(scopeKey, plan.ScopeChain) {
		return false
	}
	historical := plan.TemporalMode == "historical" || plan.KnownAsOf != ""
	if oneOf(record.Status, "quarantined", "superseded", "deprecated", "retracted") && !historical {
		return false
	}
	if record.Status == "candidate" {
		if !plan.IncludeCandidates || (record.RunID != plan.RunID && !record.crossRunCandidateAllowed()) {
			return false
		}
	}
	if !record.validityMatches(plan, now) || record.DeliveryPolicy == "hidden" {
		return false
	}
	switch plan.SourceHorizon {
	case "archive":
	case "durable":
		if record.Retention == "archive" {
			return false
		}
	case "working":
		if !oneOf(record.Retention, "ephemeral", "session") && !oneOf(record.DeliveryPolicy, "always_on", "working_set") {
			return false
		}
	default:
		return false
	}
	if len(plan.RecordIDs) > 0 && !inStrings(record.BlockID, plan.RecordIDs) {
		return false
	}
	if len(plan.LogicalBlockIDs) > 0 && (record.BlockMembership == nil || !inStrings(record.BlockMembership.PrimaryBlockID, plan.LogicalBlockIDs)) {
		return false
	}
	return true
}

func EligibleForDirective(record MemoryRecord, plan RecallPlan, now time.Time) bool {
	if plan.TemporalMode == "historical" || plan.KnownAsOf != "" || !EligibleForRecall(record, plan, now) {
		return false
	}
	return record.Authority == "directive" && record.Status == "accepted" &&
		record.VerificationStatus == "verified" && len(record.VerificationMethods) > 0 &&
		oneOf(record.TrustLevel, "workspace_verified", "external_verified", "supervisor_verified", "human_approved", "system_curated") &&
		hasDecisiveEvidence(record.EvidenceRefs) && record.PromotionDecision == "accepted" &&
		oneOf(record.DeliveryPolicy, "always_on", "working_set", "on_demand")
}
