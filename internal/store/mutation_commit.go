package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type stagedSuccessorMutation struct {
	RecordPayloadID      string          `json:"record_payload_id"`
	RecordChecksum       string          `json:"record_payload_sha256"`
	SupersededBlockID    string          `json:"superseded_block_id"`
	Action               string          `json:"action"`
	Edge                 json.RawMessage `json:"edge"`
	RollbackTargetID     string          `json:"rollback_target_id"`
	AffectedViewIDs      []string        `json:"affected_view_ids"`
	ConsolidationReceipt json.RawMessage `json:"consolidation_receipt"`
	PublicationReceipt   json.RawMessage `json:"publication_receipt"`
	AmendmentReceipt     json.RawMessage `json:"amendment_receipt"`
}

func commitSuccessorCanonicalHead(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord, recordPayloadID, recordChecksum, now string) error {
	keyHash := sha256Hex(record.CanonicalKey)
	var headRevision int64
	var workspace, storedKeyHash, predecessor, successor, status, authority, action, rollbackTarget string
	var mutationID, mutationChecksum, headStatus, mutationJSON, storedChecksum, mutationKind string
	err := tx.QueryRowContext(ctx, `SELECT h.commit_revision,h.workspace_id,h.canonical_key_sha256,
		h.predecessor_id,h.successor_id,h.successor_status,h.successor_authority,
		h.mutation_action,h.rollback_target_id,h.payload_id,h.payload_sha256,h.status,
		p.payload_json,p.payload_sha256,p.payload_kind
		FROM canonical_head_outbox h LEFT JOIN canonical_payloads p ON p.payload_id=h.payload_id
		WHERE h.successor_id=? ORDER BY h.commit_revision DESC LIMIT 1`, record.BlockID).Scan(
		&headRevision, &workspace, &storedKeyHash, &predecessor, &successor, &status,
		&authority, &action, &rollbackTarget, &mutationID, &mutationChecksum,
		&headStatus, &mutationJSON, &storedChecksum, &mutationKind,
	)
	if err != nil {
		return err
	}
	if workspace != record.WorkspaceID || storedKeyHash != keyHash || predecessor != record.RevisionOf ||
		successor != record.BlockID || status != record.Status || authority != record.Authority ||
		headStatus != "pending" || mutationKind != "mutation" ||
		mutationID != "mutation:"+mutationChecksum || storedChecksum != mutationChecksum ||
		mutationChecksum != sha256Hex("mutation\x00"+mutationJSON) {
		return errors.New("canonical successor reservation or mutation payload is invalid")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(mutationJSON), &rawFields); err != nil || len(rawFields) != 10 {
		return errors.New("canonical successor mutation payload fields are invalid")
	}
	var mutation stagedSuccessorMutation
	if err := json.Unmarshal([]byte(mutationJSON), &mutation); err != nil {
		return err
	}
	if mutation.RecordPayloadID != recordPayloadID || mutation.RecordChecksum != recordChecksum ||
		mutation.SupersededBlockID != predecessor || mutation.Action != action ||
		mutation.RollbackTargetID != rollbackTarget || len(mutation.AffectedViewIDs) != 0 ||
		string(mutation.ConsolidationReceipt) != "null" ||
		string(mutation.AmendmentReceipt) != "null" {
		return errors.New("canonical successor mutation does not match staged record")
	}
	var publication *memory.PublicationCommitReceipt
	if string(mutation.PublicationReceipt) != "null" {
		if action != "promote" && action != "correction" {
			return errors.New("publication receipt requires a promotion or correction")
		}
		decoded, err := decodePublicationReceipt(mutation.PublicationReceipt)
		if err != nil {
			return err
		}
		if err := decoded.ValidateForRecord(record); err != nil {
			return err
		}
		if action == "promote" && decoded.SourceID != predecessor {
			return errors.New("promotion receipt source is not its predecessor")
		}
		if err := validatePublicationSourceTx(ctx, tx, decoded); err != nil {
			return err
		}
		publication = &decoded
	}
	if err := validateSuccessorAction(record, action, rollbackTarget); err != nil {
		return err
	}
	var edge map[string]any
	if err := json.Unmarshal(mutation.Edge, &edge); err != nil || len(edge) != 11 {
		return errors.New("canonical successor supersedes edge is invalid")
	}
	metadata, ok := edge["metadata"].(map[string]any)
	if !ok || edge["src_id"] != record.BlockID || edge["dst_id"] != predecessor ||
		edge["edge_type"] != "supersedes" || edge["workspace_id"] != record.WorkspaceID ||
		edge["run_id"] != record.RunID || edge["subtask_id"] != record.SubtaskID ||
		edge["reason"] != action || edge["created_at"] == "" ||
		metadata["canonical_key_sha256"] != keyHash || metadata["mutation_action"] != action ||
		metadata["rollback_target_id"] != rollbackTarget {
		return errors.New("canonical successor supersedes edge does not match reservation")
	}
	var currentHead string
	if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, record.WorkspaceID, keyHash).Scan(&currentHead); err != nil {
		return err
	}
	if currentHead != predecessor {
		return errors.New("canonical_head_conflict")
	}
	correctionSourceID := ""
	if action == "correction" {
		var err error
		correctionSourceID, err = correctionPromotionSourceTx(ctx, tx, record, predecessor, publication)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_mutations(
		commit_revision,workspace_id,canonical_key_sha256,predecessor_id,
		successor_id,mutation_action,rollback_target_id,record_payload_id,
		record_payload_sha256,recorded_at,committed_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		headRevision, record.WorkspaceID, keyHash, predecessor, record.BlockID,
		action, rollbackTarget, recordPayloadID, recordChecksum, record.RecordedAt, now,
	); err != nil {
		return err
	}
	if string(mutation.PublicationReceipt) != "null" {
		if err := insertPublicationReceipt(ctx, tx, headRevision, mutation.PublicationReceipt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO edges(src_id,dst_id,edge_type,workspace_id,payload_json)
		VALUES(?,?,?,?,?)`, record.BlockID, predecessor, "supersedes", record.WorkspaceID, string(mutation.Edge)); err != nil {
		return err
	}
	dependencySourceID := ""
	switch action {
	case "rollback":
		dependencySourceID = rollbackTarget
	case "promote":
		dependencySourceID = predecessor
	case "correction":
		dependencySourceID = correctionSourceID
	}
	if dependencySourceID != "" {
		if err := inheritDerivedDependencies(ctx, tx, record.WorkspaceID,
			dependencySourceID, record.BlockID, now); err != nil {
			return err
		}
	}
	updated, err := tx.ExecContext(ctx, `UPDATE canonical_heads SET block_id=?,commit_revision=?
		WHERE workspace_id=? AND canonical_key_sha256=? AND block_id=?`,
		record.BlockID, headRevision, record.WorkspaceID, keyHash, predecessor)
	if err != nil {
		return err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("canonical_head_conflict")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memory_temporal_index SET is_current=0,
		valid_to=CASE WHEN valid_to='' THEN ? ELSE valid_to END
		WHERE workspace_id=? AND block_id=? AND is_current=1`, record.ValidFrom, record.WorkspaceID, predecessor); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE record_block_memberships SET is_current=0,
		valid_to=CASE WHEN valid_to='' THEN ? ELSE valid_to END
		WHERE workspace_id=? AND record_id=? AND is_current=1`, record.ValidFrom, record.WorkspaceID, predecessor); err != nil {
		return err
	}
	updated, err = tx.ExecContext(ctx, `UPDATE canonical_head_outbox
		SET status='committed',error='',updated_at=? WHERE commit_revision=? AND status='pending'`, now, headRevision)
	if err != nil {
		return err
	}
	count, err = updated.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("canonical successor reservation %s completion rejected", strconv.FormatInt(headRevision, 10))
	}
	return nil
}

func inheritDerivedDependencies(ctx context.Context, tx *sql.Tx, workspaceID,
	targetID, successorID, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM edges
		WHERE workspace_id=? AND src_id=? AND edge_type='derived_from' ORDER BY dst_id`,
		workspaceID, targetID)
	if err != nil {
		return err
	}
	var inherited []memory.EdgeRecord
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		var edge memory.EdgeRecord
		if err := json.Unmarshal([]byte(payload), &edge); err != nil || edge.SrcID != targetID ||
			edge.WorkspaceID != workspaceID || edge.EdgeType != "derived_from" {
			rows.Close()
			return errors.New("rollback target dependency edge is invalid")
		}
		inherited = append(inherited, edge)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, edge := range inherited {
		edge.SrcID, edge.CreatedAt = successorID, now
		payload, err := json.Marshal(edge)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO edges(src_id,dst_id,edge_type,workspace_id,payload_json)
			VALUES(?,?,?,?,?)`, successorID, edge.DstID, edge.EdgeType,
			workspaceID, string(payload)); err != nil {
			return err
		}
	}
	return nil
}
