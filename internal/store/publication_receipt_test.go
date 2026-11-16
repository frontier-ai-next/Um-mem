package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestPublicationSuccessorReceiptCommitsAtomicallyAndReplays(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	root.CanonicalKey = "alpha:observation:publication"
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	rootStage, err := writer.PrepareRootWrite(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, rootStage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	witness := root
	witness.BlockID = "mem_publication_witness"
	witness.RevisionID = "rev_publication_witness"
	witness.CanonicalKey = "alpha:observation:publication-witness"
	witnessMembership := *root.BlockMembership
	witnessMembership.RecordID = witness.BlockID
	witnessMembership.RecordRevisionID = witness.RevisionID
	witnessMembership.MembershipRevisionID = witnessMembership.RevisionHash()
	witness.BlockMembership = &witnessMembership
	witnessStage, err := writer.PrepareRootWrite(ctx, witness)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, witnessStage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AddEdge(ctx, memory.EdgeRecord{SrcID: root.BlockID,
		DstID: witness.BlockID, EdgeType: "derived_from"}); err != nil {
		t.Fatal(err)
	}

	accepted := root
	accepted.BlockID = "mem_published"
	accepted.RevisionID = "rev_published"
	accepted.RevisionOf = root.BlockID
	accepted.SupersedesIDs = []string{root.BlockID}
	accepted.Status = "accepted"
	accepted.TrustLevel = "workspace_verified"
	accepted.VerificationStatus = "verified"
	accepted.VerificationMethods = []string{"verification_report"}
	accepted.PromotionDecision = "accepted"
	accepted.EvidenceRefs = []memory.EvidenceRef{{RefType: "verification_report",
		RefID: "report-1", ProducedBy: "verifier", ProducerID: "verifier-1"}}
	accepted.Metadata = map[string]any{"promotion_source_id": root.BlockID,
		"promotion_source_revision_id": root.RevisionID}
	accepted.ValidFrom = "2024-02-01T00:00:00Z"
	accepted.RecordedAt = accepted.ValidFrom
	accepted.UpdatedAt = accepted.ValidFrom
	membership := *root.BlockMembership
	membership.RecordID = accepted.BlockID
	membership.RecordRevisionID = accepted.RevisionID
	membership.ValidFrom = accepted.ValidFrom
	membership.MembershipRevisionID = membership.RevisionHash()
	accepted.BlockMembership = &membership
	receipt := memory.PublicationCommitReceipt{
		ReceiptID: "publication:request-1", RequestHash: "request-hash-1",
		WorkspaceID: "alpha", ProfileID: "team", SourceID: root.BlockID,
		SourceRevisionID: root.RevisionID, RecordID: accepted.BlockID,
		RevisionID: accepted.RevisionID, TargetBlockID: "leaf",
		TargetScope: "workspace", TargetScopeID: "alpha", Authority: "advisory",
	}
	wrong := receipt
	wrong.TargetBlockID = "other"
	if _, err := writer.PreparePublicationSuccessorWrite(ctx, accepted, "promote", wrong); err == nil {
		t.Fatal("receipt with a different target leaf was staged")
	}
	staged, err := writer.PreparePublicationSuccessorWrite(ctx, accepted, "promote", receipt)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := writer.PreparePublicationSuccessorWrite(ctx, accepted, "promote", receipt); err != nil || !replay.Replayed || replay.CommitRevision != staged.CommitRevision {
		t.Fatalf("pending publication did not replay: %+v, %v", replay, err)
	}
	if _, err := writer.PrepareSuccessorWrite(ctx, accepted, "promote", ""); err == nil {
		t.Fatal("ordinary successor retry dropped the staged publication receipt")
	}
	if found, err := index.PublicationReceipt(ctx, receipt.ReceiptID); err != nil || found != nil {
		t.Fatalf("pending publication already has a durable receipt: %+v, %v", found, err)
	}
	if _, err := writer.db.Exec(`CREATE TRIGGER fail_publication_receipt BEFORE INSERT ON canonical_mutation_receipts
		BEGIN SELECT RAISE(ABORT, 'injected receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err == nil {
		t.Fatal("publication receipt failure was ignored")
	}
	var headID, headStatus, writeStatus string
	if err := writer.db.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, "alpha", sha256Hex(root.CanonicalKey)).Scan(&headID); err != nil || headID != root.BlockID {
		t.Fatalf("failed receipt advanced canonical head: %q, %v", headID, err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT status FROM canonical_head_outbox WHERE successor_id=?`, accepted.BlockID).Scan(&headStatus); err != nil || headStatus != "pending" {
		t.Fatalf("failed receipt committed head journal: %q, %v", headStatus, err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT status FROM write_outbox WHERE block_id=?`, accepted.BlockID).Scan(&writeStatus); err != nil || writeStatus != "pending" {
		t.Fatalf("failed receipt committed write journal: %q, %v", writeStatus, err)
	}
	if _, err := writer.db.Exec("DROP TRIGGER fail_publication_receipt"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staged.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	var dependencyCount int
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM edges WHERE src_id=?
		AND dst_id=? AND edge_type='derived_from'`, accepted.BlockID, witness.BlockID).Scan(&dependencyCount); err != nil || dependencyCount != 1 {
		t.Fatalf("publication dropped candidate dependencies: %d, %v", dependencyCount, err)
	}
	found, err := index.PublicationReceipt(ctx, receipt.ReceiptID)
	if err != nil || found == nil || *found != receipt {
		t.Fatalf("committed publication receipt is unavailable: %+v, %v", found, err)
	}
	if replay, err := writer.PreparePublicationSuccessorWrite(ctx, accepted, "promote", receipt); err != nil || !replay.Replayed || replay.ProjectionPending {
		t.Fatalf("committed publication did not replay: %+v, %v", replay, err)
	}
	changed := receipt
	changed.RequestHash = "different-request"
	if _, err := writer.PreparePublicationSuccessorWrite(ctx, accepted, "promote", changed); err == nil {
		t.Fatal("different request replayed an existing publication")
	}
	candidate := root
	candidate.BlockID = "mem_approved_correction_candidate"
	candidate.RevisionID = "rev_approved_candidate"
	candidate.CanonicalKey = "alpha:observation:approved-correction"
	candidate.Content = "Approved correction content"
	candidate.Metadata = map[string]any{"correction_target_id": accepted.BlockID,
		"correction_target_revision_id": accepted.RevisionID}
	candidateMembership := *root.BlockMembership
	candidateMembership.RecordID = candidate.BlockID
	candidateMembership.RecordRevisionID = candidate.RevisionID
	candidateMembership.MembershipRevisionID = candidateMembership.RevisionHash()
	candidate.BlockMembership = &candidateMembership
	candidateStage, err := writer.PrepareRootWrite(ctx, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, candidateStage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AddEdge(ctx, memory.EdgeRecord{SrcID: candidate.BlockID,
		DstID: witness.BlockID, EdgeType: "derived_from"}); err != nil {
		t.Fatal(err)
	}
	correction := accepted
	correction.BlockID = "mem_approved_correction"
	correction.RevisionID = "rev_approved_correction"
	correction.RevisionOf = accepted.BlockID
	correction.SupersedesIDs = []string{accepted.BlockID}
	correction.Content = candidate.Content
	correction.ValidFrom = "2024-03-01T00:00:00Z"
	correction.RecordedAt = correction.ValidFrom
	correction.UpdatedAt = correction.ValidFrom
	correction.Metadata = map[string]any{
		"correction_target_id":             accepted.BlockID,
		"approved_correction_candidate_id": candidate.BlockID,
		"promotion_source_id":              candidate.BlockID,
		"promotion_source_revision_id":     candidate.RevisionID,
	}
	correctionMembership := *accepted.BlockMembership
	correctionMembership.RecordID = correction.BlockID
	correctionMembership.RecordRevisionID = correction.RevisionID
	correctionMembership.ValidFrom = correction.ValidFrom
	correctionMembership.MembershipRevisionID = correctionMembership.RevisionHash()
	correction.BlockMembership = &correctionMembership
	correctionReceipt := receipt
	correctionReceipt.ReceiptID = "publication:approved-correction"
	correctionReceipt.RequestHash = "request-approved-correction"
	correctionReceipt.SourceID = candidate.BlockID
	correctionReceipt.SourceRevisionID = candidate.RevisionID
	correctionReceipt.RecordID = correction.BlockID
	correctionReceipt.RevisionID = correction.RevisionID
	wrongCorrection := correction
	wrongCorrection.Content = "content never approved by candidate"
	if _, err := writer.PreparePublicationSuccessorWrite(ctx, wrongCorrection, "correction", correctionReceipt); err == nil {
		t.Fatal("correction published content not approved by its candidate")
	}
	wrongCorrection = correction
	wrongCorrection.Metadata = map[string]any{
		"correction_target_id":             accepted.BlockID,
		"approved_correction_candidate_id": root.BlockID,
		"promotion_source_id":              candidate.BlockID,
		"promotion_source_revision_id":     candidate.RevisionID,
	}
	if _, err := writer.PreparePublicationSuccessorWrite(ctx, wrongCorrection, "correction", correctionReceipt); err == nil {
		t.Fatal("correction attached an unrelated approval candidate")
	}
	for _, bad := range []struct{ name, targetID, targetRevision string }{
		{"other-target", root.BlockID, root.RevisionID},
		{"stale-target-revision", accepted.BlockID, root.RevisionID},
	} {
		badCandidate := candidate
		badCandidate.BlockID = "mem_bad_candidate_" + bad.name
		badCandidate.RevisionID = "rev_bad_candidate_" + bad.name
		badCandidate.CanonicalKey = "alpha:observation:bad-candidate-" + bad.name
		badCandidate.Metadata = map[string]any{"correction_target_id": bad.targetID,
			"correction_target_revision_id": bad.targetRevision}
		badMembership := *candidate.BlockMembership
		badMembership.RecordID = badCandidate.BlockID
		badMembership.RecordRevisionID = badCandidate.RevisionID
		badMembership.MembershipRevisionID = badMembership.RevisionHash()
		badCandidate.BlockMembership = &badMembership
		badStage, err := writer.PrepareRootWrite(ctx, badCandidate)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.CompleteProjection(ctx, badStage.CommitRevision, skippedNativeProjector{}); err != nil {
			t.Fatal(err)
		}
		badCorrection := correction
		badCorrection.BlockID = "mem_bad_correction_" + bad.name
		badCorrection.RevisionID = "rev_bad_correction_" + bad.name
		badCorrection.Metadata = map[string]any{
			"correction_target_id":             accepted.BlockID,
			"approved_correction_candidate_id": badCandidate.BlockID,
			"promotion_source_id":              badCandidate.BlockID,
			"promotion_source_revision_id":     badCandidate.RevisionID,
		}
		badCorrectionMembership := *correction.BlockMembership
		badCorrectionMembership.RecordID = badCorrection.BlockID
		badCorrectionMembership.RecordRevisionID = badCorrection.RevisionID
		badCorrectionMembership.MembershipRevisionID = badCorrectionMembership.RevisionHash()
		badCorrection.BlockMembership = &badCorrectionMembership
		badReceipt := correctionReceipt
		badReceipt.SourceID = badCandidate.BlockID
		badReceipt.SourceRevisionID = badCandidate.RevisionID
		badReceipt.RecordID = badCorrection.BlockID
		badReceipt.RevisionID = badCorrection.RevisionID
		if _, err := writer.PreparePublicationSuccessorWrite(ctx, badCorrection, "correction", badReceipt); err == nil {
			t.Fatalf("correction accepted %s candidate", bad.name)
		}
	}
	correctionStage, err := writer.PreparePublicationSuccessorWrite(ctx, correction, "correction", correctionReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, correctionStage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM edges WHERE src_id=?
		AND dst_id=? AND edge_type='derived_from'`, correction.BlockID, witness.BlockID).Scan(&dependencyCount); err != nil || dependencyCount != 1 {
		t.Fatalf("approved correction dropped candidate dependencies: %d, %v", dependencyCount, err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE canonical_mutation_receipts
		SET payload_json=json_set(payload_json,'$.request_hash','rewritten') WHERE receipt_id=?`, receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}
	if _, err := index.PublicationReceipt(ctx, receipt.ReceiptID); err == nil {
		t.Fatal("detached publication receipt was accepted")
	}
}

func TestMaterializedPublicationBindsCurrentSourceAndProvenance(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	source, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	source.CanonicalKey = "alpha:observation:source"
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	stagedSource, err := writer.PrepareRootWrite(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, stagedSource.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	copyRecord := source
	copyRecord.RevisionID = "rev_materialized"
	copyMaterial := source.CanonicalKey + "|" + source.RevisionID + "|leaf|workspace|alpha"
	copyRecord.CanonicalKey = "alpha:team:publication:" + sha256Hex(copyMaterial)[:24]
	copyRecord.BlockID = memory.BlockIDForCanonicalKey(copyRecord.CanonicalKey)
	copyRecord.Status = "accepted"
	copyRecord.TrustLevel = "workspace_verified"
	copyRecord.VerificationStatus = "verified"
	copyRecord.VerificationMethods = []string{"verification_report"}
	copyRecord.PromotionDecision = "accepted"
	copyRecord.EvidenceRefs = []memory.EvidenceRef{{RefType: "verification_report",
		RefID: "report-copy", ProducedBy: "verifier", ProducerID: "verifier-1"}}
	copyRecord.Metadata = map[string]any{"promotion_source_id": source.BlockID,
		"promotion_source_revision_id": source.RevisionID}
	membership := *source.BlockMembership
	membership.RecordID = copyRecord.BlockID
	membership.RecordRevisionID = copyRecord.RevisionID
	membership.MembershipRevisionID = membership.RevisionHash()
	copyRecord.BlockMembership = &membership
	receipt := memory.PublicationCommitReceipt{
		ReceiptID: "publication:copy-1", RequestHash: "request-copy-1",
		WorkspaceID: "alpha", ProfileID: "team", SourceID: source.BlockID,
		SourceRevisionID: source.RevisionID, RecordID: copyRecord.BlockID,
		RevisionID: copyRecord.RevisionID, TargetBlockID: "leaf",
		TargetScope: "workspace", TargetScopeID: "alpha", Authority: "advisory",
		MaterializedCopy: true,
	}
	stage, err := writer.PreparePublicationRootWrite(ctx, copyRecord, receipt)
	if err != nil {
		t.Fatal(err)
	}
	var edgeCount int
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM edges WHERE src_id=?
		AND dst_id=? AND edge_type='derived_from'`, copyRecord.BlockID, source.BlockID).Scan(&edgeCount); err != nil || edgeCount != 0 {
		t.Fatalf("pending copy already has provenance: %d, %v", edgeCount, err)
	}
	if _, err := writer.CompleteProjection(ctx, stage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	if found, err := index.PublicationReceipt(ctx, receipt.ReceiptID); err != nil || found == nil || *found != receipt {
		t.Fatalf("materialized copy receipt is unavailable: %+v, %v", found, err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM edges WHERE src_id=?
		AND dst_id=? AND edge_type='derived_from'`, copyRecord.BlockID, source.BlockID).Scan(&edgeCount); err != nil || edgeCount != 1 {
		t.Fatalf("committed copy lacks provenance: %d, %v", edgeCount, err)
	}
	if replay, err := writer.PreparePublicationRootWrite(ctx, copyRecord, receipt); err != nil || !replay.Replayed || replay.ProjectionPending {
		t.Fatalf("materialized copy did not replay: %+v, %v", replay, err)
	}
	secondReceipt := receipt
	secondReceipt.ReceiptID = "publication:copy-second-operation"
	secondReceipt.RequestHash = "request-copy-second-operation"
	if err := writer.AttachPublicationCopyReceipt(ctx, secondReceipt); err != nil {
		t.Fatalf("second operation did not attach a copy receipt: %v", err)
	}
	if found, err := index.PublicationReceipt(ctx, secondReceipt.ReceiptID); err != nil ||
		found == nil || *found != secondReceipt {
		t.Fatalf("attached copy receipt did not recover: %+v, %v", found, err)
	}
	wrongReceipt := secondReceipt
	wrongReceipt.ReceiptID = "publication:copy-wrong-source"
	wrongReceipt.SourceRevisionID = "rev_wrong_source"
	if err := writer.AttachPublicationCopyReceipt(ctx, wrongReceipt); err == nil {
		t.Fatal("copy receipt accepted a different source revision")
	}
	if err := writer.AttachPublicationCopyReceipt(ctx, secondReceipt); err == nil {
		t.Fatal("copy receipt identity was attached twice")
	}
	legacyCopy := copyRecord
	legacyCopy.RevisionID = "rev_python_existing_copy"
	legacyMaterial := source.CanonicalKey + "|" + source.RevisionID + "|other|workspace|alpha"
	legacyCopy.CanonicalKey = "alpha:team:publication:" + sha256Hex(legacyMaterial)[:24]
	legacyCopy.BlockID = memory.BlockIDForCanonicalKey(legacyCopy.CanonicalKey)
	legacyMembership := *copyRecord.BlockMembership
	legacyMembership.PrimaryBlockID = "other"
	legacyMembership.SecondaryBlockIDs = []string{}
	legacyMembership.RecordID = legacyCopy.BlockID
	legacyMembership.RecordRevisionID = legacyCopy.RevisionID
	legacyMembership.MembershipRevisionID = legacyMembership.RevisionHash()
	legacyCopy.BlockMembership = &legacyMembership
	legacyStage, err := writer.PrepareRootWrite(ctx, legacyCopy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, legacyStage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	legacyReceipt := receipt
	legacyReceipt.ReceiptID = "publication:python-existing-copy"
	legacyReceipt.RequestHash = "request-python-existing-copy"
	legacyReceipt.RecordID = legacyCopy.BlockID
	legacyReceipt.RevisionID = legacyCopy.RevisionID
	legacyReceipt.TargetBlockID = "other"
	if _, err := writer.AddEdge(ctx, memory.EdgeRecord{SrcID: legacyCopy.BlockID,
		DstID: source.BlockID, EdgeType: "derived_from", WorkspaceID: "alpha",
		Reason: "tribunal_verified_lesson_promotion", Metadata: map[string]any{
			"tribunal_decision": "accept", "source_revision_id": source.RevisionID,
			"target_block_id": "other",
		}}); err != nil {
		t.Fatal(err)
	}
	legacyJSON, err := json.Marshal(legacyReceipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.ExecContext(ctx, `INSERT INTO canonical_mutation_receipts(
		commit_revision,receipt_id,payload_json) VALUES(?,?,?)`, legacyStage.CommitRevision,
		legacyReceipt.ReceiptID, string(legacyJSON)); err != nil {
		t.Fatal(err)
	}
	if found, err := index.PublicationReceipt(ctx, legacyReceipt.ReceiptID); err != nil || found == nil || *found != legacyReceipt {
		t.Fatalf("Python detached copy receipt did not recover: %+v, %v", found, err)
	}
	if _, err := writer.db.ExecContext(ctx, `DELETE FROM edges WHERE src_id=? AND dst_id=?
		AND edge_type='derived_from'`, legacyCopy.BlockID, source.BlockID); err != nil {
		t.Fatal(err)
	}
	if found, err := index.PublicationReceipt(ctx, legacyReceipt.ReceiptID); err != ErrPublicationProvenancePending || found == nil {
		t.Fatalf("detached Python receipt did not report repairable provenance: %+v, %v", found, err)
	}
	wrongLegacy := legacyReceipt
	wrongLegacy.RequestHash = "another-request"
	if err := writer.RecoverDetachedPublicationCopy(ctx, wrongLegacy); err == nil {
		t.Fatal("detached copy repaired for a different request")
	}
	if err := writer.RecoverDetachedPublicationCopy(ctx, legacyReceipt); err != nil {
		t.Fatalf("detached Python copy provenance was not repaired: %v", err)
	}
	if found, err := index.PublicationReceipt(ctx, legacyReceipt.ReceiptID); err != nil || found == nil || *found != legacyReceipt {
		t.Fatalf("repaired Python copy receipt cannot replay: %+v, %v", found, err)
	}

	staleCopy := copyRecord
	staleCopy.RevisionID = "rev_stale_copy"
	staleMaterial := source.CanonicalKey + "|" + source.RevisionID + "|third|workspace|alpha"
	staleCopy.CanonicalKey = "alpha:team:publication:" + sha256Hex(staleMaterial)[:24]
	staleCopy.BlockID = memory.BlockIDForCanonicalKey(staleCopy.CanonicalKey)
	staleMembership := *copyRecord.BlockMembership
	staleMembership.PrimaryBlockID = "third"
	staleMembership.SecondaryBlockIDs = []string{}
	staleMembership.RecordID = staleCopy.BlockID
	staleMembership.RecordRevisionID = staleCopy.RevisionID
	staleMembership.MembershipRevisionID = staleMembership.RevisionHash()
	staleCopy.BlockMembership = &staleMembership
	staleReceipt := receipt
	staleReceipt.ReceiptID = "publication:copy-2"
	staleReceipt.RequestHash = "request-copy-2"
	staleReceipt.RecordID = staleCopy.BlockID
	staleReceipt.RevisionID = staleCopy.RevisionID
	staleReceipt.TargetBlockID = "third"
	staleStage, err := writer.PreparePublicationRootWrite(ctx, staleCopy, staleReceipt)
	if err != nil {
		t.Fatal(err)
	}
	revisedSource := source
	revisedSource.BlockID = "mem_source_successor"
	revisedSource.RevisionID = "rev_source_successor"
	revisedSource.RevisionOf = source.BlockID
	revisedSource.SupersedesIDs = []string{source.BlockID}
	revisedSource.ValidFrom = "2024-02-01T00:00:00Z"
	revisedSource.RecordedAt = revisedSource.ValidFrom
	revisedSource.UpdatedAt = revisedSource.ValidFrom
	revisedMembership := *source.BlockMembership
	revisedMembership.RecordID = revisedSource.BlockID
	revisedMembership.RecordRevisionID = revisedSource.RevisionID
	revisedMembership.ValidFrom = revisedSource.ValidFrom
	revisedMembership.MembershipRevisionID = revisedMembership.RevisionHash()
	revisedSource.BlockMembership = &revisedMembership
	revisedStage, err := writer.PrepareSuccessorWrite(ctx, revisedSource, "supersede", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, revisedStage.CommitRevision, skippedNativeProjector{}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, staleStage.CommitRevision, skippedNativeProjector{}); err == nil {
		t.Fatal("copy committed after its verified source moved")
	}
	if found, err := index.PublicationReceipt(ctx, staleReceipt.ReceiptID); err != nil || found != nil {
		t.Fatalf("stale copy has a committed receipt: %+v, %v", found, err)
	}
	if err := writer.db.QueryRowContext(ctx, `SELECT count(*) FROM edges WHERE src_id=?
		AND dst_id=? AND edge_type='derived_from'`, staleCopy.BlockID, source.BlockID).Scan(&edgeCount); err != nil || edgeCount != 0 {
		t.Fatalf("stale copy has committed provenance: %d, %v", edgeCount, err)
	}
}
