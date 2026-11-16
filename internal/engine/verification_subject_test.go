package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestVerificationSubjectBindsRevisionEvidenceAndScope(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Candidate to verify",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	record, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || record == nil {
		t.Fatal(err)
	}
	request := VerificationSubjectRequest{MemoryID: record.BlockID,
		ExpectedSourceRevision: record.RevisionID}
	subject, err := selected.VerificationSubject(ctx, request)
	if err != nil || subject.MemoryID != record.BlockID ||
		subject.RevisionID != record.RevisionID || subject.Scope != "workspace" ||
		subject.ScopeID != "alpha" || subject.Authority != "advisory" ||
		subject.TargetBlockID != "stable_project_facts" || len(subject.PayloadHash) != 64 {
		t.Fatalf("verification subject lost a bound axis: %+v, %v", subject, err)
	}
	emptyHash := sha256.Sum256([]byte("[]"))
	if subject.SourceEvidenceHash != hex.EncodeToString(emptyHash[:]) {
		t.Fatalf("empty source evidence was not hashed as a Python list: %+v", subject)
	}
	stale := request
	stale.ExpectedSourceRevision = "stale"
	if _, err := selected.VerificationSubject(ctx, stale); err == nil {
		t.Fatal("stale source revision was accepted")
	}
	moved := request
	moved.TargetScope, moved.TargetScopeID = "project", "project-1"
	moved.Lineage.ProjectID = "project-2"
	if _, err := selected.VerificationSubject(ctx, moved); err == nil {
		t.Fatal("target scope ID escaped caller lineage")
	}
	moved.TargetScopeID = "project-2"
	projectSubject, err := selected.VerificationSubject(ctx, moved)
	if err != nil || projectSubject.Scope != "project" || projectSubject.ScopeID != "project-2" {
		t.Fatalf("bound target scope was not selected: %+v, %v", projectSubject, err)
	}
	badTarget := request
	badTarget.TargetBlockID = "unknown-block"
	if _, err := selected.VerificationSubject(ctx, badTarget); err == nil {
		t.Fatal("unknown publication block was accepted")
	}
	projectTarget := request
	projectTarget.TargetBlockID = "project_scope_decisions_and_contracts"
	if _, err := selected.VerificationSubject(ctx, projectTarget); err == nil {
		t.Fatal("project target was accepted without bound project lineage")
	}
	projectTarget.Lineage.ProjectID = "project-2"
	projectBound, err := selected.VerificationSubject(ctx, projectTarget)
	if err != nil || projectBound.Scope != "project" ||
		projectBound.ScopeID != "project-2" {
		t.Fatalf("target block scope was not bound to lineage: %+v, %v", projectBound, err)
	}
}

func TestVerificationSubjectPayloadHashMatchesPythonFixture(t *testing.T) {
	payload, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(payload)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := memory.PythonHashValue(record)
	if err != nil || hash != "f594e54eced6cf5c01c26ba267d40ab7ea188592a014e735fd8a7f04a3e85190" {
		t.Fatalf("verification source payload hash differs from Python: %s, %v", hash, err)
	}
}
