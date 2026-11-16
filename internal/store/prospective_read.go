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
	"io"
	"strconv"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/prospective"
)

type prospectivePayload struct {
	ObligationID         string                 `json:"obligation_id"`
	Scope                string                 `json:"scope"`
	ScopeID              string                 `json:"scope_id"`
	Status               string                 `json:"status"`
	RequiredIntervention string                 `json:"required_intervention"`
	EvidenceRefs         []json.RawMessage      `json:"evidence_refs"`
	TriggerPredicates    []prospectivePredicate `json:"trigger_predicates"`
	SuppressPredicates   []prospectivePredicate `json:"suppress_predicates"`
	CancelPredicates     []prospectivePredicate `json:"cancel_predicates"`
	SemanticCues         []string               `json:"semantic_trigger_cues"`
	EarliestAt           string                 `json:"earliest_at"`
	DeadlineAt           string                 `json:"deadline_at"`
	ExpiresAt            string                 `json:"expires_at"`
	TransitionFrom       *string                `json:"transition_from"`
	TransitionRefs       []json.RawMessage      `json:"transition_evidence_refs"`
}

type prospectivePredicate struct {
	Type     string `json:"predicate_type"`
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

func decodeProspectiveJSON(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("prospective JSON has trailing content")
	}
	return nil
}

func digestProspective(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func prospectiveNodeID(workspaceID, obligationID string) string {
	return "prospective_trigger:" + digestProspective(workspaceID+"\x00"+obligationID)
}

func predicateList(values []prospectivePredicate) ([]prospective.Predicate, error) {
	result := make([]prospective.Predicate, 0, len(values))
	for _, value := range values {
		if !map[string]bool{"time": true, "deadline": true, "host_event": true,
			"tool_state": true, "test_state": true, "file_state": true,
			"registered_condition": true}[value.Type] || strings.TrimSpace(value.Field) == "" ||
			!map[string]bool{"eq": true, "neq": true, "contains": true, "prefix": true,
				"gte": true, "lte": true, "exists": true}[value.Operator] {
			return nil, errors.New("prospective predicate is invalid")
		}
		switch value.Value.(type) {
		case string, bool:
		case json.Number:
			if strings.ContainsAny(value.Value.(json.Number).String(), ".eE") {
				return nil, errors.New("prospective predicate value is invalid")
			}
		default:
			return nil, errors.New("prospective predicate value is invalid")
		}
		if text, ok := value.Value.(string); ok && value.Operator != "exists" && text == "" {
			return nil, errors.New("prospective predicate value is required")
		}
		result = append(result, prospective.Predicate{Type: value.Type, Field: value.Field,
			Operator: value.Operator, Value: value.Value})
	}
	return result, nil
}

func obligationFromPayload(raw string) (prospective.Obligation, error) {
	var payload prospectivePayload
	if err := decodeProspectiveJSON(raw, &payload); err != nil {
		return prospective.Obligation{}, err
	}
	if strings.TrimSpace(payload.ObligationID) == "" || len(payload.EvidenceRefs) == 0 ||
		!map[string]bool{"workspace": true, "run": true, "task": true, "subtask": true}[payload.Scope] ||
		!map[string]bool{"pending": true, "armed": true, "fired": true, "satisfied": true,
			"cancelled": true, "expired": true}[payload.Status] ||
		(payload.Scope == "workspace" && payload.ScopeID != "") ||
		(payload.Scope != "workspace" && payload.ScopeID == "") {
		return prospective.Obligation{}, errors.New("prospective obligation payload is invalid")
	}
	if payload.Status != "pending" && (payload.TransitionFrom == nil || len(payload.TransitionRefs) == 0) {
		return prospective.Obligation{}, errors.New("prospective obligation transition is invalid")
	}
	if payload.Status == "pending" && (payload.TransitionFrom != nil || len(payload.TransitionRefs) != 0) {
		return prospective.Obligation{}, errors.New("pending prospective obligation has a transition")
	}
	trigger, err := predicateList(payload.TriggerPredicates)
	if err != nil {
		return prospective.Obligation{}, err
	}
	suppress, err := predicateList(payload.SuppressPredicates)
	if err != nil {
		return prospective.Obligation{}, err
	}
	cancel, err := predicateList(payload.CancelPredicates)
	if err != nil {
		return prospective.Obligation{}, err
	}
	if (len(trigger) > 0 || len(payload.SemanticCues) > 0) &&
		strings.TrimSpace(payload.RequiredIntervention) == "" {
		return prospective.Obligation{}, errors.New("prospective intervention is missing")
	}
	return prospective.Obligation{ID: payload.ObligationID, Scope: payload.Scope,
		ScopeID: payload.ScopeID, Status: payload.Status, Trigger: trigger,
		Suppress: suppress, Cancel: cancel, SemanticCues: payload.SemanticCues,
		EarliestAt: payload.EarliestAt, DeadlineAt: payload.DeadlineAt,
		ExpiresAt: payload.ExpiresAt}, nil
}

func (index *Index) prospectiveNode(ctx context.Context, tx *sql.Tx, raw string) (prospective.Obligation, error) {
	var node struct {
		NodeID      string         `json:"node_id"`
		WorkspaceID string         `json:"workspace_id"`
		NodeType    string         `json:"node_type"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := decodeProspectiveJSON(raw, &node); err != nil {
		return prospective.Obligation{}, err
	}
	metadata := node.Metadata
	id, idOK := metadata["obligation_id"].(string)
	status, statusOK := metadata["status"].(string)
	versionRaw, versionOK := metadata["version"].(json.Number)
	authorized, authorizedOK := metadata["host_action_authorized"].(bool)
	if node.WorkspaceID != index.workspaceID || node.NodeType != "prospective_trigger" ||
		!idOK || id == "" || node.NodeID != prospectiveNodeID(index.workspaceID, id) ||
		!statusOK || !versionOK || !authorizedOK || authorized {
		return prospective.Obligation{}, errors.New("prospective trigger index linkage is invalid")
	}
	version, err := strconv.Atoi(versionRaw.String())
	if err != nil || version < 0 {
		return prospective.Obligation{}, errors.New("prospective trigger index version is invalid")
	}
	var scope, storedStatus, payloadID, payloadSHA, receiptID string
	var storedVersion int
	err = tx.QueryRowContext(ctx, `SELECT scope,status,version,payload_id,payload_sha256,receipt_id
	 FROM prospective_obligations WHERE obligation_id=?`, id).Scan(&scope, &storedStatus,
		&storedVersion, &payloadID, &payloadSHA, &receiptID)
	if err != nil || storedVersion != version || storedStatus != status {
		return prospective.Obligation{}, errors.New("prospective trigger index state is invalid")
	}
	var actualSHA, kind, payloadJSON string
	err = tx.QueryRowContext(ctx, `SELECT payload_sha256,payload_kind,payload_json FROM canonical_payloads
	 WHERE payload_id=?`, payloadID).Scan(&actualSHA, &kind, &payloadJSON)
	if err != nil || kind != "prospective_obligation" || payloadSHA != actualSHA ||
		payloadSHA != digestProspective(kind+"\x00"+payloadJSON) || payloadID != kind+":"+payloadSHA {
		return prospective.Obligation{}, errors.New("prospective obligation payload linkage is invalid")
	}
	obligation, err := obligationFromPayload(payloadJSON)
	if err != nil || obligation.ID != id || obligation.Scope != scope || obligation.Status != status {
		return prospective.Obligation{}, errors.New("prospective obligation current state linkage is invalid")
	}
	var eventID, eventStatus, eventPayloadID, eventPayloadSHA, eventReceiptID string
	var expectedVersion int
	err = tx.QueryRowContext(ctx, `SELECT event_id,expected_version,to_status,payload_id,payload_sha256,receipt_id
	 FROM prospective_obligation_events WHERE obligation_id=? ORDER BY expected_version DESC LIMIT 1`, id).Scan(
		&eventID, &expectedVersion, &eventStatus, &eventPayloadID, &eventPayloadSHA, &eventReceiptID)
	if err != nil || version != expectedVersion+1 || eventStatus != status ||
		eventPayloadID != payloadID || eventPayloadSHA != payloadSHA || eventReceiptID != receiptID ||
		receiptID != digestProspective(fmt.Sprintf("%s\x00%s\x00%d\x00%s", id, eventID, version, payloadSHA)) {
		return prospective.Obligation{}, errors.New("prospective obligation event linkage is invalid")
	}
	return obligation, nil
}

// ProspectiveObligations reads a version-checked snapshot of workspace rules.
// It matches Python's index order and fails closed on any broken linkage.
func (index *Index) ProspectiveObligations(ctx context.Context, limit int) ([]prospective.Obligation, error) {
	if limit <= 0 || limit > 256 {
		return nil, errors.New("prospective candidate limit is invalid")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM index_nodes WHERE workspace_id=?
	 AND node_type='prospective_trigger' ORDER BY updated_at DESC,node_id ASC LIMIT ?`, index.workspaceID, limit)
	if err != nil {
		return nil, err
	}
	jsonRows := []string{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, err
		}
		jsonRows = append(jsonRows, raw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]prospective.Obligation, 0, len(jsonRows))
	for _, raw := range jsonRows {
		obligation, err := index.prospectiveNode(ctx, tx, raw)
		if err != nil {
			return nil, err
		}
		result = append(result, obligation)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}
