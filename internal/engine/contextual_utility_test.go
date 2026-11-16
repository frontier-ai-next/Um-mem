package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestContextualUtilityRemainsShadowOnlyAndExactContext(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	request := ContextualUtilityRequest{MemoryID: "procedure-1", TaskFamily: "implementation",
		Stage: "test", ProjectID: "project-1", HostID: "host-1", ProfileID: "team",
		MinimumSupport: 2, MinimumIndependentOutcomes: 2}
	empty, err := selected.ContextualUtilitySummary(ctx, request)
	if err != nil || empty.SupportCount != 0 || empty.ShadowEligible {
		t.Fatalf("empty utility was treated as evidence: %+v, %v", empty, err)
	}
	db, err := sql.Open("sqlite", selected.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	proofHash := "ledger-proof"
	credits := []memory.OutcomeCreditEvent{
		{CreditID: "helped", Outcome: "helped", AttributionBasis: "deterministic_verifier",
			VerifierResult: "passed", IndependenceDomain: "run-a", ProfileID: "team",
			CreatedAt: "2026-10-24T12:00:00Z"},
		{CreditID: "hurt", Outcome: "hurt", AttributionBasis: "proven_regression",
			IndependenceDomain: "run-b", ProfileID: "team",
			CreatedAt: "2026-10-24T13:00:00Z"},
		{CreditID: "foreign", Outcome: "helped", AttributionBasis: "deterministic_verifier",
			VerifierResult: "passed", IndependenceDomain: "run-c", ProfileID: "other",
			CreatedAt: "2026-10-24T14:00:00Z"},
	}
	for _, credit := range credits {
		credit.DecisionID, credit.WorkspaceID = "decision-1", "alpha"
		credit.TaskSignature, credit.ActionTraceID = "task-1", "trace-1"
		credit.MemoryIDs, credit.AppliedUsageEventIDs = []string{"procedure-1"}, []string{"usage-1"}
		credit.TaskFamily, credit.Stage = "implementation", "test"
		credit.ProjectID, credit.HostID = "project-1", "host-1"
		credit.Reason = "verified application"
		credit.EvidenceRefs = []memory.EvidenceRef{{RefType: "verification_report",
			RefID: "report-1", Hash: &proofHash, ProducedBy: "verifier", ProducerID: "verifier-1"}}
		if err := credit.Validate(); err != nil {
			t.Fatal(err)
		}
		payload, err := jsoncompat.MarshalPython(credit, false)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(payload)
		envelope, err := json.Marshal(map[string]any{"payload": credit,
			"payload_sha256": hex.EncodeToString(hash[:])})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO usage(block_id,workspace_id,outcome,payload_json)
			VALUES(?,?,?,?)`, "outcome-credit-event:"+credit.CreditID, "alpha",
			"outcome_credit", string(envelope)); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := selected.ContextualUtilitySummary(ctx, request)
	if err != nil || summary.HelpedCount != 1 || summary.HurtCount != 1 ||
		summary.SupportCount != 2 || summary.OutcomeDiversity != 2 ||
		!summary.ShadowEligible || summary.ShadowUtility != 0 ||
		summary.RankingMode != "shadow_only" || summary.AffectsAuthority ||
		summary.OldestCreditAt != "2026-10-24T12:00:00Z" ||
		summary.NewestCreditAt != "2026-10-24T13:00:00Z" {
		t.Fatalf("exact-context shadow summary changed: %+v, %v", summary, err)
	}
}
