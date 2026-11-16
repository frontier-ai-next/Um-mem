package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// PendingTopologyEvolution is the content-free public view of a prepared
// split or merge. The exact mapping remains in the private catalog journal.
type PendingTopologyEvolution struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Operation   string `json:"operation"`
}

type TopologyEvolution struct {
	OperationID string         `json:"operation_id"`
	State       string         `json:"state"`
	Payload     map[string]any `json:"payload"`
	AbortReason string         `json:"abort_reason"`
}

func topologyEvolutionPayload(raw string, checksum string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return nil, errors.New("topology evolution journal payload is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("topology evolution journal payload is invalid")
	}
	actual, err := fingerprint(payload)
	if err != nil || actual != checksum {
		return nil, errors.New("topology evolution journal payload is invalid")
	}
	return payload, nil
}

func (store *Store) topologyEvolutionConn(ctx context.Context, conn *sql.Conn,
	operationID string) (*TopologyEvolution, error) {
	var state, raw, checksum, abortReason string
	err := conn.QueryRowContext(ctx, `SELECT state,payload_json,payload_sha256,abort_reason
		FROM topology_evolutions WHERE operation_id=? AND workspace_id=? AND profile_id=?`,
		operationID, store.workspaceID, store.profileID).Scan(&state, &raw, &checksum, &abortReason)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	payload, err := topologyEvolutionPayload(raw, checksum)
	if err != nil {
		return nil, err
	}
	return &TopologyEvolution{OperationID: operationID, State: state, Payload: payload,
		AbortReason: abortReason}, nil
}

func (store *Store) TopologyEvolution(ctx context.Context,
	operationID string) (*TopologyEvolution, error) {
	if operationID == "" || operationID != strings.TrimSpace(operationID) {
		return nil, errors.New("topology evolution requires an exact operation_id")
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return store.topologyEvolutionConn(ctx, conn, operationID)
}

// PrepareTopologyEvolution durably reserves source leaves against competing
// staged generations. Retrying an ID requires the exact Python payload hash.
func (store *Store) PrepareTopologyEvolution(ctx context.Context,
	operationID string, payload map[string]any) (TopologyEvolution, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" || len(operationID) > 256 || payload == nil {
		return TopologyEvolution{}, errors.New("topology evolution operation is invalid")
	}
	operation, ok := payload["operation"].(string)
	if !ok || (operation != "split" && operation != "merge") {
		return TopologyEvolution{}, errors.New("topology evolution operation is invalid")
	}
	sources, sourcesOK := payload["sources"].(map[string]any)
	_, targetsOK := payload["targets"].([]any)
	_, assignmentsOK := payload["assignments"].(map[string]any)
	if !sourcesOK || !targetsOK || !assignmentsOK {
		return TopologyEvolution{}, errors.New("topology evolution mapping is invalid")
	}
	raw, err := canonical(payload)
	if err != nil {
		return TopologyEvolution{}, err
	}
	checksum, err := fingerprint(payload)
	if err != nil {
		return TopologyEvolution{}, err
	}
	storedPayload, err := topologyEvolutionPayload(raw, checksum)
	if err != nil {
		return TopologyEvolution{}, err
	}
	var result TopologyEvolution
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		existing, err := store.topologyEvolutionConn(ctx, conn, operationID)
		if err != nil {
			return err
		}
		if existing != nil {
			storedChecksum, err := fingerprint(existing.Payload)
			if err != nil || storedChecksum != checksum {
				return errors.New("operation_id already binds a different topology evolution")
			}
			result = *existing
			return nil
		}
		rows, err := conn.QueryContext(ctx, `SELECT payload_json,payload_sha256
			FROM topology_evolutions WHERE workspace_id=? AND profile_id=?
			AND state IN ('prepared','projections_ready')`, store.workspaceID, store.profileID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pendingRaw, pendingChecksum string
			if err := rows.Scan(&pendingRaw, &pendingChecksum); err != nil {
				rows.Close()
				return err
			}
			pending, err := topologyEvolutionPayload(pendingRaw, pendingChecksum)
			if err != nil {
				rows.Close()
				return err
			}
			pendingSources, ok := pending["sources"].(map[string]any)
			if !ok {
				rows.Close()
				return errors.New("pending topology evolution sources are invalid")
			}
			for sourceID := range sources {
				if _, conflicts := pendingSources[sourceID]; conflicts {
					rows.Close()
					return errors.New("source block already has a pending topology evolution")
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		stamp := now()
		if _, err := conn.ExecContext(ctx, `INSERT INTO topology_evolutions(
			operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,
			created_at,updated_at) VALUES(?,?,?,'prepared',?,?,?,?)`,
			operationID, store.workspaceID, store.profileID, raw, checksum,
			stamp, stamp); err != nil {
			return err
		}
		result = TopologyEvolution{OperationID: operationID, State: "prepared",
			Payload: storedPayload}
		return nil
	})
	return result, err
}

// MarkTopologyProjectionsReady records that every native target projection has
// been completed. It does not expose the target generation to readers.
func (store *Store) MarkTopologyProjectionsReady(ctx context.Context, operationID string) error {
	if operationID == "" || operationID != strings.TrimSpace(operationID) {
		return errors.New("topology evolution requires an exact operation_id")
	}
	return immediate(ctx, store.db, func(conn *sql.Conn) error {
		evolution, err := store.topologyEvolutionConn(ctx, conn, operationID)
		if err != nil {
			return err
		}
		if evolution == nil {
			return errors.New("topology evolution operation was not found")
		}
		if evolution.State != "prepared" {
			return nil
		}
		result, err := conn.ExecContext(ctx, `UPDATE topology_evolutions
			SET state='projections_ready',updated_at=? WHERE operation_id=?
			AND workspace_id=? AND profile_id=? AND state='prepared'`,
			now(), operationID, store.workspaceID, store.profileID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("topology evolution changed before projections were ready")
		}
		return nil
	})
}

// PendingTopologyEvolutions reads Python's version 2 catalog journal without
// exposing mapping content or treating a staged generation as published.
func (store *Store) PendingTopologyEvolutions(ctx context.Context) ([]PendingTopologyEvolution, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT operation_id,state,payload_json,payload_sha256
		FROM topology_evolutions WHERE workspace_id=? AND profile_id=?
		AND state IN ('prepared','projections_ready') ORDER BY created_at,operation_id`,
		store.workspaceID, store.profileID)
	if err != nil {
		return nil, err
	}
	result := make([]PendingTopologyEvolution, 0)
	for rows.Next() {
		var operationID, state, raw, checksum string
		if err := rows.Scan(&operationID, &state, &raw, &checksum); err != nil {
			rows.Close()
			return nil, err
		}
		payload, err := topologyEvolutionPayload(raw, checksum)
		if err != nil {
			rows.Close()
			return nil, err
		}
		operation, ok := payload["operation"].(string)
		if !ok || (operation != "split" && operation != "merge") || operationID == "" {
			rows.Close()
			return nil, errors.New("topology evolution journal operation is invalid")
		}
		result = append(result, PendingTopologyEvolution{OperationID: operationID,
			State: state, Operation: operation})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

type AbortedTopologyEvolution struct {
	OperationID string `json:"operation_id"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}

// AbortTopologyEvolution keeps the immutable mapping journal while releasing
// an unpublished source claim. The state check and update share one immediate
// catalog transaction, including retries of an already aborted operation.
func (store *Store) AbortTopologyEvolution(ctx context.Context,
	operationID, reason string) (AbortedTopologyEvolution, error) {
	operationID = strings.TrimSpace(operationID)
	reason = strings.TrimSpace(reason)
	if operationID == "" || reason == "" {
		return AbortedTopologyEvolution{}, errors.New("operation_id and reason are required")
	}
	var result AbortedTopologyEvolution
	err := immediate(ctx, store.db, func(conn *sql.Conn) error {
		var state, raw, checksum, abortReason string
		err := conn.QueryRowContext(ctx, `SELECT state,payload_json,payload_sha256,abort_reason
			FROM topology_evolutions WHERE operation_id=? AND workspace_id=? AND profile_id=?`,
			operationID, store.workspaceID, store.profileID).Scan(&state, &raw, &checksum, &abortReason)
		if err == sql.ErrNoRows {
			return errors.New("topology evolution operation was not found")
		}
		if err != nil {
			return err
		}
		payload, err := topologyEvolutionPayload(raw, checksum)
		if err != nil {
			return err
		}
		operation, ok := payload["operation"].(string)
		if !ok || (operation != "split" && operation != "merge") {
			return errors.New("topology evolution journal operation is invalid")
		}
		if state == "published" {
			return errors.New("published topology evolution cannot be aborted")
		}
		if state == "aborted" {
			result = AbortedTopologyEvolution{OperationID: operationID, State: state,
				Reason: abortReason}
			return nil
		}
		if state != "prepared" && state != "projections_ready" {
			return errors.New("topology evolution state is invalid")
		}
		updated, err := conn.ExecContext(ctx, `UPDATE topology_evolutions
			SET state='aborted',abort_reason=?,updated_at=? WHERE operation_id=?
			AND workspace_id=? AND profile_id=? AND state IN ('prepared','projections_ready')`,
			reason, now(), operationID, store.workspaceID, store.profileID)
		if err != nil {
			return err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("topology evolution changed before abort")
		}
		result = AbortedTopologyEvolution{OperationID: operationID, State: "aborted",
			Reason: reason}
		return nil
	})
	return result, err
}
