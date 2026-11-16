package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type PendingPublication struct {
	CommitRevision int64
	RecordID       string
	Receipt        memory.PublicationCommitReceipt
}

// PendingPublication finds an immutable staged publication by its request
// identity. Retrying the original journal is necessary when the catalog has
// changed since staging, because rebuilding the record would change its bound
// membership even though the publication request is identical.
func (index *Index) PendingPublication(ctx context.Context, receiptID string) (*PendingPublication, error) {
	if receiptID == "" {
		return nil, errors.New("pending publication requires a receipt ID")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT h.commit_revision,h.successor_id,h.mutation_action,
		h.payload_id,h.payload_sha256,p.payload_json,p.payload_sha256,p.payload_kind,
		b.payload_json,o.status
		FROM canonical_head_outbox h JOIN canonical_payloads p ON p.payload_id=h.payload_id
		JOIN blocks b ON b.block_id=h.successor_id
		JOIN write_outbox o ON o.commit_revision=h.commit_revision
		WHERE h.workspace_id=? AND h.status='pending'`, index.workspaceID)
	if err != nil {
		return nil, err
	}
	var found *PendingPublication
	for rows.Next() {
		var revision int64
		var successorID, action, payloadID, checksum, mutationJSON, storedChecksum,
			kind, recordJSON, outboxStatus string
		if err := rows.Scan(&revision, &successorID, &action, &payloadID,
			&checksum, &mutationJSON, &storedChecksum, &kind, &recordJSON,
			&outboxStatus); err != nil {
			rows.Close()
			return nil, err
		}
		var mutation stagedSuccessorMutation
		if err := json.Unmarshal([]byte(mutationJSON), &mutation); err != nil {
			rows.Close()
			return nil, errors.New("pending publication mutation is malformed")
		}
		if string(mutation.PublicationReceipt) == "null" || len(mutation.PublicationReceipt) == 0 {
			continue
		}
		receipt, err := decodePublicationReceipt(mutation.PublicationReceipt)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if receipt.ReceiptID != receiptID {
			continue
		}
		if found != nil || payloadID != "mutation:"+checksum || checksum != storedChecksum ||
			kind != "mutation" || checksum != sha256Hex("mutation\x00"+mutationJSON) ||
			outboxStatus != "pending" || mutation.Action != action ||
			(mutation.Action == "insert" && mutation.SupersededBlockID != "") ||
			(mutation.Action != "insert" && mutation.SupersededBlockID == "") {
			rows.Close()
			return nil, errors.New("pending publication journal is duplicated or invalid")
		}
		recordChecksum := sha256Hex("record\x00" + recordJSON)
		if mutation.RecordPayloadID != "record:"+recordChecksum ||
			mutation.RecordChecksum != recordChecksum {
			rows.Close()
			return nil, errors.New("pending publication record payload differs from mutation")
		}
		record, err := memory.DecodeCanonicalRecord([]byte(recordJSON))
		if err != nil || record.BlockID != successorID ||
			record.WorkspaceID != index.workspaceID || record.ProfileID != index.profileID ||
			receipt.ValidateForRecord(record) != nil ||
			(receipt.MaterializedCopy && action != "insert") ||
			(!receipt.MaterializedCopy && action != "promote" && action != "correction") {
			rows.Close()
			return nil, errors.New("pending publication identity differs from staged record")
		}
		found = &PendingPublication{CommitRevision: revision, RecordID: successorID,
			Receipt: receipt}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return found, nil
}
