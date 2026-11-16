package memory

import (
	"reflect"
	"testing"
)

func TestConsolidationScheduleMatchesPythonFixture(t *testing.T) {
	// IDs below come from the repository's Python ConsolidationScheduler for
	// these two signals and window="controlled".
	refs := []EvidenceRef{{RefType: "test_run", RefID: "fixture",
		ProducedBy: "verifier", ProducerID: "go-port-test"}}
	a := ConsolidationSignal{WorkspaceID: "alpha", SourceEpisodeIDs: []string{"episode-b", "episode-a"},
		GroupingKey: "host", TaskFamily: "release", Trigger: "session_boundary",
		ProposedAction: "consolidate", ExpectedApplicability: "same workspace",
		Risk: "low", CostBudgetTokens: 1024, Rationale: "verified closure",
		EvidenceRefs: refs, ExpectedValue: 1, EstimatedCostTokens: 32,
		DependenciesClosed: true, RecurrenceCount: 2}
	b := ConsolidationSignal{WorkspaceID: "alpha", SourceEpisodeIDs: []string{"episode-c"},
		GroupingKey: "host", TaskFamily: "release", Trigger: "recurrence",
		ProposedAction: "defer", ExpectedApplicability: "same workspace",
		Risk: "critical", CostBudgetTokens: 256, Rationale: "single sighting",
		EvidenceRefs: refs, RareCritical: true, ExpectedValue: 0.7,
		AgeSeconds: 80, EstimatedCostTokens: 16, DependenciesClosed: true,
		RecurrenceCount: 1}
	baseline, err := BuildConsolidationSchedule([]ConsolidationSignal{a, b, a}, "controlled")
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := BuildConsolidationSchedule([]ConsolidationSignal{b, a}, "controlled")
	if err != nil || !reflect.DeepEqual(baseline, reordered) {
		t.Fatalf("schedule depended on input order or duplicates: %+v, %v", reordered, err)
	}
	if baseline.InputSetFingerprintSHA256 != "sha256:7be2bba0b20899186426692fc23a5b2b7eba3bf9545e05abd7fb25643a3acca4" ||
		baseline.ScheduleID != "schedule-c15b4eaaf0d350e79c8de0bc" ||
		len(baseline.Entries) != 2 ||
		baseline.Entries[0].Decision.DecisionID != "consolidation-b8118f8f4922440b7b418ea9" ||
		baseline.Entries[1].Decision.DecisionID != "consolidation-b7523e04729356c911584a6c" {
		t.Fatalf("Go consolidation identity differs from Python: %+v", baseline)
	}
	if baseline.Entries[0].Reason != "rare_critical_bypass" ||
		baseline.Entries[0].PriorityScore != 14075 ||
		baseline.Entries[1].PriorityScore != 1100 ||
		baseline.CanonicalMutationApplied || baseline.HostActionAuthorized ||
		baseline.Entries[0].Decision.HostActionAuthorized {
		t.Fatalf("Go scheduler changed priority or authorized a write: %+v", baseline)
	}
}

func TestConsolidationScheduleDefersUnclosedDependencies(t *testing.T) {
	signal := ConsolidationSignal{WorkspaceID: "alpha", SourceEpisodeIDs: []string{"episode-a"},
		GroupingKey: "host", TaskFamily: "release", Trigger: "recurrence",
		ProposedAction: "defer", ExpectedApplicability: "same workspace",
		Risk: "critical", CostBudgetTokens: 256, Rationale: "not ready",
		EvidenceRefs: []EvidenceRef{{RefType: "test_run", RefID: "fixture",
			ProducedBy: "verifier", ProducerID: "go-port-test"}},
		RareCritical: true, ExpectedValue: 0.7, EstimatedCostTokens: 16,
		DependenciesClosed: false, RecurrenceCount: 1}
	schedule, err := BuildConsolidationSchedule([]ConsolidationSignal{signal}, "controlled")
	if err != nil || len(schedule.Entries) != 1 ||
		schedule.Entries[0].Disposition != "deferred" ||
		schedule.Entries[0].Reason != "dependency_closure_incomplete" {
		t.Fatalf("unclosed dependencies were scheduled: %+v, %v", schedule, err)
	}
	decisions, err := ScheduleConsolidation([]ConsolidationSignal{signal})
	if err != nil || len(decisions) != 0 {
		t.Fatalf("immediate schedule executed deferred signal: %+v, %v", decisions, err)
	}
	signal.DependenciesClosed = true
	decisions, err = ScheduleConsolidation([]ConsolidationSignal{signal})
	if err != nil || len(decisions) != 1 || decisions[0].Status != "proposed" ||
		decisions[0].HostActionAuthorized {
		t.Fatalf("eligible signal did not produce a non-authorizing decision: %+v, %v", decisions, err)
	}
}

func TestEmptyConsolidationScheduleMatchesPython(t *testing.T) {
	schedule, err := BuildConsolidationSchedule(nil, "controlled")
	if err != nil || schedule.ScheduleID != "schedule-d01917ed0a54ca3cb0b92056" ||
		schedule.InputSetFingerprintSHA256 != "sha256:4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945" ||
		len(schedule.Entries) != 0 || schedule.HostActionAuthorized {
		t.Fatalf("empty schedule differs from Python: %+v, %v", schedule, err)
	}
}
