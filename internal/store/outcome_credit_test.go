package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestOutcomeCreditsForMemoryChecksPythonJournalHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	proofHash := "ledger-proof"
	credit := memory.OutcomeCreditEvent{CreditID: "credit-1", DecisionID: "decision-1",
		WorkspaceID: "alpha", TaskSignature: "task-1", ActionTraceID: "trace-1",
		MemoryIDs: []string{"procedure-1"}, AppliedUsageEventIDs: []string{"usage-1"},
		Outcome: "helped", AttributionBasis: "deterministic_verifier", VerifierResult: "passed",
		TaskFamily: "implementation", Stage: "test", ProjectID: "project-1", HostID: "host-1",
		ProfileID: "team", IndependenceDomain: "domain-1", Reason: "verified application",
		CreatedAt: "2026-10-24 12:00:00+00:00",
		EvidenceRefs: []memory.EvidenceRef{{RefType: "verification_report", RefID: "report-1",
			Hash: &proofHash, ProducedBy: "verifier", ProducerID: "verifier-1"}}}
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
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO usage(block_id,workspace_id,outcome,payload_json)
		VALUES(?,?,?,?)`, "outcome-credit-event:credit-1", "alpha", "outcome_credit", string(envelope)); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	credits, err := index.OutcomeCreditsForMemory(context.Background(), "procedure-1")
	if err != nil || len(credits) != 1 || credits[0].CreditID != "credit-1" {
		t.Fatalf("Python-shaped outcome journal did not decode: %+v, %v", credits, err)
	}
	if _, err := db.Exec(`UPDATE usage SET payload_json=? WHERE block_id=?`,
		`{"payload":{"credit_id":"forged","memory_ids":["procedure-1"]},"payload_sha256":"bad"}`,
		"outcome-credit-event:credit-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := index.OutcomeCreditsForMemory(context.Background(), "procedure-1"); err == nil {
		t.Fatal("forged outcome journal hash was accepted")
	}
}
