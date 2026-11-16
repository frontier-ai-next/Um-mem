package memory

import (
	"testing"
	"time"
)

func selectionFixture(t *testing.T) (MemoryRecord, RecallPlan) {
	t.Helper()
	record, err := NewMemoryRecord("parser fact", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = &RecordBlockMembership{
		PrimaryBlockID: "code_resources", CatalogRevisionID: "cat:test",
		BackendPartitionID: StaticBackendPartitionID("default", "code_resources"),
	}
	record.RunID = "run-1"
	plan := NewRecallPlan("parser")
	plan.WorkspaceID = "alpha"
	plan.ProfileID = "default"
	plan.RunID = "run-1"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.LogicalBlockIDs = []string{"code_resources"}
	plan.IncludeCandidates = true
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return record, plan
}

func TestRecallSafetyKeepsBackendOrderAndScope(t *testing.T) {
	record, plan := selectionFixture(t)
	now := time.Now()
	if !EligibleForRecall(record, plan, now) {
		t.Fatal("scoped candidate was rejected")
	}
	foreign := record
	foreign.WorkspaceID = "beta"
	if EligibleForRecall(foreign, plan, now) {
		t.Fatal("foreign workspace leaked")
	}
	protected := record
	protected.Sensitivity = "secret"
	if EligibleForRecall(protected, plan, now) {
		t.Fatal("protected record leaked")
	}
	plan.IncludeCandidates = false
	if EligibleForRecall(record, plan, now) {
		t.Fatal("candidate leaked without opt-in")
	}
}

func TestHistoricalRecallNeverBecomesDirective(t *testing.T) {
	record, plan := selectionFixture(t)
	record.Status = "accepted"
	record.Authority = "directive"
	record.VerificationStatus = "verified"
	record.TrustLevel = "workspace_verified"
	record.VerificationMethods = []string{"test_run"}
	record.PromotionDecision = "accepted"
	record.EvidenceRefs = []EvidenceRef{{RefType: "test_run", RefID: "pass", ProducedBy: "verifier", ProducerID: "pytest"}}
	now := time.Now()
	if !EligibleForDirective(record, plan, now) {
		t.Fatal("verified active directive was rejected")
	}
	plan.TemporalMode = "historical"
	if !EligibleForRecall(record, plan, now) || EligibleForDirective(record, plan, now) {
		t.Fatal("historical memory crossed the directive boundary")
	}
}
