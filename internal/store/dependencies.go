package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type DependencyOptions struct {
	AsOf     string
	MaxNodes int
	MaxEdges int
}

type dependencyPending struct {
	id        string
	ancestors map[string]bool
}

func (index *Index) recordProfileTx(ctx context.Context, tx *sql.Tx, recordID string) (string, error) {
	var payload string
	err := tx.QueryRowContext(ctx, `SELECT payload_json FROM blocks WHERE block_id=? AND workspace_id=?`,
		recordID, index.workspaceID).Scan(&payload)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var identity struct {
		ProfileID string `json:"profile_id"`
	}
	if err := json.Unmarshal([]byte(payload), &identity); err != nil {
		return "", err
	}
	return identity.ProfileID, nil
}

func (index *Index) dependencyNodeExistsTx(ctx context.Context, tx *sql.Tx, nodeID string) (bool, error) {
	var existing int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM index_nodes WHERE node_id=? AND workspace_id=?`,
		nodeID, index.workspaceID).Scan(&existing)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (index *Index) derivedEdgesTx(ctx context.Context, tx *sql.Tx, sourceID string, limit int) ([]memory.EdgeRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT dst_id,payload_json FROM edges
		WHERE workspace_id=? AND src_id=? AND edge_type='derived_from' ORDER BY dst_id LIMIT ?`,
		index.workspaceID, sourceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]memory.EdgeRecord, 0)
	for rows.Next() {
		var targetID, payload string
		if err := rows.Scan(&targetID, &payload); err != nil {
			return nil, err
		}
		var edge memory.EdgeRecord
		if err := json.Unmarshal([]byte(payload), &edge); err != nil {
			return nil, err
		}
		if edge.SrcID != sourceID || edge.DstID != targetID || edge.WorkspaceID != index.workspaceID || edge.EdgeType != "derived_from" {
			return nil, errors.New("stored dependency edge identity differs from graph index")
		}
		result = append(result, edge)
	}
	return result, rows.Err()
}

// ValidateDependencies checks the exact derived_from graph at one event time.
// It returns a bounded validity receipt; it does not make evidence judgments.
func (index *Index) ValidateDependencies(ctx context.Context, recordID string, options DependencyOptions) (memory.DependencyValidation, error) {
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return memory.DependencyValidation{}, err
	}
	defer tx.Rollback()
	receipt, err := index.validateDependenciesTx(ctx, tx, recordID, options)
	if err != nil {
		return memory.DependencyValidation{}, err
	}
	return receipt, tx.Commit()
}

func (index *Index) validateDependenciesTx(ctx context.Context, tx *sql.Tx, recordID string, options DependencyOptions) (memory.DependencyValidation, error) {
	if recordID == "" {
		return memory.DependencyValidation{}, errors.New("dependency validation requires an exact record ID")
	}
	maxNodes, maxEdges := options.MaxNodes, options.MaxEdges
	if maxNodes == 0 {
		maxNodes = 256
	}
	if maxEdges == 0 {
		maxEdges = 1024
	}
	if maxNodes < 1 || maxNodes > 4096 || maxEdges < 1 || maxEdges > 16384 {
		return memory.DependencyValidation{}, errors.New("dependency traversal limits are outside allowed bounds")
	}
	instant := time.Now().UTC()
	if options.AsOf != "" {
		parsed, exists, err := parseTemporalTime(options.AsOf)
		if err != nil || !exists {
			return memory.DependencyValidation{}, fmt.Errorf("dependency as_of requires an ISO timestamp: %q", options.AsOf)
		}
		instant = parsed
	}
	receipt := memory.DependencyValidation{
		RecordID: recordID, WorkspaceID: index.workspaceID, ProfileID: index.profileID,
		AsOf: instant.Format(time.RFC3339Nano), Status: "valid",
		CheckedRecordIDs: []string{}, Issues: []memory.DependencyIssue{},
		MaxNodes: maxNodes, MaxEdges: maxEdges,
	}
	visited := make(map[string]bool)
	issueSeen := make(map[memory.DependencyIssue]bool)
	addIssue := func(id, code string) {
		issue := memory.DependencyIssue{RecordID: id, Code: code}
		if !issueSeen[issue] {
			issueSeen[issue] = true
			receipt.Issues = append(receipt.Issues, issue)
		}
	}
	pending := []dependencyPending{{id: recordID, ancestors: map[string]bool{}}}
	edgeCount := 0
	incomplete := false
	for len(pending) > 0 {
		item := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if item.ancestors[item.id] {
			addIssue(item.id, "dependency_cycle")
			incomplete = true
			continue
		}
		if visited[item.id] {
			continue
		}
		if len(visited) >= maxNodes {
			addIssue(recordID, "dependency_budget_exhausted")
			incomplete = true
			break
		}
		visited[item.id] = true
		source, err := index.readStoredRecordTx(ctx, tx, item.id)
		if err != nil {
			return memory.DependencyValidation{}, err
		}
		if source == nil {
			profile, err := index.recordProfileTx(ctx, tx, item.id)
			if err != nil {
				return memory.DependencyValidation{}, err
			}
			if profile != "" && profile != index.profileID {
				addIssue(item.id, "profile_mismatch")
				continue
			}
			if item.id != recordID {
				node, err := index.dependencyNodeExistsTx(ctx, tx, item.id)
				if err != nil {
					return memory.DependencyValidation{}, err
				}
				if node {
					continue
				}
			}
			addIssue(item.id, "record_unavailable")
			continue
		}
		receipt.CheckedRecordIDs = append(receipt.CheckedRecordIDs, item.id)
		if source.Status != "accepted" && source.Status != "candidate" {
			addIssue(item.id, "lifecycle_invalid")
		}
		if options.AsOf == "" && source.CanonicalKey != "" {
			current, err := index.isCurrentRecordTx(ctx, tx, *source)
			if err != nil {
				return memory.DependencyValidation{}, err
			}
			if !current {
				addIssue(item.id, "revision_not_current")
			}
		}
		temporalPlan := memory.NewRecallPlan("")
		temporalPlan.AsOf = receipt.AsOf
		temporalPlan.TemporalMode = "historical"
		_, matches, err := index.eventMatchesTx(ctx, tx, *source, temporalPlan)
		if err != nil {
			return memory.DependencyValidation{}, err
		}
		if !matches {
			addIssue(item.id, "outside_validity")
		}
		edges, err := index.derivedEdgesTx(ctx, tx, item.id, maxEdges-edgeCount+1)
		if err != nil {
			return memory.DependencyValidation{}, err
		}
		edgeCount += len(edges)
		if edgeCount > maxEdges {
			addIssue(recordID, "dependency_budget_exhausted")
			incomplete = true
			break
		}
		for _, edge := range edges {
			createdAt, hasCreated, err := parseTemporalTime(edge.CreatedAt)
			if err != nil {
				return memory.DependencyValidation{}, err
			}
			if options.AsOf != "" && hasCreated && createdAt.After(instant) {
				continue
			}
			ancestors := make(map[string]bool, len(item.ancestors)+1)
			for id := range item.ancestors {
				ancestors[id] = true
			}
			ancestors[item.id] = true
			pending = append(pending, dependencyPending{id: edge.DstID, ancestors: ancestors})
		}
	}
	if incomplete {
		receipt.Status = "incomplete"
	} else if len(receipt.Issues) > 0 {
		receipt.Status = "invalidated"
	}
	return receipt, nil
}
