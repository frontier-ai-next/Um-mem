package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type DecisionJournalReceipt struct {
	OK         bool   `json:"ok"`
	DecisionID string `json:"decision_id,omitempty"`
	EventID    string `json:"event_id,omitempty"`
	CreditID   string `json:"credit_id,omitempty"`
	Backend    string `json:"backend"`
	Replayed   bool   `json:"replayed"`
}

type decisionJournalQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func decisionEnvelope(canonical []byte) string {
	return `{"payload":` + string(canonical) + `,"payload_sha256":"` + sha256Hex(string(canonical)) + `"}`
}

func decodeDecisionEnvelope(encoded string, target any) error {
	var envelope struct {
		Payload       json.RawMessage `json:"payload"`
		PayloadSHA256 string          `json:"payload_sha256"`
	}
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil || len(envelope.Payload) == 0 {
		return errors.New("memory decision journal payload is invalid")
	}
	var payload map[string]any
	if err := decodeProspectiveJSON(string(envelope.Payload), &payload); err != nil || payload == nil {
		return errors.New("memory decision journal payload is invalid")
	}
	canonical, err := jsoncompat.MarshalPython(payload, false)
	if err != nil {
		return err
	}
	if envelope.PayloadSHA256 != sha256Hex(string(canonical)) {
		return errors.New("memory decision journal payload hash mismatch")
	}
	decoder := json.NewDecoder(bytes.NewReader(envelope.Payload))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("memory decision journal typed payload is invalid: %w", err)
	}
	return nil
}

func decodedDecision(encoded string) (memory.MemoryDecisionEvent, error) {
	var event memory.MemoryDecisionEvent
	if err := decodeDecisionEnvelope(encoded, &event); err != nil {
		return memory.MemoryDecisionEvent{}, err
	}
	return event.Normalize()
}

func decodedUsage(encoded string) (memory.MemoryUsageEvent, error) {
	var event memory.MemoryUsageEvent
	if err := decodeDecisionEnvelope(encoded, &event); err != nil {
		return memory.MemoryUsageEvent{}, err
	}
	return event.Normalize()
}

func journalRows(ctx context.Context, query decisionJournalQueryer, workspaceID, blockID, outcome string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT payload_json FROM usage WHERE block_id=? AND workspace_id=? AND outcome=?`,
		blockID, workspaceID, outcome)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return nil, err
		}
		result = append(result, encoded)
	}
	return result, rows.Err()
}

func getMemoryDecision(ctx context.Context, query decisionJournalQueryer, workspaceID, id string) (*memory.MemoryDecisionEvent, error) {
	rows, err := journalRows(ctx, query, workspaceID, "memory-decision-event:"+id, "memory_decision")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) != 1 {
		return nil, errors.New("memory decision journal linkage is invalid")
	}
	event, err := decodedDecision(rows[0])
	if err != nil {
		return nil, err
	}
	if event.DecisionID != id || event.WorkspaceID != workspaceID {
		return nil, errors.New("memory decision journal linkage is invalid")
	}
	return &event, nil
}

// GetMemoryDecision reads one hash-verified decision outside ordinary recall.
func (index *Index) GetMemoryDecision(ctx context.Context, id string) (*memory.MemoryDecisionEvent, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("decision_id is required")
	}
	return getMemoryDecision(ctx, index.db, index.workspaceID, id)
}

// RecordMemoryDecision writes one immutable decision snapshot. Replays check
// the exact Python-compatible envelope and typed payload.
func (writer *Writer) RecordMemoryDecision(ctx context.Context, event memory.MemoryDecisionEvent) (DecisionJournalReceipt, error) {
	if strings.TrimSpace(event.WorkspaceID) != writer.workspaceID {
		return DecisionJournalReceipt{}, errors.New("memory decision workspace mismatch")
	}
	connection, err := beginImmediate(ctx, writer.db)
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	defer rollbackImmediate(connection)
	rows, err := journalRows(ctx, connection, writer.workspaceID, "memory-decision-event:"+strings.TrimSpace(event.DecisionID), "memory_decision")
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	if event.CreatedAt == "" && len(rows) == 1 {
		prior, err := decodedDecision(rows[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		event.CreatedAt = prior.CreatedAt
	}
	event, err = event.Normalize()
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	payload, err := event.CanonicalJSON()
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	envelope := decisionEnvelope(payload)
	if len(rows) > 0 {
		if len(rows) != 1 || rows[0] != envelope {
			return DecisionJournalReceipt{}, errors.New("memory_decision_idempotency_conflict")
		}
		prior, err := decodedDecision(rows[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		if !reflect.DeepEqual(prior, event) {
			return DecisionJournalReceipt{}, errors.New("memory_decision_idempotency_conflict")
		}
		if err := commitImmediate(ctx, connection); err != nil {
			return DecisionJournalReceipt{}, err
		}
		return DecisionJournalReceipt{OK: true, DecisionID: event.DecisionID, Backend: writer.backendID, Replayed: true}, nil
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO usage(block_id,workspace_id,outcome,payload_json) VALUES(?,?,?,?)`,
		"memory-decision-event:"+event.DecisionID, event.WorkspaceID, "memory_decision", envelope); err != nil {
		return DecisionJournalReceipt{}, err
	}
	if err := commitImmediate(ctx, connection); err != nil {
		return DecisionJournalReceipt{}, err
	}
	return DecisionJournalReceipt{OK: true, DecisionID: event.DecisionID, Backend: writer.backendID}, nil
}

// RecordMemoryUsageEvent enforces candidate and action linkage, unique
// per-decision sequence, and non-contradictory applied observations.
func (writer *Writer) RecordMemoryUsageEvent(ctx context.Context, event memory.MemoryUsageEvent) (DecisionJournalReceipt, error) {
	if strings.TrimSpace(event.WorkspaceID) != writer.workspaceID {
		return DecisionJournalReceipt{}, errors.New("memory usage workspace mismatch")
	}
	connection, err := beginImmediate(ctx, writer.db)
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	defer rollbackImmediate(connection)
	decision, err := getMemoryDecision(ctx, connection, writer.workspaceID, strings.TrimSpace(event.DecisionID))
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	if decision == nil {
		return DecisionJournalReceipt{}, errors.New("memory_decision_missing")
	}
	if strings.TrimSpace(event.TaskSignature) != decision.TaskSignature || strings.TrimSpace(event.ActionTraceID) != decision.ActionTraceID {
		return DecisionJournalReceipt{}, errors.New("memory usage task signature linkage is invalid")
	}
	if !slices.Contains(decision.CandidateIDs, strings.TrimSpace(event.MemoryID)) {
		return DecisionJournalReceipt{}, errors.New("memory usage available candidate linkage is invalid")
	}
	if slices.Contains([]string{"selected", "rendered", "cited", "applied"}, strings.TrimSpace(event.UsageType)) &&
		!slices.Contains(decision.SelectedCandidateIDs, strings.TrimSpace(event.MemoryID)) {
		return DecisionJournalReceipt{}, errors.New("memory usage selected candidate linkage is invalid")
	}
	blockID := "memory-usage-event:" + strings.TrimSpace(event.EventID)
	outcome := "memory_usage:" + strings.TrimSpace(event.UsageType)
	rows, err := journalRows(ctx, connection, writer.workspaceID, blockID, outcome)
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	if event.CreatedAt == "" && len(rows) == 1 {
		prior, err := decodedUsage(rows[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		event.CreatedAt = prior.CreatedAt
	}
	event, err = event.Normalize()
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	payload, err := event.CanonicalJSON()
	if err != nil {
		return DecisionJournalReceipt{}, err
	}
	envelope := decisionEnvelope(payload)
	if len(rows) > 0 {
		if len(rows) != 1 || rows[0] != envelope {
			return DecisionJournalReceipt{}, errors.New("memory_usage_idempotency_conflict")
		}
		prior, err := decodedUsage(rows[0])
		if err != nil {
			return DecisionJournalReceipt{}, err
		}
		if !reflect.DeepEqual(prior, event) {
			return DecisionJournalReceipt{}, errors.New("memory_usage_idempotency_conflict")
		}
		if err := commitImmediate(ctx, connection); err != nil {
			return DecisionJournalReceipt{}, err
		}
		return DecisionJournalReceipt{OK: true, EventID: event.EventID, Backend: writer.backendID, Replayed: true}, nil
	}
	if event.UsageType == "applied" || event.UsageType == "not_applied" {
		opposite := "applied"
		if event.UsageType == "applied" {
			opposite = "not_applied"
		}
		var exists int
		err = connection.QueryRowContext(ctx, `SELECT 1 FROM usage WHERE workspace_id=? AND outcome=?
		 AND json_extract(payload_json,'$.payload.decision_id')=? AND json_extract(payload_json,'$.payload.memory_id')=? LIMIT 1`,
			writer.workspaceID, "memory_usage:"+opposite, event.DecisionID, event.MemoryID).Scan(&exists)
		if err != nil && err != sql.ErrNoRows {
			return DecisionJournalReceipt{}, err
		}
		if err == nil {
			return DecisionJournalReceipt{}, errors.New("memory usage contains contradictory action-use observations")
		}
	}
	var sequenceBlock string
	err = connection.QueryRowContext(ctx, `SELECT block_id FROM usage WHERE workspace_id=? AND outcome LIKE 'memory_usage:%'
	 AND json_extract(payload_json,'$.payload.decision_id')=? AND json_extract(payload_json,'$.payload.sequence')=? LIMIT 1`,
		writer.workspaceID, event.DecisionID, event.Sequence).Scan(&sequenceBlock)
	if err != nil && err != sql.ErrNoRows {
		return DecisionJournalReceipt{}, err
	}
	if err == nil {
		return DecisionJournalReceipt{}, errors.New("memory_usage_sequence_conflict")
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO usage(block_id,workspace_id,outcome,payload_json) VALUES(?,?,?,?)`,
		blockID, event.WorkspaceID, outcome, envelope); err != nil {
		return DecisionJournalReceipt{}, err
	}
	if err := commitImmediate(ctx, connection); err != nil {
		return DecisionJournalReceipt{}, err
	}
	return DecisionJournalReceipt{OK: true, EventID: event.EventID, Backend: writer.backendID}, nil
}

// MemoryDecisionTrace returns the ordered candidate-to-action observation
// history. Every envelope and cross-event identity is checked before return.
func (index *Index) MemoryDecisionTrace(ctx context.Context, id string) (memory.MemoryDecisionTrace, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return memory.MemoryDecisionTrace{}, errors.New("decision_id is required")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return memory.MemoryDecisionTrace{}, err
	}
	defer tx.Rollback()
	decision, err := getMemoryDecision(ctx, tx, index.workspaceID, id)
	if err != nil {
		return memory.MemoryDecisionTrace{}, err
	}
	if decision == nil {
		return memory.MemoryDecisionTrace{}, errors.New("memory_decision_missing")
	}
	rows, err := tx.QueryContext(ctx, `SELECT block_id,outcome,payload_json FROM usage WHERE workspace_id=?
	 AND outcome LIKE 'memory_usage:%' AND json_extract(payload_json,'$.payload.decision_id')=?
	 ORDER BY CAST(json_extract(payload_json,'$.payload.sequence') AS INTEGER),block_id`, index.workspaceID, id)
	if err != nil {
		return memory.MemoryDecisionTrace{}, err
	}
	trace := memory.MemoryDecisionTrace{Decision: *decision, UsageEvents: []memory.MemoryUsageEvent{}, Channel: "memory_decision_usage_trace"}
	for rows.Next() {
		var blockID, outcome, encoded string
		if err := rows.Scan(&blockID, &outcome, &encoded); err != nil {
			rows.Close()
			return memory.MemoryDecisionTrace{}, err
		}
		event, err := decodedUsage(encoded)
		if err != nil {
			rows.Close()
			return memory.MemoryDecisionTrace{}, err
		}
		if blockID != "memory-usage-event:"+event.EventID || outcome != "memory_usage:"+event.UsageType {
			rows.Close()
			return memory.MemoryDecisionTrace{}, errors.New("memory usage journal linkage is invalid")
		}
		trace.UsageEvents = append(trace.UsageEvents, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return memory.MemoryDecisionTrace{}, err
	}
	if err := rows.Close(); err != nil {
		return memory.MemoryDecisionTrace{}, err
	}
	if err := trace.Validate(); err != nil {
		return memory.MemoryDecisionTrace{}, err
	}
	if err := tx.Commit(); err != nil {
		return memory.MemoryDecisionTrace{}, err
	}
	return trace, nil
}
