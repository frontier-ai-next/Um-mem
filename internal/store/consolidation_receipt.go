package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func decodeConsolidationReceipt(raw json.RawMessage) (memory.ConsolidationCommitReceipt, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != 3 {
		return memory.ConsolidationCommitReceipt{}, errors.New("consolidation receipt payload has invalid fields")
	}
	for _, field := range []string{"receipt_id", "evidence_refs", "committed_ids"} {
		if _, exists := fields[field]; !exists {
			return memory.ConsolidationCommitReceipt{}, errors.New("consolidation receipt is missing a required field")
		}
	}
	var receipt memory.ConsolidationCommitReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func sameConsolidationReceipt(raw json.RawMessage, expected *memory.ConsolidationCommitReceipt) error {
	if expected == nil {
		if string(raw) != "null" {
			return errors.New("canonical root retry consolidation receipt differs")
		}
		return nil
	}
	actual, err := decodeConsolidationReceipt(raw)
	if err != nil || !reflect.DeepEqual(actual, *expected) {
		return errors.New("canonical root retry consolidation receipt differs")
	}
	return nil
}

func ensureConsolidationReceiptUnused(ctx context.Context, tx *sql.Tx, receiptID string) error {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT payload_json FROM canonical_mutation_receipts
		WHERE receipt_id=? LIMIT 1`, receiptID).Scan(&existing)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("consolidation receipt identity already committed")
}

func insertConsolidationReceipt(ctx context.Context, tx *sql.Tx, revision int64,
	raw json.RawMessage, record memory.MemoryRecord) error {
	receipt, err := decodeConsolidationReceipt(raw)
	if err != nil {
		return err
	}
	if err := receipt.ValidateForRecord(record); err != nil {
		return err
	}
	if err := ensureConsolidationReceiptUnused(ctx, tx, receipt.ReceiptID); err != nil {
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

// validateConsolidationSourcesTx closes the gap between tribunal assessment
// and the canonical commit. A corrected or retracted source cannot be used by
// a pending native projection to publish a stale consolidation.
func validateConsolidationSourcesTx(ctx context.Context, tx *sql.Tx,
	record memory.MemoryRecord) error {
	raw, hasSources := record.Metadata["source_record_ids"]
	if !hasSources {
		return nil
	}
	ids := []string{}
	switch values := raw.(type) {
	case []string:
		ids = append(ids, values...)
	case []any:
		for _, value := range values {
			id, ok := value.(string)
			if !ok {
				return errors.New("consolidation source IDs must be strings")
			}
			ids = append(ids, id)
		}
	default:
		return errors.New("consolidation source IDs are malformed")
	}
	if len(ids) < 2 {
		return errors.New("consolidation requires at least two canonical sources")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || id != strings.TrimSpace(id) || seen[id] || id == record.BlockID {
			return errors.New("consolidation source identities are invalid")
		}
		seen[id] = true
		var payload, committedAt string
		if err := tx.QueryRowContext(ctx, `SELECT payload_json,committed_at FROM blocks
			WHERE block_id=? AND workspace_id=?`, id, record.WorkspaceID).Scan(&payload, &committedAt); err != nil {
			return errors.New("consolidation source is no longer committed")
		}
		source, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil || committedAt == "" || source.ProfileID != record.ProfileID ||
			source.CanonicalKey == "" || source.Scope != "workspace" ||
			source.Retention != "durable" || source.DeliveryPolicy == "hidden" ||
			source.Status == "quarantined" || source.Status == "superseded" ||
			source.Status == "deprecated" || source.Status == "retracted" {
			return errors.New("consolidation source is no longer an eligible canonical record")
		}
		var headID string
		if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
			WHERE workspace_id=? AND canonical_key_sha256=?`, record.WorkspaceID,
			sha256Hex(source.CanonicalKey)).Scan(&headID); err != nil || headID != id {
			return errors.New("consolidation source is no longer the canonical head")
		}
	}
	return nil
}

func (index *Index) ConsolidationReceipt(ctx context.Context, receiptID string) (*memory.ConsolidationCommitReceipt, error) {
	var payload, recordPayload string
	err := index.db.QueryRowContext(ctx, `SELECT r.payload_json,b.payload_json
		FROM canonical_mutation_receipts r
		JOIN canonical_mutations m ON m.commit_revision=r.commit_revision
		JOIN blocks b ON b.block_id=m.successor_id
		WHERE r.receipt_id=? AND m.workspace_id=? AND b.workspace_id=? AND b.committed_at<>''`,
		receiptID, index.workspaceID, index.workspaceID).Scan(&payload, &recordPayload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := memory.DecodeCanonicalRecord([]byte(recordPayload))
	if err != nil {
		return nil, err
	}
	if record.ProfileID != index.profileID {
		return nil, nil
	}
	receipt, err := decodeConsolidationReceipt(json.RawMessage(payload))
	if err != nil {
		return nil, err
	}
	if err := receipt.ValidateForRecord(record); err != nil {
		return nil, err
	}
	return &receipt, nil
}
