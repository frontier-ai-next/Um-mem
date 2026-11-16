package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func decodeOutcomeCreditEnvelope(encoded string) (memory.OutcomeCreditEvent, error) {
	var envelope struct {
		Payload       json.RawMessage `json:"payload"`
		PayloadSHA256 string          `json:"payload_sha256"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil || len(envelope.Payload) == 0 {
		return memory.OutcomeCreditEvent{}, errors.New("memory decision journal payload is invalid")
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return memory.OutcomeCreditEvent{}, errors.New("memory decision journal payload is invalid")
	}
	canonical, err := jsoncompat.MarshalPython(raw, false)
	if err != nil {
		return memory.OutcomeCreditEvent{}, err
	}
	digest := sha256.Sum256(canonical)
	if envelope.PayloadSHA256 != hex.EncodeToString(digest[:]) {
		return memory.OutcomeCreditEvent{}, errors.New("memory decision journal payload hash mismatch")
	}
	var credit memory.OutcomeCreditEvent
	strict := json.NewDecoder(bytes.NewReader(envelope.Payload))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&credit); err != nil {
		return memory.OutcomeCreditEvent{}, fmt.Errorf("outcome credit payload is invalid: %w", err)
	}
	if err := credit.Validate(); err != nil {
		return memory.OutcomeCreditEvent{}, err
	}
	return credit, nil
}

// OutcomeCreditsForMemory reads Python-compatible usage journal envelopes in
// insertion order. Each payload hash and typed credit is checked before use.
func (index *Index) OutcomeCreditsForMemory(ctx context.Context, memoryID string) ([]memory.OutcomeCreditEvent, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return nil, errors.New("memory_id is required")
	}
	rows, err := index.db.QueryContext(ctx, `SELECT journal.payload_json FROM usage AS journal
		JOIN json_each(journal.payload_json, '$.payload.memory_ids') AS memory
		WHERE journal.workspace_id=? AND journal.outcome='outcome_credit'
		AND memory.value=? ORDER BY journal.id`, index.workspaceID, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	credits := make([]memory.OutcomeCreditEvent, 0)
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		credit, err := decodeOutcomeCreditEnvelope(encoded)
		if err != nil {
			return nil, err
		}
		if credit.WorkspaceID != index.workspaceID || !slices.Contains(credit.MemoryIDs, memoryID) {
			return nil, errors.New("outcome credit journal scope mismatch")
		}
		credits = append(credits, credit)
	}
	return credits, rows.Err()
}

// OutcomeCredits reads the hash-verified attribution journal for one decision.
func (index *Index) OutcomeCredits(ctx context.Context, decisionID string) ([]memory.OutcomeCreditEvent, error) {
	decisionID = strings.TrimSpace(decisionID)
	if decisionID == "" {
		return nil, errors.New("decision_id is required")
	}
	rows, err := index.db.QueryContext(ctx, `SELECT payload_json FROM usage WHERE workspace_id=? AND outcome='outcome_credit'
	 AND json_extract(payload_json,'$.payload.decision_id')=? ORDER BY id`, index.workspaceID, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []memory.OutcomeCreditEvent{}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		credit, err := decodeOutcomeCreditEnvelope(encoded)
		if err != nil {
			return nil, err
		}
		if credit.DecisionID != decisionID || credit.WorkspaceID != index.workspaceID {
			return nil, errors.New("outcome credit journal linkage is invalid")
		}
		result = append(result, credit)
	}
	return result, rows.Err()
}

// RecordOutcomeCredit writes a verified, evidence-backed attribution after
// checking exact applied-usage links under the SQLite writer reservation.
func (writer *Writer) RecordOutcomeCredit(ctx context.Context, credit memory.OutcomeCreditEvent) (DecisionJournalReceipt, error) {
	if credit.WorkspaceID != writer.workspaceID {
		return DecisionJournalReceipt{}, errors.New("outcome credit workspace mismatch")
	}
	connection, err := beginImmediate(ctx, writer.db)
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	defer rollbackImmediate(connection)
	decision, err := getMemoryDecision(ctx, connection, writer.workspaceID, credit.DecisionID)
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	if decision == nil {
		return DecisionJournalReceipt{}, errors.New("memory_decision_missing")
	}
	if credit.TaskSignature != decision.TaskSignature || credit.ActionTraceID != decision.ActionTraceID {
		return DecisionJournalReceipt{}, errors.New("outcome credit task and action linkage is invalid")
	}
	usageEvents := make([]memory.MemoryUsageEvent, 0, len(credit.AppliedUsageEventIDs))
	for _, eventID := range credit.AppliedUsageEventIDs {
		rows, err := connection.QueryContext(ctx, `SELECT payload_json FROM usage WHERE block_id=? AND workspace_id=? AND outcome LIKE 'memory_usage:%'`,
			"memory-usage-event:"+eventID, writer.workspaceID)
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		encoded := []string{}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return DecisionJournalReceipt{}, err
			}
			encoded = append(encoded, raw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return DecisionJournalReceipt{}, err
		}
		if err := rows.Close(); err != nil {
			return DecisionJournalReceipt{}, err
		}
		if len(encoded) != 1 {
			return DecisionJournalReceipt{}, errors.New("outcome credit usage linkage is invalid")
		}
		usage, err := decodedUsage(encoded[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		if usage.EventID != eventID || usage.DecisionID != credit.DecisionID || usage.WorkspaceID != writer.workspaceID {
			return DecisionJournalReceipt{}, errors.New("outcome credit usage linkage is invalid")
		}
		usageEvents = append(usageEvents, usage)
	}
	usageMemoryIDs := make(map[string]bool)
	for _, usage := range usageEvents {
		usageMemoryIDs[usage.MemoryID] = true
	}
	for memoryID := range usageMemoryIDs {
		if !slices.Contains(credit.MemoryIDs, memoryID) {
			return DecisionJournalReceipt{}, errors.New("outcome credit memory linkage is invalid")
		}
	}
	if credit.Outcome == "helped" || credit.Outcome == "hurt" {
		if len(usageEvents) == 0 || len(usageMemoryIDs) != len(credit.MemoryIDs) {
			return DecisionJournalReceipt{}, errors.New("outcome credit requires exact applied usage linkage")
		}
		for _, usage := range usageEvents {
			if usage.UsageType != "applied" {
				return DecisionJournalReceipt{}, errors.New("outcome credit requires exact applied usage linkage")
			}
		}
	}
	blockID := "outcome-credit-event:" + credit.CreditID
	rows, err := journalRows(ctx, connection, writer.workspaceID, blockID, "outcome_credit")
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	if credit.CreatedAt == "" && len(rows) == 1 {
		prior, err := decodeOutcomeCreditEnvelope(rows[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		credit.CreatedAt = prior.CreatedAt
	}
	if credit.CreatedAt == "" {
		credit.CreatedAt = time.Now().UTC().Format("2006-01-02T15:04:05Z")
	}
	if err := credit.Validate(); err != nil {
		return DecisionJournalReceipt{}, err
	}
	payload, err := jsoncompat.MarshalPython(credit, false)
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	envelope := decisionEnvelope(payload)
	if len(rows) > 0 {
		if len(rows) != 1 || rows[0] != envelope {
			return DecisionJournalReceipt{}, errors.New("outcome_credit_idempotency_conflict")
		}
		prior, err := decodeOutcomeCreditEnvelope(rows[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		if !reflect.DeepEqual(prior, credit) {
			return DecisionJournalReceipt{}, errors.New("outcome_credit_idempotency_conflict")
		}
		if err := commitImmediate(ctx, connection); err != nil {
			return DecisionJournalReceipt{}, err
		}
		return DecisionJournalReceipt{OK: true, CreditID: credit.CreditID, Backend: writer.backendID, Replayed: true}, nil
	}
	if credit.Outcome == "helped" || credit.Outcome == "hurt" {
		for _, eventID := range credit.AppliedUsageEventIDs {
			var exists int
			err := connection.QueryRowContext(ctx, `SELECT 1 FROM usage AS journal
			 JOIN json_each(journal.payload_json,'$.payload.applied_usage_event_ids') AS applied
			 WHERE journal.workspace_id=? AND journal.outcome='outcome_credit'
			 AND json_extract(journal.payload_json,'$.payload.outcome') IN ('helped','hurt')
			 AND applied.value=? LIMIT 1`, writer.workspaceID, eventID).Scan(&exists)
			if err != nil && err != sql.ErrNoRows {
				return DecisionJournalReceipt{}, err
			}
			if err == nil {
				return DecisionJournalReceipt{}, errors.New("outcome_credit_applied_usage_conflict")
			}
		}
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO usage(block_id,workspace_id,outcome,payload_json) VALUES(?,?,?,?)`,
		blockID, credit.WorkspaceID, "outcome_credit", envelope); err != nil {
		return DecisionJournalReceipt{}, err
	}
	if err := commitImmediate(ctx, connection); err != nil {
		return DecisionJournalReceipt{}, err
	}
	return DecisionJournalReceipt{OK: true, CreditID: credit.CreditID, Backend: writer.backendID}, nil
}
