package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type ProspectiveReceipt struct {
	OK           bool                         `json:"ok"`
	Obligation   memory.ProspectiveObligation `json:"obligation"`
	ObligationID string                       `json:"obligation_id"`
	Status       string                       `json:"status"`
	Version      int                          `json:"version"`
	ReceiptID    string                       `json:"receipt_id"`
	Replayed     bool                         `json:"replayed"`
}

type prospectiveState struct {
	Obligation memory.ProspectiveObligation
	Scope      string
	Status     string
	Version    int
	PayloadID  string
	SHA        string
	ReceiptID  string
}

type prospectiveQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func prospectiveNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00") }

func prospectiveReceipt(id, event string, version int, sha string) string {
	return sha256Hex(fmt.Sprintf("%s\x00%s\x00%d\x00%s", id, event, version, sha))
}

func prospectiveIdentity(value memory.ProspectiveObligation) (string, string, string, error) {
	encoded, err := value.CanonicalJSON()
	if err != nil {
		return "", "", "", err
	}
	payload := string(encoded)
	sha := sha256Hex("prospective_obligation\x00" + payload)
	return payload, "prospective_obligation:" + sha, sha, nil
}

func prospectiveLoad(ctx context.Context, query prospectiveQueryer, payloadID, sha string) (memory.ProspectiveObligation, error) {
	var actualSHA, kind, raw string
	if err := query.QueryRowContext(ctx, `SELECT payload_sha256,payload_kind,payload_json FROM canonical_payloads WHERE payload_id=?`, payloadID).
		Scan(&actualSHA, &kind, &raw); err != nil {
		return memory.ProspectiveObligation{}, err
	}
	if kind != "prospective_obligation" || actualSHA != sha || payloadID != kind+":"+sha || sha != sha256Hex(kind+"\x00"+raw) {
		return memory.ProspectiveObligation{}, errors.New("prospective payload linkage is invalid")
	}
	var obligation memory.ProspectiveObligation
	if err := decodeProspectiveJSON(raw, &obligation); err != nil {
		return memory.ProspectiveObligation{}, err
	}
	obligation, err := obligation.Normalize()
	if err != nil {
		return memory.ProspectiveObligation{}, err
	}
	canonical, err := obligation.CanonicalJSON()
	if err != nil {
		return memory.ProspectiveObligation{}, err
	}
	if string(canonical) != raw {
		return memory.ProspectiveObligation{}, errors.New("prospective payload is not canonical")
	}
	return obligation, nil
}

func prospectiveCurrent(ctx context.Context, query prospectiveQueryer, id string) (*prospectiveState, error) {
	state := &prospectiveState{}
	err := query.QueryRowContext(ctx, `SELECT scope,status,version,payload_id,payload_sha256,receipt_id FROM prospective_obligations WHERE obligation_id=?`, id).
		Scan(&state.Scope, &state.Status, &state.Version, &state.PayloadID, &state.SHA, &state.ReceiptID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.Obligation, err = prospectiveLoad(ctx, query, state.PayloadID, state.SHA)
	if err != nil {
		return nil, err
	}
	if state.Obligation.ObligationID != id || state.Obligation.Scope != state.Scope || state.Obligation.Status != state.Status || state.Version < 0 {
		return nil, errors.New("prospective current state linkage is invalid")
	}
	var eventID, status, payloadID, sha, receipt string
	var expected int
	err = query.QueryRowContext(ctx, `SELECT event_id,expected_version,to_status,payload_id,payload_sha256,receipt_id
	 FROM prospective_obligation_events WHERE obligation_id=? ORDER BY expected_version DESC LIMIT 1`, id).
		Scan(&eventID, &expected, &status, &payloadID, &sha, &receipt)
	if err != nil || state.Version != expected+1 || state.Status != status || state.PayloadID != payloadID || state.SHA != sha ||
		state.ReceiptID != receipt || receipt != prospectiveReceipt(id, eventID, state.Version, sha) {
		return nil, errors.New("prospective event linkage is invalid")
	}
	return state, nil
}

func prospectiveEvents(ctx context.Context, query prospectiveQueryer, id, eventID, key string) ([]prospectiveEventRow, error) {
	rows, err := query.QueryContext(ctx, `SELECT event_id,idempotency_key,expected_version,from_status,to_status,payload_id,payload_sha256,receipt_id
	 FROM prospective_obligation_events WHERE obligation_id=? AND (event_id=? OR idempotency_key=?)`, id, eventID, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []prospectiveEventRow{}
	for rows.Next() {
		var row prospectiveEventRow
		if err := rows.Scan(&row.EventID, &row.Key, &row.ExpectedVersion, &row.FromStatus, &row.ToStatus,
			&row.PayloadID, &row.SHA, &row.ReceiptID); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type prospectiveEventRow struct {
	EventID, Key, FromStatus, ToStatus, PayloadID, SHA, ReceiptID string
	ExpectedVersion                                               int
}

func prospectiveResult(value memory.ProspectiveObligation, version int, receipt string, replayed bool) ProspectiveReceipt {
	return ProspectiveReceipt{true, value, value.ObligationID, value.Status, version, receipt, replayed}
}

func beginImmediate(ctx context.Context, db *sql.DB) (*sql.Conn, error) {
	connection, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func commitImmediate(ctx context.Context, connection *sql.Conn) error {
	_, err := connection.ExecContext(ctx, "COMMIT")
	return err
}

func rollbackImmediate(connection *sql.Conn) {
	_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
	_ = connection.Close()
}

func prospectiveStorePayload(ctx context.Context, connection *sql.Conn, payloadID, sha, raw string) error {
	_, err := connection.ExecContext(ctx, `INSERT OR IGNORE INTO canonical_payloads(payload_id,payload_sha256,payload_kind,payload_json,created_at)
	 VALUES(?,?,?,?,?)`, payloadID, sha, "prospective_obligation", raw, prospectiveNow())
	if err != nil {
		return err
	}
	var actualSHA, kind, actual string
	if err := connection.QueryRowContext(ctx, `SELECT payload_sha256,payload_kind,payload_json FROM canonical_payloads WHERE payload_id=?`, payloadID).
		Scan(&actualSHA, &kind, &actual); err != nil {
		return err
	}
	if actualSHA != sha || kind != "prospective_obligation" || actual != raw {
		return errors.New("prospective payload identity conflict")
	}
	return nil
}

func prospectiveNode(value memory.ProspectiveObligation, workspaceID string, version int, createdAt, updatedAt string) memory.IndexNode {
	fields := map[string]bool{}
	for _, group := range [][]memory.ProspectivePredicate{value.TriggerPredicates, value.SuppressPredicates, value.CancelPredicates} {
		for _, predicate := range group {
			fields[predicate.PredicateType+":"+predicate.Field] = true
		}
	}
	ordered := make([]string, 0, len(fields))
	for field := range fields {
		ordered = append(ordered, field)
	}
	sort.Strings(ordered)
	tags := []string{"prospective_obligation", value.Status}
	if len(value.SemanticTriggerCues) > 0 {
		tags = append(tags, "semantic_cue")
	}
	tags = append(tags, ordered...)
	refs := make([]string, 0, len(value.EvidenceRefs))
	for _, ref := range value.EvidenceRefs {
		refs = append(refs, ref.RefID)
	}
	return memory.IndexNode{
		NodeID:   prospectiveNodeID(workspaceID, value.ObligationID),
		Title:    "Prospective trigger: " + value.ObligationID,
		NodeType: "prospective_trigger", WorkspaceID: workspaceID, Tags: tags,
		Metadata: map[string]any{
			"obligation_id": value.ObligationID, "scope": value.Scope, "scope_id": value.ScopeID,
			"status": value.Status, "version": version, "trigger_predicates": value.TriggerPredicates,
			"semantic_trigger_cues": value.SemanticTriggerCues, "earliest_at": value.EarliestAt,
			"deadline_at": value.DeadlineAt, "expires_at": value.ExpiresAt,
			"recurrence_policy":           value.RecurrencePolicy,
			"recurrence_interval_seconds": value.RecurrenceIntervalSeconds,
			"suppress_predicates":         value.SuppressPredicates, "cancel_predicates": value.CancelPredicates,
			"source_evidence_ref_ids": refs, "host_action_authorized": false,
		}, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
}

func prospectiveSyncNode(ctx context.Context, connection *sql.Conn, value memory.ProspectiveObligation, workspaceID string, version int) error {
	id := prospectiveNodeID(workspaceID, value.ObligationID)
	now := prospectiveNow()
	created := now
	var old string
	err := connection.QueryRowContext(ctx, `SELECT payload_json FROM index_nodes WHERE node_id=?`, id).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		var prior memory.IndexNode
		if err := decodeProspectiveJSON(old, &prior); err != nil {
			return err
		}
		if prior.NodeID != id || prior.WorkspaceID != workspaceID || prior.NodeType != "prospective_trigger" || prior.CreatedAt == "" {
			return errors.New("prospective index node identity is invalid")
		}
		created = prior.CreatedAt
	}
	node := prospectiveNode(value, workspaceID, version, created, now)
	encoded, err := jsoncompat.MarshalPythonDefault(node, false)
	if err != nil {
		return err
	}
	tags, err := jsoncompat.MarshalPythonDefault(node.Tags, false)
	if err != nil {
		return err
	}
	_, err = connection.ExecContext(ctx, `INSERT OR REPLACE INTO index_nodes(node_id,workspace_id,node_type,title,tags_json,payload_json,updated_at)
	 VALUES(?,?,?,?,?,?,?)`, node.NodeID, node.WorkspaceID, node.NodeType, node.Title, string(tags), string(encoded), node.UpdatedAt)
	return err
}

// GetProspectiveObligation reads one obligation and checks its payload and latest event.
func (index *Index) GetProspectiveObligation(ctx context.Context, id string) (*memory.ProspectiveObligation, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("prospective obligation_id is required")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	state, err := prospectiveCurrent(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}
	return &state.Obligation, nil
}

// ProspectiveTelemetryObligations reads full, version-checked obligations for
// the operator telemetry channel. It does not expose them through recall.
func (index *Index) ProspectiveTelemetryObligations(ctx context.Context, limit int) ([]memory.ProspectiveObligation, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("prospective telemetry limit must be 1 through 256")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM index_nodes WHERE workspace_id=? AND node_type='prospective_trigger'
	 ORDER BY updated_at DESC,node_id ASC LIMIT ?`, index.workspaceID, limit)
	if err != nil {
		return nil, err
	}
	nodes := []string{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		nodes = append(nodes, raw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	values := make([]memory.ProspectiveObligation, 0, len(nodes))
	for _, raw := range nodes {
		candidate, err := index.prospectiveNode(ctx, tx, raw)
		if err != nil {
			return nil, err
		}
		state, err := prospectiveCurrent(ctx, tx, candidate.ID)
		if err != nil {
			return nil, err
		}
		if state == nil || state.Status != candidate.Status {
			return nil, errors.New("prospective telemetry obligation state is invalid")
		}
		values = append(values, state.Obligation)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

// CreateProspectiveObligation creates a pending rule and its exact trigger index atomically.
func (writer *Writer) CreateProspectiveObligation(ctx context.Context, value memory.ProspectiveObligation, eventID, idempotencyKey string) (ProspectiveReceipt, error) {
	value, err := value.Normalize()
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	if value.Status != "pending" {
		return ProspectiveReceipt{}, errors.New("prospective creation requires pending state")
	}
	eventID, idempotencyKey = strings.TrimSpace(eventID), strings.TrimSpace(idempotencyKey)
	if eventID == "" {
		return ProspectiveReceipt{}, errors.New("prospective event_id is required")
	}
	if idempotencyKey == "" {
		idempotencyKey = eventID
	}
	raw, payloadID, sha, err := prospectiveIdentity(value)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	receipt := prospectiveReceipt(value.ObligationID, eventID, 0, sha)
	connection, err := beginImmediate(ctx, writer.db)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	defer rollbackImmediate(connection)
	events, err := prospectiveEvents(ctx, connection, value.ObligationID, eventID, idempotencyKey)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	if len(events) > 0 {
		if len(events) != 1 {
			return ProspectiveReceipt{}, errors.New("prospective_obligation_idempotency_conflict")
		}
		event := events[0]
		if event.EventID != eventID || event.Key != idempotencyKey || event.ExpectedVersion != -1 ||
			event.FromStatus != "" || event.ToStatus != "pending" || event.PayloadID != payloadID || event.SHA != sha || event.ReceiptID != receipt {
			return ProspectiveReceipt{}, errors.New("prospective_obligation_idempotency_conflict")
		}
		state, err := prospectiveCurrent(ctx, connection, value.ObligationID)
		if err != nil {
			return ProspectiveReceipt{}, err
		}
		if state == nil || state.Version != 0 || state.PayloadID != payloadID || state.ReceiptID != receipt || !reflect.DeepEqual(state.Obligation, value) {
			return ProspectiveReceipt{}, errors.New("prospective_obligation_state_mismatch")
		}
		if err := prospectiveSyncNode(ctx, connection, value, writer.workspaceID, 0); err != nil {
			return ProspectiveReceipt{}, err
		}
		if err := commitImmediate(ctx, connection); err != nil {
			return ProspectiveReceipt{}, err
		}
		return prospectiveResult(value, 0, receipt, true), nil
	}
	state, err := prospectiveCurrent(ctx, connection, value.ObligationID)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	if state != nil {
		return ProspectiveReceipt{}, errors.New("prospective_obligation_exists")
	}
	if err := prospectiveStorePayload(ctx, connection, payloadID, sha, raw); err != nil {
		return ProspectiveReceipt{}, err
	}
	now := prospectiveNow()
	if _, err := connection.ExecContext(ctx, `INSERT INTO prospective_obligations(obligation_id,scope,status,version,payload_id,payload_sha256,receipt_id,updated_at)
	 VALUES(?,?,?,?,?,?,?,?)`, value.ObligationID, value.Scope, "pending", 0, payloadID, sha, receipt, now); err != nil {
		return ProspectiveReceipt{}, err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO prospective_obligation_events(obligation_id,event_id,idempotency_key,expected_version,from_status,to_status,payload_id,payload_sha256,receipt_id,created_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?)`, value.ObligationID, eventID, idempotencyKey, -1, "", "pending", payloadID, sha, receipt, now); err != nil {
		return ProspectiveReceipt{}, err
	}
	if err := prospectiveSyncNode(ctx, connection, value, writer.workspaceID, 0); err != nil {
		return ProspectiveReceipt{}, err
	}
	if err := commitImmediate(ctx, connection); err != nil {
		return ProspectiveReceipt{}, err
	}
	return prospectiveResult(value, 0, receipt, false), nil
}

// TransitionProspectiveObligation uses the event's expected state as a CAS fence.
func (writer *Writer) TransitionProspectiveObligation(ctx context.Context, id string, event memory.ProspectiveLifecycleEvent) (ProspectiveReceipt, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProspectiveReceipt{}, errors.New("prospective obligation_id is required")
	}
	event, err := event.Normalize()
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	target := event.TargetStatus()
	connection, err := beginImmediate(ctx, writer.db)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	defer rollbackImmediate(connection)
	events, err := prospectiveEvents(ctx, connection, id, event.EventID, event.IdempotencyKey)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	if len(events) > 0 {
		if len(events) != 1 {
			return ProspectiveReceipt{}, errors.New("prospective_obligation_idempotency_conflict")
		}
		prior := events[0]
		replayed, err := prospectiveLoad(ctx, connection, prior.PayloadID, prior.SHA)
		if err != nil {
			return ProspectiveReceipt{}, err
		}
		if prior.EventID != event.EventID || prior.Key != event.IdempotencyKey || prior.ExpectedVersion != event.ExpectedVersion ||
			prior.FromStatus != event.ExpectedStatus || prior.ToStatus != target || replayed.ObligationID != id ||
			replayed.TransitionFrom == nil || *replayed.TransitionFrom != event.ExpectedStatus || replayed.Status != target ||
			!reflect.DeepEqual(replayed.TransitionEvidenceRefs, event.EvidenceRefs) ||
			!reflect.DeepEqual(replayed.SatisfactionEvidenceRefs, event.SatisfactionEvidenceRefs) ||
			(event.SatisfactionVerifier != "" && replayed.SatisfactionVerifier != event.SatisfactionVerifier) ||
			replayed.SatisfactionVerifierResult != event.SatisfactionVerifierResult ||
			prior.ReceiptID != prospectiveReceipt(id, event.EventID, event.ExpectedVersion+1, prior.SHA) {
			return ProspectiveReceipt{}, errors.New("prospective_obligation_idempotency_conflict")
		}
		state, err := prospectiveCurrent(ctx, connection, id)
		if err != nil {
			return ProspectiveReceipt{}, err
		}
		replayVersion := event.ExpectedVersion + 1
		if state == nil || state.Version < replayVersion {
			return ProspectiveReceipt{}, errors.New("prospective_obligation_state_mismatch")
		}
		if state.Version == replayVersion {
			if state.Status != target || state.PayloadID != prior.PayloadID || state.SHA != prior.SHA || state.ReceiptID != prior.ReceiptID {
				return ProspectiveReceipt{}, errors.New("prospective_obligation_state_mismatch")
			}
			if err := prospectiveSyncNode(ctx, connection, replayed, writer.workspaceID, replayVersion); err != nil {
				return ProspectiveReceipt{}, err
			}
		} else if err := prospectiveCheckContinuation(ctx, connection, id, replayVersion, state.Version, target, state.Status); err != nil {
			return ProspectiveReceipt{}, err
		}
		if err := commitImmediate(ctx, connection); err != nil {
			return ProspectiveReceipt{}, err
		}
		return prospectiveResult(replayed, replayVersion, prior.ReceiptID, true), nil
	}
	state, err := prospectiveCurrent(ctx, connection, id)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	if state == nil {
		return ProspectiveReceipt{}, errors.New("prospective_obligation_missing")
	}
	if state.Version != event.ExpectedVersion || state.Status != event.ExpectedStatus {
		return ProspectiveReceipt{}, errors.New("prospective_obligation_stale")
	}
	successor, err := state.Obligation.TransitionTo(target, event.EvidenceRefs, event.SatisfactionEvidenceRefs,
		event.SatisfactionVerifier, event.SatisfactionVerifierResult)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	raw, payloadID, sha, err := prospectiveIdentity(successor)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	receipt := prospectiveReceipt(id, event.EventID, state.Version+1, sha)
	if err := prospectiveStorePayload(ctx, connection, payloadID, sha, raw); err != nil {
		return ProspectiveReceipt{}, err
	}
	now := prospectiveNow()
	result, err := connection.ExecContext(ctx, `UPDATE prospective_obligations SET status=?,version=?,payload_id=?,payload_sha256=?,receipt_id=?,updated_at=?
	 WHERE obligation_id=? AND version=? AND status=?`, target, state.Version+1, payloadID, sha, receipt, now, id, state.Version, state.Status)
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ProspectiveReceipt{}, err
	}
	if changed != 1 {
		return ProspectiveReceipt{}, errors.New("prospective_obligation_stale")
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO prospective_obligation_events(obligation_id,event_id,idempotency_key,expected_version,from_status,to_status,payload_id,payload_sha256,receipt_id,created_at)
	 VALUES(?,?,?,?,?,?,?,?,?,?)`, id, event.EventID, event.IdempotencyKey, state.Version, state.Status, target, payloadID, sha, receipt, now); err != nil {
		return ProspectiveReceipt{}, err
	}
	if err := prospectiveSyncNode(ctx, connection, successor, writer.workspaceID, state.Version+1); err != nil {
		return ProspectiveReceipt{}, err
	}
	if err := commitImmediate(ctx, connection); err != nil {
		return ProspectiveReceipt{}, err
	}
	return prospectiveResult(successor, state.Version+1, receipt, false), nil
}

func prospectiveCheckContinuation(ctx context.Context, connection *sql.Conn, id string, from, current int, status, expected string) error {
	rows, err := connection.QueryContext(ctx, `SELECT expected_version,from_status,to_status FROM prospective_obligation_events
	 WHERE obligation_id=? AND expected_version>=? AND expected_version<? ORDER BY expected_version`, id, from, current)
	if err != nil {
		return err
	}
	defer rows.Close()
	version := from
	for rows.Next() {
		var next int
		var before, after string
		if err := rows.Scan(&next, &before, &after); err != nil {
			return err
		}
		if next != version || before != status {
			return errors.New("prospective_obligation_state_mismatch")
		}
		version++
		status = after
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if version != current || status != expected {
		return errors.New("prospective_obligation_state_mismatch")
	}
	return nil
}
