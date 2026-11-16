package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func commitRootCanonicalHead(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord, recordPayloadID, recordChecksum, now string) error {
	if record.CanonicalKey == "" {
		return nil
	}
	keyHash := sha256Hex(record.CanonicalKey)
	var headRevision int64
	var workspace, storedKeyHash, predecessor, successor, status, authority, action, rollbackTarget string
	var mutationID, mutationChecksum, headStatus string
	err := tx.QueryRowContext(ctx, `SELECT commit_revision,workspace_id,canonical_key_sha256,
		predecessor_id,successor_id,successor_status,successor_authority,mutation_action,
		rollback_target_id,payload_id,payload_sha256,status
		FROM canonical_head_outbox WHERE successor_id=? ORDER BY commit_revision DESC LIMIT 1`, record.BlockID).Scan(
		&headRevision, &workspace, &storedKeyHash, &predecessor, &successor, &status,
		&authority, &action, &rollbackTarget, &mutationID, &mutationChecksum, &headStatus,
	)
	if err != nil {
		return err
	}
	if workspace != record.WorkspaceID || storedKeyHash != keyHash || predecessor != "" ||
		successor != record.BlockID || status != record.Status || authority != record.Authority ||
		action != "insert" || rollbackTarget != "" || headStatus != "pending" {
		return errors.New("canonical root reservation does not match staged write")
	}
	var storedJSON, storedChecksum, kind string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json,payload_sha256,payload_kind
		FROM canonical_payloads WHERE payload_id=?`, mutationID).Scan(&storedJSON, &storedChecksum, &kind); err != nil {
		return err
	}
	if storedChecksum != mutationChecksum || kind != "mutation" ||
		mutationID != "mutation:"+mutationChecksum ||
		mutationChecksum != sha256Hex("mutation\x00"+storedJSON) {
		return errors.New("canonical root mutation payload is invalid")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(storedJSON), &rawFields); err != nil || len(rawFields) != 10 {
		return errors.New("canonical root mutation payload fields are invalid")
	}
	var mutation stagedSuccessorMutation
	if err := json.Unmarshal([]byte(storedJSON), &mutation); err != nil {
		return err
	}
	if mutation.RecordPayloadID != recordPayloadID || mutation.RecordChecksum != recordChecksum ||
		mutation.SupersededBlockID != "" || mutation.Action != "insert" ||
		string(mutation.Edge) != "null" || mutation.RollbackTargetID != "" ||
		len(mutation.AffectedViewIDs) != 0 ||
		string(mutation.AmendmentReceipt) != "null" {
		return errors.New("canonical root mutation does not match staged record")
	}
	var consolidation *memory.ConsolidationCommitReceipt
	if string(mutation.ConsolidationReceipt) != "null" {
		decoded, err := decodeConsolidationReceipt(mutation.ConsolidationReceipt)
		if err != nil {
			return err
		}
		if err := decoded.ValidateForRecord(record); err != nil {
			return err
		}
		if err := validateConsolidationSourcesTx(ctx, tx, record); err != nil {
			return err
		}
		consolidation = &decoded
	}
	var publication *memory.PublicationCommitReceipt
	if string(mutation.PublicationReceipt) != "null" {
		if consolidation != nil {
			return errors.New("root mutation cannot combine consolidation and publication receipts")
		}
		decoded, err := decodePublicationReceipt(mutation.PublicationReceipt)
		if err != nil {
			return err
		}
		if !decoded.MaterializedCopy {
			return errors.New("canonical root publication must be a materialized copy")
		}
		if err := decoded.ValidateForRecord(record); err != nil {
			return err
		}
		if err := validatePublicationSourceTx(ctx, tx, decoded); err != nil {
			return err
		}
		if err := validatePublicationCopyIdentityTx(ctx, tx, decoded, record); err != nil {
			return err
		}
		publication = &decoded
	}
	expectedJSON, err := canonicalMutationJSONWithReceipts(recordPayloadID,
		recordChecksum, "insert", "", "", nil, consolidation, publication)
	if err != nil || expectedJSON != storedJSON {
		return errors.New("canonical root mutation payload differs from reserved identity")
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, record.WorkspaceID, keyHash).Scan(&existing)
	if err != sql.ErrNoRows {
		if err != nil {
			return err
		}
		return fmt.Errorf("canonical head conflict with %s", existing)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_mutations(
		commit_revision,workspace_id,canonical_key_sha256,predecessor_id,
		successor_id,mutation_action,rollback_target_id,record_payload_id,
		record_payload_sha256,recorded_at,committed_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		headRevision, record.WorkspaceID, keyHash, "", record.BlockID, "insert", "",
		recordPayloadID, recordChecksum, record.RecordedAt, now,
	); err != nil {
		return err
	}
	if publication != nil {
		if err := insertPublicationReceipt(ctx, tx, headRevision, mutation.PublicationReceipt); err != nil {
			return err
		}
		if err := insertPublicationCopyProvenance(ctx, tx, record, *publication, now); err != nil {
			return err
		}
	}
	if consolidation != nil {
		if err := insertConsolidationReceipt(ctx, tx, headRevision,
			mutation.ConsolidationReceipt, record); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_heads(
		workspace_id,canonical_key_sha256,block_id,commit_revision
	) VALUES(?,?,?,?)`, record.WorkspaceID, keyHash, record.BlockID, headRevision); err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE canonical_head_outbox SET status='committed',
		error='',updated_at=? WHERE commit_revision=? AND status='pending'`, now, headRevision)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("canonical root reservation completion rejected")
	}
	return nil
}
