package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func validateSuccessorAction(record memory.MemoryRecord, action, rollbackTargetID string) error {
	if record.CanonicalKey == "" || record.CanonicalKey != strings.TrimSpace(record.CanonicalKey) ||
		record.RevisionOf == "" || record.RevisionOf != strings.TrimSpace(record.RevisionOf) ||
		record.RecordedAt == "" || record.ValidFrom == "" {
		return errors.New("canonical successor requires a key, predecessor and temporal identity")
	}
	switch action {
	case "supersede", "correction", "deprecate", "retract", "rollback", "promote", "quarantine":
	default:
		return fmt.Errorf("unsupported canonical successor action %q", action)
	}
	if (action == "rollback") != (rollbackTargetID != "") {
		return errors.New("rollback target is required only for rollback mutations")
	}
	switch action {
	case "deprecate":
		if record.Status != "deprecated" {
			return errors.New("deprecate action requires deprecated successor")
		}
	case "retract":
		if record.Status != "retracted" {
			return errors.New("retract action requires retracted successor")
		}
	case "promote":
		if record.Status != "accepted" {
			return errors.New("promote action requires accepted successor")
		}
	case "quarantine":
		if record.Status != "quarantined" {
			return errors.New("quarantine action requires quarantined successor")
		}
	}
	return record.ValidateForStorage()
}

func successorEdge(record memory.MemoryRecord, action, rollbackTargetID, createdAt string) map[string]any {
	return map[string]any{
		"src_id":        record.BlockID,
		"dst_id":        record.RevisionOf,
		"edge_type":     "supersedes",
		"workspace_id":  record.WorkspaceID,
		"run_id":        record.RunID,
		"subtask_id":    record.SubtaskID,
		"reason":        action,
		"weight":        json.Number("1.0"),
		"evidence_refs": record.EvidenceRefs,
		"metadata": map[string]string{
			"canonical_key_sha256": sha256Hex(record.CanonicalKey),
			"mutation_action":      action,
			"rollback_target_id":   rollbackTargetID,
		},
		"created_at": createdAt,
	}
}

// PrepareSuccessorWrite performs the compare-and-swap reservation before any
// native write. The successor remains invisible until CompleteProjection
// verifies native state and atomically advances the canonical head.
func (writer *Writer) PrepareSuccessorWrite(ctx context.Context, record memory.MemoryRecord, action, rollbackTargetID string) (WriteReceipt, error) {
	return writer.prepareSuccessorWrite(ctx, record, action, rollbackTargetID, nil, false)
}

// PreparePublishedSuccessorWrite accepts a primary-block move already verified
// by the engine against the catalog's published membership pointer.
func (writer *Writer) PreparePublishedSuccessorWrite(ctx context.Context, record memory.MemoryRecord,
	action, rollbackTargetID string) (WriteReceipt, error) {
	return writer.prepareSuccessorWrite(ctx, record, action, rollbackTargetID, nil, true)
}

// PreparePublicationSuccessorWrite stages the accepted revision and its
// publication receipt in one content-addressed mutation. Completion persists
// the head and receipt in the same SQLite transaction.
func (writer *Writer) PreparePublicationSuccessorWrite(ctx context.Context, record memory.MemoryRecord, action string, publication memory.PublicationCommitReceipt) (WriteReceipt, error) {
	if action != "promote" && action != "correction" {
		return WriteReceipt{}, errors.New("publication successor requires promote or correction action")
	}
	return writer.prepareSuccessorWrite(ctx, record, action, "", &publication, false)
}

func (writer *Writer) prepareSuccessorWrite(ctx context.Context, record memory.MemoryRecord, action, rollbackTargetID string,
	publication *memory.PublicationCommitReceipt, publishedMove bool) (WriteReceipt, error) {
	if record.WorkspaceID != writer.workspaceID || record.ProfileID != writer.profileID {
		return WriteReceipt{}, errors.New("canonical successor scope does not match selected writer")
	}
	if err := validateSuccessorAction(record, action, rollbackTargetID); err != nil {
		return WriteReceipt{}, err
	}
	if publication != nil {
		if err := publication.ValidateForRecord(record); err != nil {
			return WriteReceipt{}, err
		}
		if publication.SourceID != record.RevisionOf && action == "promote" {
			return WriteReceipt{}, errors.New("promotion receipt source is not its predecessor")
		}
	}
	payload, err := canonicalRecordJSON(record)
	if err != nil {
		return WriteReceipt{}, err
	}
	checksum := sha256Hex("record\x00" + payload)
	payloadID := "record:" + checksum
	keyHash := sha256Hex(record.CanonicalKey)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return WriteReceipt{}, err
	}
	defer tx.Rollback()
	var existingPayload string
	err = tx.QueryRowContext(ctx, "SELECT payload_json FROM blocks WHERE block_id=?", record.BlockID).Scan(&existingPayload)
	if err != nil && err != sql.ErrNoRows {
		return WriteReceipt{}, err
	}
	if err == nil {
		if !sameJSON(existingPayload, payload) {
			return WriteReceipt{}, errors.New("canonical successor revisions are immutable")
		}
		var revision int64
		var status, backend, workspace, storedAction, predecessor, stagedMutation string
		if err := tx.QueryRowContext(ctx, `SELECT commit_revision,status,store,workspace_id
			FROM write_outbox WHERE block_id=? ORDER BY commit_revision DESC LIMIT 1`, record.BlockID).Scan(
			&revision, &status, &backend, &workspace,
		); err != nil {
			return WriteReceipt{}, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT h.mutation_action,h.predecessor_id,p.payload_json
			FROM canonical_head_outbox h JOIN canonical_payloads p ON p.payload_id=h.payload_id
			WHERE h.successor_id=? ORDER BY h.commit_revision DESC LIMIT 1`, record.BlockID).Scan(
			&storedAction, &predecessor, &stagedMutation,
		); err != nil {
			return WriteReceipt{}, err
		}
		if backend != writer.outboxStore(record) || workspace != writer.workspaceID || storedAction != action || predecessor != record.RevisionOf {
			return WriteReceipt{}, errors.New("canonical successor retry identity conflicts with journal")
		}
		var staged stagedSuccessorMutation
		if err := json.Unmarshal([]byte(stagedMutation), &staged); err != nil {
			return WriteReceipt{}, err
		}
		if err := samePublicationReceipt(staged.PublicationReceipt, publication); err != nil {
			return WriteReceipt{}, err
		}
		return WriteReceipt{record.BlockID, revision, status != "committed", true}, nil
	}
	var headID string
	if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, record.WorkspaceID, keyHash).Scan(&headID); err != nil {
		if err == sql.ErrNoRows {
			return WriteReceipt{}, errors.New("canonical successor has no current head")
		}
		return WriteReceipt{}, err
	}
	if headID != record.RevisionOf {
		return WriteReceipt{}, errors.New("canonical_head_conflict")
	}
	if action == "correction" {
		if _, err := correctionPromotionSourceTx(ctx, tx, record, record.RevisionOf, publication); err != nil {
			return WriteReceipt{}, err
		}
	}
	if publication != nil {
		if err := validatePublicationSourceTx(ctx, tx, *publication); err != nil {
			return WriteReceipt{}, err
		}
		if err := ensurePublicationReceiptUnused(ctx, tx, publication.ReceiptID); err != nil {
			return WriteReceipt{}, err
		}
	}
	var predecessorPayload, predecessorStatus, predecessorBackend string
	if err := tx.QueryRowContext(ctx, `SELECT b.payload_json,o.status,o.store FROM blocks b
		JOIN write_outbox o ON o.block_id=b.block_id
		WHERE b.block_id=? AND b.workspace_id=? ORDER BY o.commit_revision DESC LIMIT 1`,
		record.RevisionOf, record.WorkspaceID,
	).Scan(&predecessorPayload, &predecessorStatus, &predecessorBackend); err != nil {
		return WriteReceipt{}, err
	}
	predecessor, err := memory.DecodeCanonicalRecord([]byte(predecessorPayload))
	if err != nil {
		return WriteReceipt{}, err
	}
	if predecessor.CanonicalKey != record.CanonicalKey || predecessor.ProfileID != record.ProfileID ||
		predecessor.BlockMembership == nil || predecessorStatus != "committed" ||
		predecessorBackend != writer.outboxStore(predecessor) {
		return WriteReceipt{}, errors.New("canonical predecessor identity or native write is invalid")
	}
	if action != "correction" && !publishedMove &&
		predecessor.BlockMembership.PrimaryBlockID != record.BlockMembership.PrimaryBlockID {
		return WriteReceipt{}, errors.New("only an explicit correction may move a revision between primary blocks")
	}
	if rollbackTargetID != "" {
		var targetPayload string
		if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM blocks WHERE block_id=? AND workspace_id=?`,
			rollbackTargetID, record.WorkspaceID).Scan(&targetPayload); err != nil {
			return WriteReceipt{}, err
		}
		target, err := memory.DecodeCanonicalRecord([]byte(targetPayload))
		if err != nil || target.CanonicalKey != record.CanonicalKey || target.ProfileID != record.ProfileID {
			return WriteReceipt{}, errors.New("rollback target is outside the canonical chain")
		}
		var targetKeyHash, targetStatus string
		if err := tx.QueryRowContext(ctx, `SELECT m.canonical_key_sha256,h.status
			FROM canonical_mutations m JOIN canonical_head_outbox h
			ON h.commit_revision=m.commit_revision
			WHERE m.successor_id=? AND m.workspace_id=?`,
			rollbackTargetID, record.WorkspaceID).Scan(&targetKeyHash, &targetStatus); err != nil {
			return WriteReceipt{}, err
		}
		if targetKeyHash != keyHash || targetStatus != "committed" {
			return WriteReceipt{}, errors.New("rollback target is not committed in the canonical chain")
		}
	}
	var pending int64
	err = tx.QueryRowContext(ctx, `SELECT commit_revision FROM canonical_head_outbox
		WHERE workspace_id=? AND canonical_key_sha256=? AND status='pending'`, record.WorkspaceID, keyHash).Scan(&pending)
	if err == nil {
		return WriteReceipt{}, errors.New("canonical_head_pending")
	}
	if err != sql.ErrNoRows {
		return WriteReceipt{}, err
	}
	revision, err := writer.insertStagedRecordTx(ctx, tx, record, payload, payloadID, checksum, keyHash, now)
	if err != nil {
		return WriteReceipt{}, err
	}
	edge := successorEdge(record, action, rollbackTargetID, time.Now().UTC().Format(time.RFC3339))
	mutationJSON, err := canonicalMutationJSONWithPublication(payloadID, checksum, action, record.RevisionOf, rollbackTargetID, edge, publication)
	if err != nil {
		return WriteReceipt{}, err
	}
	mutationChecksum := sha256Hex("mutation\x00" + mutationJSON)
	mutationID := "mutation:" + mutationChecksum
	if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_payloads(
		payload_id,payload_sha256,payload_kind,payload_json,created_at
	) VALUES(?,?,?,?,?)`, mutationID, mutationChecksum, "mutation", mutationJSON, now); err != nil {
		return WriteReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_head_outbox(
		workspace_id,canonical_key_sha256,predecessor_id,successor_id,
		successor_status,successor_authority,mutation_action,rollback_target_id,
		payload_id,payload_sha256,status,error,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,'pending','',?,?)`,
		record.WorkspaceID, keyHash, record.RevisionOf, record.BlockID, record.Status,
		record.Authority, action, rollbackTargetID, mutationID, mutationChecksum, now, now,
	); err != nil {
		return WriteReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return WriteReceipt{}, err
	}
	return WriteReceipt{record.BlockID, revision, true, false}, nil
}
