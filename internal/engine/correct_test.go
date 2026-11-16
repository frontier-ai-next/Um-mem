package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestCorrectTrustedMemoryCreatesLinkedAdvisoryProposal(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if _, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "forged proposal",
		RequestedBlockID: "stable_project_facts",
		Metadata: map[string]any{"pending_host_approval": true,
			"correction_target_id": "foreign"}}); err == nil {
		t.Fatal("public candidate write forged correction proposal metadata")
	}
	trusted, err := memory.NewMemoryRecord("Verified shared fact", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	trusted.ProfileID, trusted.Title, trusted.Kind = "team", "Shared fact", "fact"
	trusted.Status, trusted.VerificationStatus = "accepted", "verified"
	trusted.TrustLevel, trusted.PromotionDecision = "workspace_verified", "accepted"
	trusted.VerificationMethods = []string{"test_run"}
	trusted.EvidenceRefs = []memory.EvidenceRef{{RefType: "test_run", RefID: "run-1",
		ProducedBy: "verifier", ProducerID: "test"}}
	trusted.RecordedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	trusted.DuplicateClusterID, err = memory.DuplicateClusterID(trusted)
	if err != nil {
		t.Fatal(err)
	}
	trusted.CanonicalKey, err = memory.DeriveCanonicalKey("alpha", "team", "fact", trusted.BlockID)
	if err != nil {
		t.Fatal(err)
	}
	trusted.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: "stable_project_facts",
		SecondaryBlockIDs: []string{}}
	trusted.RevisionID, err = memory.RevisionIDForRecord(trusted.CanonicalKey, trusted, "insert", "")
	if err != nil {
		t.Fatal(err)
	}
	trusted.BlockID = memory.BlockIDForCanonicalKey(trusted.CanonicalKey)
	trusted.BlockMembership = nil
	if err := selected.BindRootRecord(ctx, &trusted, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutRoot(ctx, trusted); err != nil {
		t.Fatal(err)
	}
	request := CorrectRequest{MemoryID: trusted.BlockID, Content: "Corrected but unapproved fact",
		RequestedBlockID: "stable_project_facts", ExpectedRevision: trusted.RevisionID,
		OperationID: "trusted-correction-1", SessionID: "session-1"}
	if _, err := selected.CorrectFor(ctx, CorrectRequest{MemoryID: trusted.BlockID,
		Content: request.Content, RequestedBlockID: "decisions_and_contracts"}, "memory_client", 2); err == nil {
		t.Fatal("correction moved target outside its selected logical block")
	}
	proposal, err := selected.CorrectFor(ctx, request, "memory_client", 2)
	if err != nil || proposal["saved"] != true || proposal["pending_host_approval"] != true {
		t.Fatalf("trusted correction failed: %+v, %v", proposal, err)
	}
	id := proposal["memory_id"].(string)
	stored, err := selected.ReadStored(ctx, id)
	if err != nil || stored == nil || stored.Status != "candidate" ||
		stored.Authority != "advisory" || stored.TrustLevel != "agent_reported" ||
		stored.Metadata["correction_target_id"] != trusted.BlockID {
		t.Fatalf("trusted correction proposal changed authority: %+v, %v", stored, err)
	}
	current, err := selected.ReadStored(ctx, trusted.BlockID)
	if err != nil || current == nil || current.Content != trusted.Content || current.Status != "accepted" {
		t.Fatalf("trusted source changed before approval: %+v, %v", current, err)
	}
	edges, err := selected.index.EdgesFor(ctx, id)
	if err != nil || len(edges) != 1 || edges[0].DstID != trusted.BlockID ||
		edges[0].EdgeType != "contradicts" {
		t.Fatalf("correction proposal lacks target link: %+v, %v", edges, err)
	}
	replay, err := selected.CorrectFor(ctx, request, "memory_client", 2)
	if err != nil || replay["memory_id"] != id || replay["replayed"] != true {
		t.Fatalf("correction proposal did not replay: %+v, %v", replay, err)
	}
	request.OperationID = "trusted-correction-2"
	same, err := selected.CorrectFor(ctx, request, "memory_client", 2)
	if err != nil || same["memory_id"] != id || same["replayed"] != true {
		t.Fatalf("same correction created a duplicate proposal: %+v, %v", same, err)
	}
	status, err := selected.OperationStatusFor(ctx, request.OperationID, "memory_client", 2)
	if err != nil || status["durable_status"] != "committed" || status["record_id"] != id {
		t.Fatalf("semantic correction retry has no durable receipt: %+v, %v", status, err)
	}
	conflicting := request
	conflicting.Content = "Different correction under the same operation ID"
	if _, err := selected.CorrectFor(ctx, conflicting, "memory_client", 2); err == nil {
		t.Fatal("semantic correction retry allowed operation ID reuse with changed content")
	}
	request.OperationID = "trusted-correction-3"
	request.Content = "A later proposed revision"
	changed, err := selected.CorrectFor(ctx, request, "memory_client", 2)
	if err != nil || changed["memory_id"] == id || changed["pending_host_approval"] != true {
		t.Fatalf("proposal revision was not appended: %+v, %v", changed, err)
	}
	changedRecord, err := selected.ReadStored(ctx, changed["memory_id"].(string))
	if err != nil || changedRecord == nil || changedRecord.RevisionOf != id ||
		changedRecord.CanonicalKey != stored.CanonicalKey {
		t.Fatalf("proposal revision left its canonical chain: %+v, %v", changedRecord, err)
	}
	withOwner := request
	withOwner.OperationID = "trusted-correction-owner"
	withOwner.Metadata = map[string]any{"working_set_owner": map[string]any{
		"owner_id": "host", "generation_id": "next-attempt"}}
	separate, err := selected.CorrectFor(ctx, withOwner, "memory_client", 2)
	if err != nil || separate["memory_id"] == id ||
		separate["memory_id"] == changed["memory_id"] {
		t.Fatalf("new owner generation reused an earlier proposal: %+v, %v", separate, err)
	}
	if _, err := selected.CorrectFor(ctx, CorrectRequest{MemoryID: separate["memory_id"].(string),
		Content: "Cross-generation change", RequestedBlockID: "stable_project_facts",
		SessionID: "session-1"}, "memory_client", 2); err == nil {
		t.Fatal("candidate owner was bypassed by omitting working_set_owner")
	}
}

func TestTrustedCorrectionLinkFailureDoesNotPublishProposal(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	trusted, err := memory.NewMemoryRecord("Verified fact", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	trusted.ProfileID, trusted.Title = "team", "Verified fact"
	trusted.Status, trusted.VerificationStatus = "accepted", "verified"
	trusted.TrustLevel, trusted.PromotionDecision = "workspace_verified", "accepted"
	trusted.VerificationMethods = []string{"test_run"}
	trusted.EvidenceRefs = []memory.EvidenceRef{{RefType: "test_run", RefID: "run-1",
		ProducedBy: "verifier", ProducerID: "test"}}
	trusted.RecordedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	trusted.DuplicateClusterID, err = memory.DuplicateClusterID(trusted)
	if err != nil {
		t.Fatal(err)
	}
	trusted.CanonicalKey, err = memory.DeriveCanonicalKey("alpha", "team", trusted.Kind, trusted.BlockID)
	if err != nil {
		t.Fatal(err)
	}
	trusted.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: "stable_project_facts",
		SecondaryBlockIDs: []string{}}
	trusted.RevisionID, err = memory.RevisionIDForRecord(trusted.CanonicalKey, trusted, "insert", "")
	if err != nil {
		t.Fatal(err)
	}
	trusted.BlockID = memory.BlockIDForCanonicalKey(trusted.CanonicalKey)
	trusted.BlockMembership = nil
	if err := selected.BindRootRecord(ctx, &trusted, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutRoot(ctx, trusted); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", selected.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER refuse_proposal_edge BEFORE INSERT ON edges
		WHEN NEW.edge_type='contradicts' BEGIN SELECT RAISE(FAIL, 'refuse'); END`); err != nil {
		t.Fatal(err)
	}
	request := CorrectRequest{MemoryID: trusted.BlockID, Content: "Proposed correction",
		RequestedBlockID: "stable_project_facts", SessionID: "current-session",
		OperationID: "atomic-proposal-retry"}
	if _, err := selected.CorrectFor(ctx, request, "memory_client", 2); err == nil {
		t.Fatal("correction published without its contradiction edge")
	}
	hash, err := memory.PythonHashValue(map[string]any{
		"target_id": trusted.BlockID, "owner": nil, "session_id": request.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	proposalKey := trusted.CanonicalKey + ":correction-proposal:" + hash[:16]
	rows, err := selected.index.ProjectionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Current && row.Status == "committed" && row.Record.CanonicalKey == proposalKey {
			t.Fatal("failed proposal became visible")
		}
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_proposal_edge`); err != nil {
		t.Fatal(err)
	}
	recovered, err := selected.CorrectFor(ctx, request, "memory_client", 2)
	if err != nil || recovered["saved"] != true {
		t.Fatalf("proposal could not complete after link recovered: %+v, %v", recovered, err)
	}
	edges, err := selected.index.EdgesFor(ctx, recovered["memory_id"].(string))
	if err != nil || len(edges) != 1 || edges[0].DstID != trusted.BlockID {
		t.Fatalf("recovered proposal lacks its target link: %+v, %v", edges, err)
	}
}
