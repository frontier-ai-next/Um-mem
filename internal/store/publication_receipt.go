package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

var ErrPublicationProvenancePending = errors.New("publication copy provenance needs recovery")

func decodePublicationReceipt(raw json.RawMessage) (memory.PublicationCommitReceipt, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 13 {
		return memory.PublicationCommitReceipt{}, errors.New("publication receipt payload has invalid fields")
	}
	for _, field := range []string{"receipt_id", "request_hash", "workspace_id",
		"profile_id", "source_id", "source_revision_id", "record_id", "revision_id",
		"target_block_id", "target_scope", "target_scope_id", "authority",
		"materialized_copy"} {
		if _, ok := fields[field]; !ok {
			return memory.PublicationCommitReceipt{}, fmt.Errorf("publication receipt missing %s", field)
		}
	}
	var receipt memory.PublicationCommitReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return memory.PublicationCommitReceipt{}, err
	}
	return receipt, receipt.Validate()
}

func samePublicationReceipt(raw json.RawMessage, expected *memory.PublicationCommitReceipt) error {
	if expected == nil {
		if string(raw) != "null" {
			return errors.New("canonical successor retry publication receipt differs")
		}
		return nil
	}
	actual, err := decodePublicationReceipt(raw)
	if err != nil || actual != *expected {
		return errors.New("canonical successor retry publication receipt differs")
	}
	return nil
}

func validatePublicationSourceTx(ctx context.Context, tx *sql.Tx, receipt memory.PublicationCommitReceipt) error {
	var payload, committedAt string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,committed_at FROM blocks
		WHERE block_id=? AND workspace_id=?`, receipt.SourceID, receipt.WorkspaceID).Scan(&payload, &committedAt); err != nil {
		return err
	}
	source, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil || committedAt == "" || source.BlockID != receipt.SourceID ||
		source.RevisionID != receipt.SourceRevisionID || source.ProfileID != receipt.ProfileID ||
		source.Status != "candidate" {
		return errors.New("publication source is not a committed exact revision")
	}
	if source.CanonicalKey != "" {
		var headID string
		if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
			WHERE workspace_id=? AND canonical_key_sha256=?`, receipt.WorkspaceID,
			sha256Hex(source.CanonicalKey)).Scan(&headID); err != nil {
			return err
		}
		if headID != receipt.SourceID {
			return errors.New("publication source is no longer the canonical head")
		}
	}
	return nil
}

// correctionPromotionSourceTx distinguishes an approved correction candidate
// from an ordinary amendment. Python only inherits candidate dependencies when
// correction_target_id names the predecessor being replaced.
func correctionPromotionSourceTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord,
	predecessor string, publication *memory.PublicationCommitReceipt) (string, error) {
	target, _ := record.Metadata["correction_target_id"].(string)
	sourceID, _ := record.Metadata["approved_correction_candidate_id"].(string)
	if target != predecessor || sourceID == "" {
		if publication != nil {
			return "", errors.New("publication correction has no approved candidate source")
		}
		return "", nil
	}
	if publication != nil && publication.SourceID != sourceID {
		return "", errors.New("publication receipt differs from approved correction candidate")
	}
	var payload, committedAt string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,committed_at FROM blocks
		WHERE block_id=? AND workspace_id=?`, sourceID, record.WorkspaceID).Scan(&payload, &committedAt); err != nil {
		return "", err
	}
	source, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil || committedAt == "" || source.WorkspaceID != record.WorkspaceID ||
		source.ProfileID != record.ProfileID || source.Status != "candidate" ||
		source.RevisionID != record.Metadata["promotion_source_revision_id"] ||
		sourceID != record.Metadata["promotion_source_id"] ||
		source.Content != record.Content || source.CanonicalKey == "" {
		return "", errors.New("approved correction candidate identity or content mismatch")
	}
	var predecessorPayload string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM blocks
		WHERE block_id=? AND workspace_id=? AND committed_at<>''`,
		predecessor, record.WorkspaceID).Scan(&predecessorPayload); err != nil {
		return "", err
	}
	targetRecord, err := memory.DecodeCanonicalRecord([]byte(predecessorPayload))
	if err != nil || source.Metadata["correction_target_id"] != predecessor ||
		source.Metadata["correction_target_revision_id"] != targetRecord.RevisionID ||
		source.BlockMembership == nil || targetRecord.BlockMembership == nil ||
		source.BlockMembership.PrimaryBlockID != targetRecord.BlockMembership.PrimaryBlockID {
		return "", errors.New("approved correction candidate targets a different revision or leaf")
	}
	var headID string
	if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, record.WorkspaceID,
		sha256Hex(source.CanonicalKey)).Scan(&headID); err != nil {
		return "", err
	}
	if headID != sourceID {
		return "", errors.New("approved correction candidate is no longer current")
	}
	return sourceID, nil
}

func ensurePublicationReceiptUnused(ctx context.Context, tx *sql.Tx, receiptID string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT payload_json FROM canonical_mutation_receipts
		WHERE receipt_id=? LIMIT 1`, receiptID).Scan(&existing)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("publication receipt identity already committed")
}

func insertPublicationReceipt(ctx context.Context, tx *sql.Tx, revision int64, raw json.RawMessage) error {
	receipt, err := decodePublicationReceipt(raw)
	if err != nil {
		return err
	}
	if err := ensurePublicationReceiptUnused(ctx, tx, receipt.ReceiptID); err != nil {
		return err
	}
	payload, err := jsoncompat.MarshalPython(receipt, false)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO canonical_mutation_receipts(commit_revision,receipt_id,payload_json)
		VALUES(?,?,?)`, revision, receipt.ReceiptID, string(payload))
	return err
}

func insertPublicationCopyProvenance(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord,
	receipt memory.PublicationCommitReceipt, now string) error {
	createdAt, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		return err
	}
	edge := memory.EdgeRecord{
		SrcID: record.BlockID, DstID: receipt.SourceID, EdgeType: "derived_from",
		WorkspaceID: record.WorkspaceID, RunID: record.RunID, SubtaskID: record.SubtaskID,
		Reason: "tribunal_verified_lesson_promotion", Weight: json.Number("1.0"),
		EvidenceRefs: record.EvidenceRefs,
		Metadata: map[string]any{
			"tribunal_decision": "accept", "source_revision_id": receipt.SourceRevisionID,
			"target_block_id": receipt.TargetBlockID,
		}, CreatedAt: createdAt.UTC().Format(time.RFC3339),
	}
	payload, err := jsoncompat.MarshalPython(edge, false)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO edges(src_id,dst_id,edge_type,workspace_id,payload_json)
		VALUES(?,?,?,?,?)`, edge.SrcID, edge.DstID, edge.EdgeType,
		edge.WorkspaceID, string(payload))
	return err
}

// Python could attach a receipt to an already committed materialized copy.
// That historical mutation has publication_receipt:null, so verify the exact
// source and separately committed provenance before acknowledging it.
func validatePublicationCopyIdentityTx(ctx context.Context, tx *sql.Tx,
	receipt memory.PublicationCommitReceipt, record memory.MemoryRecord) error {
	var sourceJSON, committedAt string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,committed_at FROM blocks
		WHERE block_id=? AND workspace_id=?`, receipt.SourceID, receipt.WorkspaceID).Scan(&sourceJSON, &committedAt); err != nil {
		return err
	}
	source, err := memory.DecodeCanonicalRecord([]byte(sourceJSON))
	if err != nil || committedAt == "" || source.BlockID != receipt.SourceID ||
		source.RevisionID != receipt.SourceRevisionID || source.ProfileID != receipt.ProfileID ||
		source.Status != "candidate" {
		return errors.New("publication copy source revision is invalid")
	}
	material := source.CanonicalKey + "|" + source.RevisionID + "|" +
		receipt.TargetBlockID + "|" + receipt.TargetScope + "|" + receipt.TargetScopeID
	canonicalKey := receipt.WorkspaceID + ":" + receipt.ProfileID + ":publication:" + sha256Hex(material)[:24]
	if source.CanonicalKey == "" || record.CanonicalKey != canonicalKey ||
		record.BlockID != memory.BlockIDForCanonicalKey(canonicalKey) ||
		record.Content != source.Content {
		return errors.New("publication copy identity or content is invalid")
	}
	return nil
}

func validateDetachedCopyReceiptTx(ctx context.Context, tx *sql.Tx,
	receipt memory.PublicationCommitReceipt, record memory.MemoryRecord) error {
	if err := validatePublicationCopyIdentityTx(ctx, tx, receipt, record); err != nil {
		return err
	}
	var edgeJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM edges WHERE workspace_id=?
		AND src_id=? AND dst_id=? AND edge_type='derived_from'`,
		receipt.WorkspaceID, receipt.RecordID, receipt.SourceID).Scan(&edgeJSON); err == sql.ErrNoRows {
		return ErrPublicationProvenancePending
	} else if err != nil {
		return err
	}
	var edge memory.EdgeRecord
	if err := json.Unmarshal([]byte(edgeJSON), &edge); err != nil ||
		edge.SrcID != receipt.RecordID || edge.DstID != receipt.SourceID ||
		edge.EdgeType != "derived_from" || edge.WorkspaceID != receipt.WorkspaceID ||
		edge.Reason != "tribunal_verified_lesson_promotion" ||
		edge.Metadata["tribunal_decision"] != "accept" ||
		edge.Metadata["source_revision_id"] != receipt.SourceRevisionID ||
		edge.Metadata["target_block_id"] != receipt.TargetBlockID {
		return errors.New("detached publication provenance is invalid")
	}
	return nil
}

// AttachPublicationCopyReceipt acknowledges an already committed verified
// copy for a second operation. The new receipt points at the original immutable
// mutation; no new record or provenance edge is created.
func (writer *Writer) AttachPublicationCopyReceipt(ctx context.Context,
	receipt memory.PublicationCommitReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if !receipt.MaterializedCopy || receipt.WorkspaceID != writer.workspaceID ||
		receipt.ProfileID != writer.profileID {
		return errors.New("detached publication requires selected copy identity")
	}
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensurePublicationReceiptUnused(ctx, tx, receipt.ReceiptID); err != nil {
		return err
	}
	var revision int64
	var workspace, successor, action, headStatus, mutationID, mutationChecksum string
	var mutationJSON, storedChecksum, kind, recordJSON, committedAt, writeStatus string
	var recordPayloadID, recordChecksum, writePayloadID, writeChecksum string
	err = tx.QueryRowContext(ctx, `SELECT m.commit_revision,m.workspace_id,m.successor_id,
		m.mutation_action,m.record_payload_id,m.record_payload_sha256,
		h.status,h.payload_id,h.payload_sha256,p.payload_json,
		p.payload_sha256,p.payload_kind,b.payload_json,b.committed_at,o.status,
		o.payload_id,o.payload_sha256
		FROM canonical_mutations m
		JOIN canonical_head_outbox h ON h.commit_revision=m.commit_revision
		JOIN canonical_payloads p ON p.payload_id=h.payload_id
		JOIN blocks b ON b.block_id=m.successor_id
		JOIN write_outbox o ON o.block_id=m.successor_id
		WHERE m.successor_id=? AND m.workspace_id=?`, receipt.RecordID,
		receipt.WorkspaceID).Scan(&revision, &workspace, &successor, &action,
		&recordPayloadID, &recordChecksum, &headStatus, &mutationID,
		&mutationChecksum, &mutationJSON, &storedChecksum, &kind,
		&recordJSON, &committedAt, &writeStatus, &writePayloadID, &writeChecksum)
	if err != nil {
		return err
	}
	if workspace != writer.workspaceID || successor != receipt.RecordID ||
		action != "insert" || headStatus != "committed" || writeStatus != "committed" ||
		committedAt == "" || kind != "mutation" ||
		mutationID != "mutation:"+mutationChecksum || storedChecksum != mutationChecksum ||
		mutationChecksum != sha256Hex("mutation\x00"+mutationJSON) ||
		recordPayloadID != writePayloadID || recordChecksum != writeChecksum ||
		recordPayloadID != "record:"+recordChecksum ||
		recordChecksum != sha256Hex("record\x00"+recordJSON) {
		return errors.New("existing publication copy journal is invalid")
	}
	var mutation stagedSuccessorMutation
	if err := json.Unmarshal([]byte(mutationJSON), &mutation); err != nil ||
		mutation.Action != "insert" || mutation.SupersededBlockID != "" ||
		string(mutation.Edge) != "null" ||
		mutation.RecordPayloadID != recordPayloadID || mutation.RecordChecksum != recordChecksum {
		return errors.New("existing publication copy mutation is invalid")
	}
	record, err := memory.DecodeCanonicalRecord([]byte(recordJSON))
	if err != nil || receipt.ValidateForRecord(record) != nil {
		return errors.New("existing publication copy record is invalid")
	}
	if record.CanonicalKey == "" {
		return errors.New("existing publication copy has no canonical key")
	}
	var currentHead string
	if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, writer.workspaceID,
		sha256Hex(record.CanonicalKey)).Scan(&currentHead); err != nil {
		return err
	}
	if currentHead != record.BlockID {
		return errors.New("existing publication copy is no longer current")
	}
	if string(mutation.PublicationReceipt) != "null" {
		original, err := decodePublicationReceipt(mutation.PublicationReceipt)
		if err != nil || original.ValidateForRecord(record) != nil ||
			original.SourceID != receipt.SourceID ||
			original.SourceRevisionID != receipt.SourceRevisionID {
			return errors.New("existing publication copy original receipt is invalid")
		}
	}
	if err := validatePublicationSourceTx(ctx, tx, receipt); err != nil {
		return err
	}
	if err := validateDetachedCopyReceiptTx(ctx, tx, receipt, record); err != nil {
		return err
	}
	encoded, err := jsoncompat.MarshalPython(receipt, false)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_mutation_receipts(
		commit_revision,receipt_id,payload_json) VALUES(?,?,?)`, revision,
		receipt.ReceiptID, string(encoded)); err != nil {
		return err
	}
	return tx.Commit()
}

// PublicationReceipt resolves a committed publication for an idempotent caller
// retry. It checks the receipt against the immutable record and mutation, so a
// detached or rewritten receipt cannot be used as a success response.
func (index *Index) PublicationReceipt(ctx context.Context, receiptID string) (*memory.PublicationCommitReceipt, error) {
	if receiptID == "" {
		return nil, errors.New("publication receipt ID is required")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var receiptCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM canonical_mutation_receipts
		WHERE receipt_id=?`, receiptID).Scan(&receiptCount); err != nil {
		return nil, err
	}
	if receiptCount == 0 {
		return nil, nil
	}
	if receiptCount != 1 {
		return nil, errors.New("publication receipt identity is duplicated")
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.payload_json,m.workspace_id,m.successor_id,
		m.record_payload_id,m.record_payload_sha256,h.status,h.payload_id,h.payload_sha256,
		p.payload_json,p.payload_sha256,p.payload_kind,b.payload_json,b.committed_at,
		o.status,o.payload_id,o.payload_sha256
		FROM canonical_mutation_receipts r
		JOIN canonical_mutations m ON m.commit_revision=r.commit_revision
		JOIN canonical_head_outbox h ON h.commit_revision=m.commit_revision
		JOIN canonical_payloads p ON p.payload_id=h.payload_id
		JOIN blocks b ON b.block_id=m.successor_id
		JOIN write_outbox o ON o.block_id=m.successor_id
		WHERE r.receipt_id=?`, receiptID)
	if err != nil {
		return nil, err
	}
	var result *memory.PublicationCommitReceipt
	var resultRecord memory.MemoryRecord
	legacyDetachedCopy := false
	for rows.Next() {
		var receiptJSON, workspace, successor, recordPayloadID, recordChecksum string
		var headStatus, mutationID, mutationChecksum, mutationJSON, storedMutationChecksum, mutationKind string
		var recordJSON, committedAt, writeStatus, writePayloadID, writeChecksum string
		if err := rows.Scan(&receiptJSON, &workspace, &successor, &recordPayloadID,
			&recordChecksum, &headStatus, &mutationID, &mutationChecksum,
			&mutationJSON, &storedMutationChecksum, &mutationKind, &recordJSON,
			&committedAt, &writeStatus, &writePayloadID, &writeChecksum); err != nil {
			rows.Close()
			return nil, err
		}
		if result != nil || workspace != index.workspaceID || headStatus != "committed" ||
			writeStatus != "committed" || committedAt == "" ||
			mutationKind != "mutation" || mutationChecksum != storedMutationChecksum ||
			mutationID != "mutation:"+mutationChecksum ||
			mutationChecksum != sha256Hex("mutation\x00"+mutationJSON) ||
			recordPayloadID != writePayloadID || recordChecksum != writeChecksum ||
			recordPayloadID != "record:"+recordChecksum ||
			recordChecksum != sha256Hex("record\x00"+recordJSON) {
			rows.Close()
			return nil, errors.New("committed publication receipt journal is invalid")
		}
		receipt, err := decodePublicationReceipt([]byte(receiptJSON))
		if err != nil || receipt.ReceiptID != receiptID ||
			receipt.WorkspaceID != index.workspaceID || receipt.ProfileID != index.profileID ||
			receipt.RecordID != successor {
			rows.Close()
			return nil, errors.New("committed publication receipt identity is invalid")
		}
		var mutation stagedSuccessorMutation
		if err := json.Unmarshal([]byte(mutationJSON), &mutation); err != nil ||
			mutation.RecordPayloadID != recordPayloadID ||
			mutation.RecordChecksum != recordChecksum {
			rows.Close()
			return nil, errors.New("committed publication receipt differs from mutation")
		}
		if string(mutation.PublicationReceipt) == "null" {
			if !receipt.MaterializedCopy || mutation.Action != "insert" ||
				mutation.SupersededBlockID != "" || string(mutation.Edge) != "null" {
				rows.Close()
				return nil, errors.New("detached publication receipt is not a root copy")
			}
			legacyDetachedCopy = true
		} else if samePublicationReceipt(mutation.PublicationReceipt, &receipt) != nil {
			// A later request may acknowledge the same verified copy with a
			// detached receipt. The original embedded receipt must still bind
			// the immutable root; provenance is checked below.
			original, decodeErr := decodePublicationReceipt(mutation.PublicationReceipt)
			if decodeErr != nil || !receipt.MaterializedCopy || !original.MaterializedCopy ||
				mutation.Action != "insert" || mutation.SupersededBlockID != "" ||
				string(mutation.Edge) != "null" {
				rows.Close()
				return nil, errors.New("committed publication receipt differs from mutation")
			}
			legacyDetachedCopy = true
		}
		record, err := memory.DecodeCanonicalRecord([]byte(recordJSON))
		if err != nil || receipt.ValidateForRecord(record) != nil {
			rows.Close()
			return nil, errors.New("committed publication receipt differs from record")
		}
		if legacyDetachedCopy && string(mutation.PublicationReceipt) != "null" {
			original, _ := decodePublicationReceipt(mutation.PublicationReceipt)
			if original.ValidateForRecord(record) != nil || original.SourceID != receipt.SourceID ||
				original.SourceRevisionID != receipt.SourceRevisionID {
				rows.Close()
				return nil, errors.New("detached publication receipt differs from original source")
			}
		}
		result = &receipt
		resultRecord = record
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("publication receipt has no committed mutation")
	}
	if legacyDetachedCopy {
		if err := validateDetachedCopyReceiptTx(ctx, tx, *result, resultRecord); err != nil {
			if errors.Is(err, ErrPublicationProvenancePending) {
				return result, err
			}
			return nil, err
		}
	}
	return result, tx.Commit()
}

// RecoverDetachedPublicationCopy closes the narrow Python crash window where
// an already committed copy received its receipt but the follow-up provenance
// edge was not written. The caller must supply the exact request-bound receipt.
func (writer *Writer) RecoverDetachedPublicationCopy(ctx context.Context,
	expected memory.PublicationCommitReceipt) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if !expected.MaterializedCopy || expected.WorkspaceID != writer.workspaceID ||
		expected.ProfileID != writer.profileID {
		return errors.New("detached publication recovery requires selected copy identity")
	}
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var receiptCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM canonical_mutation_receipts
		WHERE receipt_id=?`, expected.ReceiptID).Scan(&receiptCount); err != nil || receiptCount != 1 {
		return errors.New("detached publication receipt identity is unavailable or duplicated")
	}
	var receiptJSON, workspace, successor, action, headStatus, mutationID, mutationChecksum string
	var mutationJSON, storedChecksum, kind, recordJSON, committedAt, writeStatus string
	var recordPayloadID, recordChecksum, writePayloadID, writeChecksum string
	err = tx.QueryRowContext(ctx, `SELECT r.payload_json,m.workspace_id,m.successor_id,
		m.mutation_action,m.record_payload_id,m.record_payload_sha256,
		h.status,h.payload_id,h.payload_sha256,p.payload_json,
		p.payload_sha256,p.payload_kind,b.payload_json,b.committed_at,o.status,
		o.payload_id,o.payload_sha256
		FROM canonical_mutation_receipts r
		JOIN canonical_mutations m ON m.commit_revision=r.commit_revision
		JOIN canonical_head_outbox h ON h.commit_revision=m.commit_revision
		JOIN canonical_payloads p ON p.payload_id=h.payload_id
		JOIN blocks b ON b.block_id=m.successor_id
		JOIN write_outbox o ON o.block_id=m.successor_id
		WHERE r.receipt_id=?`, expected.ReceiptID).Scan(&receiptJSON, &workspace,
		&successor, &action, &recordPayloadID, &recordChecksum, &headStatus,
		&mutationID, &mutationChecksum, &mutationJSON, &storedChecksum, &kind,
		&recordJSON, &committedAt, &writeStatus, &writePayloadID, &writeChecksum)
	if err != nil {
		return err
	}
	receipt, err := decodePublicationReceipt([]byte(receiptJSON))
	if err != nil || receipt != expected || workspace != writer.workspaceID ||
		successor != expected.RecordID || action != "insert" ||
		headStatus != "committed" || writeStatus != "committed" ||
		committedAt == "" || kind != "mutation" ||
		mutationID != "mutation:"+mutationChecksum ||
		storedChecksum != mutationChecksum ||
		mutationChecksum != sha256Hex("mutation\x00"+mutationJSON) ||
		recordPayloadID != writePayloadID || recordChecksum != writeChecksum ||
		recordPayloadID != "record:"+recordChecksum ||
		recordChecksum != sha256Hex("record\x00"+recordJSON) {
		return errors.New("detached publication recovery journal is invalid")
	}
	var mutation stagedSuccessorMutation
	if err := json.Unmarshal([]byte(mutationJSON), &mutation); err != nil ||
		mutation.Action != "insert" || mutation.SupersededBlockID != "" ||
		string(mutation.Edge) != "null" || string(mutation.PublicationReceipt) != "null" ||
		mutation.RecordPayloadID != recordPayloadID || mutation.RecordChecksum != recordChecksum {
		return errors.New("detached publication recovery mutation is invalid")
	}
	record, err := memory.DecodeCanonicalRecord([]byte(recordJSON))
	if err != nil || receipt.ValidateForRecord(record) != nil {
		return errors.New("detached publication recovery record is invalid")
	}
	validationErr := validateDetachedCopyReceiptTx(ctx, tx, receipt, record)
	if validationErr == nil {
		return tx.Commit()
	}
	if !errors.Is(validationErr, ErrPublicationProvenancePending) {
		return validationErr
	}
	cycle, err := writer.wouldCycleTx(ctx, tx, record.BlockID, receipt.SourceID)
	if err != nil {
		return err
	}
	if cycle {
		return errors.New("detached publication provenance would create a cycle")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := insertPublicationCopyProvenance(ctx, tx, record, receipt, now); err != nil {
		return err
	}
	return tx.Commit()
}
