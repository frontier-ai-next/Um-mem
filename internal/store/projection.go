package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// ProjectionResult is returned only after the selected backend has checked its
// durable native state. A successful HTTP write alone is not verification.
type ProjectionResult struct {
	NativeID string
	Verified bool
	Skipped  bool
}

type NativeProjector interface {
	BackendID() string
	RequiresNative(memory.MemoryRecord) bool
	Project(context.Context, memory.MemoryRecord, string, int64) (ProjectionResult, error)
}

// CompleteProjection recovers a pending write from its immutable payload. A
// backend error leaves the journal pending so the same native ID can be checked
// and retried after a process crash or transient failure.
func (writer *Writer) CompleteProjection(ctx context.Context, revision int64, projector NativeProjector) (WriteReceipt, error) {
	if projector == nil || projector.BackendID() != writer.backendID {
		return WriteReceipt{}, errors.New("selected backend projector does not match write journal")
	}
	var blockID, workspaceID, backendID, payloadID, checksum, status, payload, blockPayload, profileID, storedChecksum, payloadKind string
	err := writer.db.QueryRowContext(ctx, `SELECT o.block_id,o.workspace_id,o.store,o.payload_id,o.payload_sha256,
		o.status,p.payload_json,b.payload_json,p.payload_sha256,p.payload_kind
		FROM write_outbox o JOIN canonical_payloads p ON p.payload_id=o.payload_id
		JOIN blocks b ON b.block_id=o.block_id WHERE o.commit_revision=?`, revision).Scan(
		&blockID, &workspaceID, &backendID, &payloadID, &checksum, &status, &payload, &blockPayload,
		&storedChecksum, &payloadKind,
	)
	if err != nil {
		return WriteReceipt{}, err
	}
	if workspaceID != writer.workspaceID ||
		payloadKind != "record" || storedChecksum != checksum ||
		payloadID != "record:"+checksum || checksum != sha256Hex("record\x00"+payload) ||
		!sameJSON(payload, blockPayload) {
		return WriteReceipt{}, errors.New("selected backend write journal or payload identity is invalid")
	}
	record, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil {
		return WriteReceipt{}, fmt.Errorf("selected backend write payload is invalid: %w", err)
	}
	profileID = record.ProfileID
	if record.BlockID != blockID || record.WorkspaceID != workspaceID || profileID != writer.profileID {
		return WriteReceipt{}, errors.New("selected backend write payload identity is invalid")
	}
	if backendID != writer.outboxStore(record) {
		return WriteReceipt{}, errors.New("selected backend write store differs from canonical record")
	}
	if err := record.ValidateForStorage(); err != nil {
		return WriteReceipt{}, fmt.Errorf("selected backend write payload is not storable: %w", err)
	}
	if status == "committed" {
		if record.CanonicalKey != "" {
			var headStatus string
			if err := writer.db.QueryRowContext(ctx, `SELECT status FROM canonical_head_outbox
				WHERE successor_id=? ORDER BY commit_revision DESC LIMIT 1`, blockID).Scan(&headStatus); err != nil {
				return WriteReceipt{}, err
			}
			if headStatus != "committed" {
				return WriteReceipt{}, errors.New("canonical head is not committed with the native write")
			}
		}
		return WriteReceipt{blockID, revision, false, true}, nil
	}
	if status != "pending" {
		return WriteReceipt{}, fmt.Errorf("selected backend write journal is %s", status)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// Persist the boundary before calling a provider. On replay the projector
	// first checks whether the same document was already retained.
	_, err = writer.db.ExecContext(ctx, `UPDATE write_outbox SET error=?,updated_at=?
		WHERE commit_revision=? AND status='pending' AND error=''`,
		`{"kind":"native_projection","attempts":1,"retry_at":0,"state":"started"}`, now, revision)
	if err != nil {
		return WriteReceipt{}, err
	}
	started := time.Now()
	result, err := projector.Project(ctx, record, payload, revision)
	if err != nil {
		return WriteReceipt{}, err
	}
	if !result.Verified {
		return WriteReceipt{}, errors.New("selected backend did not verify native projection")
	}
	if result.Skipped == projector.RequiresNative(record) {
		return WriteReceipt{}, errors.New("selected backend projection skip violates native eligibility")
	}
	if (!result.Skipped && result.NativeID == "") || (result.Skipped && result.NativeID != "") {
		return WriteReceipt{}, errors.New("selected backend projection receipt is incomplete")
	}
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return WriteReceipt{}, err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM write_outbox WHERE commit_revision=?", revision).Scan(&current); err != nil {
		return WriteReceipt{}, err
	}
	if current != "pending" {
		return WriteReceipt{}, errors.New("selected backend write journal changed during projection")
	}
	if result.NativeID != "" {
		var existing string
		err := tx.QueryRowContext(ctx, "SELECT native_id FROM write_outbox_native_refs WHERE commit_revision=?", revision).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return WriteReceipt{}, err
		}
		if existing != "" && existing != result.NativeID {
			return WriteReceipt{}, errors.New("selected backend native reference is immutable")
		}
		if existing == "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO write_outbox_native_refs(commit_revision,native_id,created_at)
				VALUES(?,?,?)`, revision, result.NativeID, now); err != nil {
				return WriteReceipt{}, err
			}
		}
	}
	updatedBlock, err := tx.ExecContext(ctx, `UPDATE blocks SET committed_at=?
		WHERE block_id=? AND workspace_id=? AND committed_at=''`, now, record.BlockID, record.WorkspaceID)
	if err != nil {
		return WriteReceipt{}, err
	}
	blockCount, err := updatedBlock.RowsAffected()
	if err != nil || blockCount != 1 {
		return WriteReceipt{}, errors.New("canonical block completion rejected")
	}
	var headErr error
	if record.RevisionOf != "" {
		headErr = commitSuccessorCanonicalHead(ctx, tx, record, payloadID, checksum, now)
	} else {
		headErr = commitRootCanonicalHead(ctx, tx, record, payloadID, checksum, now)
	}
	if headErr != nil {
		return WriteReceipt{}, headErr
	}
	if record.RevisionOf == "" {
		if record.Metadata["pending_host_approval"] == true {
			if err := writer.commitCorrectionProposalTx(ctx, tx, record); err != nil {
				return WriteReceipt{}, err
			}
		} else {
			if err := writer.commitRawWitnessProvenanceTx(ctx, tx, record); err != nil {
				return WriteReceipt{}, err
			}
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE write_outbox SET status='committed',error='',updated_at=?
		WHERE commit_revision=? AND status='pending'`, now, revision)
	if err != nil {
		return WriteReceipt{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return WriteReceipt{}, errors.New("selected backend write journal completion rejected")
	}
	if !result.Skipped {
		if partitioner, ok := projector.(interface {
			PartitionIdentity(memory.MemoryRecord) (memory.NativePartitionIdentity, error)
		}); ok {
			identity, err := partitioner.PartitionIdentity(record)
			if err != nil {
				return WriteReceipt{}, err
			}
			recordBytes := int64(len(payload))
			if err := recordPartitionOperation(ctx, tx, identity, PartitionOperation{
				Operation: "write", LatencyMS: float64(time.Since(started).Microseconds()) / 1000,
				RecordBytes: &recordBytes, Success: true,
			}); err != nil {
				return WriteReceipt{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return WriteReceipt{}, err
	}
	return WriteReceipt{blockID, revision, false, false}, nil
}
