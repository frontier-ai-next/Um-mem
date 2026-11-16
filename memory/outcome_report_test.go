package memory

import (
	"strings"
	"testing"
)

func TestOperatorReportKeepsMissingActionUseUnknown(t *testing.T) {
	hash := strings.Repeat("a", 64)
	ref := EvidenceRef{RefType: "test_run", RefID: "test-1", Hash: &hash, ProducedBy: "verifier", ProducerID: "verifier-1"}
	decision := MemoryDecisionEvent{DecisionID: "decision-1", WorkspaceID: "ws", RunID: "run-1",
		TaskSignature: "repair:test", ActionTraceID: "trace-1", Action: "recall",
		CanonicalSnapshotSHA256: "sha256:" + hash, ContextSnapshotSHA256: "sha256:" + strings.Repeat("b", 64),
		LiveStateSnapshotSHA256: "sha256:" + strings.Repeat("c", 64), LiveTrigger: "test_state:failed",
		CandidateIDs: []string{"memory-1", "memory-2"}, SelectedCandidateIDs: []string{"memory-1"},
		PolicyRevision: "policy-1", CatalogRevision: "catalog-1", DownstreamOutcome: "pending",
		EvidenceRefs: []EvidenceRef{ref}, CreatedAt: "2026-10-24T12:00:00Z"}
	trace := MemoryDecisionTrace{Decision: decision, UsageEvents: []MemoryUsageEvent{}, Channel: "memory_decision_usage_trace"}
	report, err := BuildMemoryOutcomeReport("operator-report:decision-1", trace, nil)
	if err != nil || len(report.Entries) != 2 || report.Entries[0].Disposition != "unknown" ||
		report.Entries[1].Disposition != "unknown" || report.HostActionAuthorized || report.AffectsAuthority {
		t.Fatalf("missing callbacks were treated as nonuse: %+v %v", report, err)
	}
	trace.UsageEvents = []MemoryUsageEvent{{EventID: "usage-1", DecisionID: decision.DecisionID, MemoryID: "memory-1",
		WorkspaceID: "ws", TaskSignature: decision.TaskSignature, ActionTraceID: decision.ActionTraceID,
		UsageType: "not_applied", Sequence: 0, EvidenceRefs: []EvidenceRef{ref}, CreatedAt: "2026-10-24T12:00:01Z"}}
	report, err = BuildMemoryOutcomeReport("operator-report:decision-1", trace, nil)
	if err != nil || report.Entries[0].Disposition != "unnecessary" || report.Entries[1].Disposition != "unknown" {
		t.Fatalf("explicit nonuse did not remain distinct: %+v %v", report, err)
	}
}
