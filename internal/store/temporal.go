package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func parseTemporalTime(value string) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("invalid temporal timestamp %q", value)
}

func (index *Index) successorBoundaryTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord) (time.Time, string, bool, error) {
	if record.CanonicalKey == "" {
		return time.Time{}, "", false, nil
	}
	var successorID, payloadID, checksum, payload, storedChecksum, kind string
	err := tx.QueryRowContext(ctx, `SELECT m.successor_id,m.record_payload_id,m.record_payload_sha256,
		p.payload_json,p.payload_sha256,p.payload_kind
		FROM canonical_mutations m LEFT JOIN canonical_payloads p ON p.payload_id=m.record_payload_id
		WHERE m.workspace_id=? AND m.canonical_key_sha256=? AND m.predecessor_id=?
		ORDER BY m.commit_revision LIMIT 1`, index.workspaceID, sha256Hex(record.CanonicalKey), record.BlockID).Scan(
		&successorID, &payloadID, &checksum, &payload, &storedChecksum, &kind,
	)
	if err == sql.ErrNoRows {
		return time.Time{}, "", false, nil
	}
	if err != nil {
		return time.Time{}, "", false, err
	}
	if kind != "record" || checksum != storedChecksum || payloadID != "record:"+checksum ||
		checksum != sha256Hex("record\x00"+payload) {
		return time.Time{}, "", false, errors.New("canonical successor payload identity is invalid")
	}
	successor, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil {
		return time.Time{}, "", false, err
	}
	if successor.BlockID != successorID || successor.RevisionOf != record.BlockID ||
		successor.CanonicalKey != record.CanonicalKey || successor.ProfileID != record.ProfileID {
		return time.Time{}, "", false, errors.New("canonical successor boundary identity is invalid")
	}
	parsed, present, err := parseTemporalTime(successor.ValidFrom)
	return parsed, successor.ValidFrom, present, err
}

func (index *Index) eventWindowTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord, includeSuccessor bool) (memory.MemoryRecord, time.Time, bool, time.Time, bool, error) {
	start, hasStart, err := parseTemporalTime(record.ValidFrom)
	if err != nil {
		return record, time.Time{}, false, time.Time{}, false, err
	}
	end, hasEnd, err := parseTemporalTime(record.ValidTo)
	if err != nil {
		return record, time.Time{}, false, time.Time{}, false, err
	}
	if includeSuccessor {
		boundary, boundaryRaw, hasBoundary, err := index.successorBoundaryTx(ctx, tx, record)
		if err != nil {
			return record, time.Time{}, false, time.Time{}, false, err
		}
		if hasBoundary && (!hasEnd || boundary.Before(end)) {
			end, hasEnd = boundary, true
			if hasStart {
				record.ValidTo = boundaryRaw
			}
		}
	}
	return record, start, hasStart, end, hasEnd, nil
}

func (index *Index) eventMatchesTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord, plan memory.RecallPlan) (memory.MemoryRecord, bool, error) {
	projected, start, hasStart, end, hasEnd, err := index.eventWindowTx(ctx, tx, record, plan.KnownAsOf == "")
	if err != nil {
		return record, false, err
	}
	if plan.AsOf == "" {
		return projected, true, nil
	}
	bound, _, err := parseTemporalTime(plan.AsOf)
	if err != nil {
		return record, false, err
	}
	if hasStart && bound.Before(start) || hasEnd && !bound.Before(end) {
		return projected, false, nil
	}
	expiration, hasExpiration, err := parseTemporalTime(record.ExpiresAt)
	if err != nil {
		return record, false, err
	}
	if hasExpiration && !bound.Before(expiration) {
		return projected, false, nil
	}
	return projected, true, nil
}

func (index *Index) committedByTx(ctx context.Context, tx *sql.Tx, recordID string, knownBound time.Time) (bool, error) {
	var committedAt string
	if err := tx.QueryRowContext(ctx, `SELECT committed_at FROM blocks
		WHERE workspace_id=? AND block_id=?`, index.workspaceID, recordID).Scan(&committedAt); err != nil {
		return false, err
	}
	committed, hasCommit, err := parseTemporalTime(committedAt)
	if err != nil {
		return false, err
	}
	return hasCommit && !committed.After(knownBound), nil
}

func (index *Index) knownHeadTx(ctx context.Context, tx *sql.Tx, canonicalKey string, knownBound time.Time) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT successor_id,committed_at FROM canonical_mutations
		WHERE workspace_id=? AND canonical_key_sha256=? ORDER BY commit_revision`,
		index.workspaceID, sha256Hex(canonicalKey))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	headID := ""
	for rows.Next() {
		var successorID, committedAt string
		if err := rows.Scan(&successorID, &committedAt); err != nil {
			return "", err
		}
		committed, hasCommit, err := parseTemporalTime(committedAt)
		if err != nil || !hasCommit {
			return "", errors.New("canonical mutation has invalid commit time")
		}
		if !committed.After(knownBound) {
			headID = successorID
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if headID != "" {
		return headID, nil
	}
	// Stores predating the canonical mutation journal can still have a root
	// temporal row. Select only a block committed by the knowledge cutoff.
	rows, err = tx.QueryContext(ctx, `SELECT t.block_id,b.committed_at FROM memory_temporal_index t
		JOIN blocks b ON b.block_id=t.block_id
		WHERE t.workspace_id=? AND t.canonical_key_sha256=?`,
		index.workspaceID, sha256Hex(canonicalKey))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var latest time.Time
	for rows.Next() {
		var recordID, committedAt string
		if err := rows.Scan(&recordID, &committedAt); err != nil {
			return "", err
		}
		committed, hasCommit, err := parseTemporalTime(committedAt)
		if err != nil {
			return "", err
		}
		if hasCommit && !committed.After(knownBound) &&
			(headID == "" || committed.After(latest) || committed.Equal(latest) && recordID < headID) {
			headID, latest = recordID, committed
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return headID, nil
}

func (index *Index) eventHeadTx(ctx context.Context, tx *sql.Tx, canonicalKey string, eventBound time.Time) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT m.successor_id,b.payload_json
		FROM canonical_mutations m JOIN blocks b ON b.block_id=m.successor_id
		WHERE m.workspace_id=? AND m.canonical_key_sha256=? ORDER BY m.commit_revision`,
		index.workspaceID, sha256Hex(canonicalKey))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	headID := ""
	for rows.Next() {
		var recordID, payload string
		if err := rows.Scan(&recordID, &payload); err != nil {
			return "", err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil || record.BlockID != recordID || record.CanonicalKey != canonicalKey {
			return "", errors.New("canonical event head payload is invalid")
		}
		_, start, hasStart, end, hasEnd, err := index.eventWindowTx(ctx, tx, record, true)
		if err != nil {
			return "", err
		}
		if (!hasStart || !eventBound.Before(start)) && (!hasEnd || eventBound.Before(end)) {
			headID = recordID
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return headID, nil
}
