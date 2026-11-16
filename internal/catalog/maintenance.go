package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var maintenanceTypes = map[string]bool{
	"capacity_pressure": true, "low_usage": true, "high_revision_count": true,
	"many_conflicts": true, "active_reference_present": true,
	"consolidation_available": true,
}

type MaintenanceObservation struct {
	ObservationID     string         `json:"observation_id"`
	WorkspaceID       string         `json:"workspace_id"`
	ProfileID         string         `json:"profile_id"`
	BlockID           string         `json:"block_id"`
	ObservationType   string         `json:"observation_type"`
	Values            map[string]any `json:"values"`
	ThresholdRevision string         `json:"threshold_revision"`
	EvidenceIDs       []string       `json:"evidence_ids"`
	CreatedAt         string         `json:"created_at"`
}

type AddObservationRequest struct {
	BlockID           string         `json:"block_id"`
	ObservationType   string         `json:"observation_type"`
	Values            map[string]any `json:"values"`
	ThresholdRevision string         `json:"threshold_revision"`
	EvidenceIDs       []string       `json:"evidence_ids"`
}

func numericObservationValue(value any) bool {
	switch actual := value.(type) {
	case json.Number:
		parsed, err := actual.Float64()
		return err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
	case float64:
		return !math.IsInf(actual, 0) && !math.IsNaN(actual)
	case float32:
		return !math.IsInf(float64(actual), 0) && !math.IsNaN(float64(actual))
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	}
	return false
}

func validateObservation(observation MaintenanceObservation) error {
	for _, value := range []string{observation.ObservationID, observation.WorkspaceID,
		observation.ProfileID, observation.BlockID, observation.ThresholdRevision,
		observation.CreatedAt} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("maintenance observation requires exact identities")
		}
	}
	if !maintenanceTypes[observation.ObservationType] {
		return errors.New("maintenance observation type is unsupported")
	}
	if observation.Values == nil {
		return errors.New("maintenance observation values must be numeric facts")
	}
	for _, value := range observation.Values {
		if !numericObservationValue(value) {
			return errors.New("maintenance observation values must be numeric facts")
		}
	}
	for _, id := range observation.EvidenceIDs {
		if id == "" || id != strings.TrimSpace(id) {
			return errors.New("maintenance observation evidence_id requires exact text")
		}
	}
	return nil
}

func (store *Store) AddObservation(ctx context.Context, request AddObservationRequest) (MaintenanceObservation, error) {
	id, err := uuid4()
	if err != nil {
		return MaintenanceObservation{}, err
	}
	evidence := make([]string, 0, len(request.EvidenceIDs))
	seen := map[string]bool{}
	for _, value := range request.EvidenceIDs {
		if !seen[value] {
			evidence = append(evidence, value)
			seen[value] = true
		}
	}
	observation := MaintenanceObservation{ObservationID: id,
		WorkspaceID: store.workspaceID, ProfileID: store.profileID,
		BlockID: request.BlockID, ObservationType: request.ObservationType,
		Values: request.Values, ThresholdRevision: request.ThresholdRevision,
		EvidenceIDs: evidence, CreatedAt: now()}
	if err := validateObservation(observation); err != nil {
		return MaintenanceObservation{}, err
	}
	valuesJSON, err := canonical(observation.Values)
	if err != nil {
		return MaintenanceObservation{}, err
	}
	evidenceJSON, err := canonical(observation.EvidenceIDs)
	if err != nil {
		return MaintenanceObservation{}, err
	}
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_blocks WHERE
			workspace_id=? AND profile_id=? AND block_id=?`, store.workspaceID,
			store.profileID, observation.BlockID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return errors.New("maintenance observation block was not found")
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO maintenance_observations(
			observation_id,workspace_id,profile_id,block_id,observation_type,
			values_json,threshold_revision,evidence_ids_json,created_at
		) VALUES(?,?,?,?,?,?,?,?,?)`, observation.ObservationID, observation.WorkspaceID,
			observation.ProfileID, observation.BlockID, observation.ObservationType,
			valuesJSON, observation.ThresholdRevision, evidenceJSON, observation.CreatedAt)
		return err
	})
	return observation, err
}

func (store *Store) Observations(ctx context.Context, blockID string) ([]MaintenanceObservation, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT observation_id,workspace_id,profile_id,block_id,
		observation_type,values_json,threshold_revision,evidence_ids_json,created_at
		FROM maintenance_observations WHERE workspace_id=? AND profile_id=? AND block_id=?
		ORDER BY created_at,observation_id`, store.workspaceID, store.profileID, blockID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]MaintenanceObservation, 0)
	for rows.Next() {
		var item MaintenanceObservation
		var valuesJSON, evidenceJSON string
		if err := rows.Scan(&item.ObservationID, &item.WorkspaceID, &item.ProfileID,
			&item.BlockID, &item.ObservationType, &valuesJSON, &item.ThresholdRevision,
			&evidenceJSON, &item.CreatedAt); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(strings.NewReader(valuesJSON))
		decoder.UseNumber()
		if err := decoder.Decode(&item.Values); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &item.EvidenceIDs); err != nil {
			return nil, err
		}
		if item.WorkspaceID != store.workspaceID || item.ProfileID != store.profileID ||
			item.BlockID != blockID {
			return nil, errors.New("maintenance observation scope differs from query")
		}
		if err := validateObservation(item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
