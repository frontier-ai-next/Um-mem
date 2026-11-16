package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type topologyTarget struct {
	Block              memory.Block `json:"block"`
	BackendPartitionID string       `json:"backend_partition_id"`
}

type topologyAssignment struct {
	RecordRevisionID string `json:"record_revision_id"`
	TargetBlockID    string `json:"target_block_id"`
}

type topologyPayload struct {
	Operation   string                        `json:"operation"`
	Sources     map[string]int                `json:"sources"`
	Targets     []topologyTarget              `json:"targets"`
	Assignments map[string]topologyAssignment `json:"assignments"`
	Reason      string                        `json:"reason"`
	Actor       string                        `json:"actor"`
	RequestHash string                        `json:"request_hash"`
}

func parseTopologyPayload(raw map[string]any) (topologyPayload, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return topologyPayload{}, err
	}
	var payload topologyPayload
	if err := json.Unmarshal(encoded, &payload); err != nil ||
		(payload.Operation != "split" && payload.Operation != "merge") ||
		len(payload.Sources) == 0 || len(payload.Targets) == 0 || len(payload.Assignments) == 0 {
		return topologyPayload{}, errors.New("topology evolution mapping is invalid")
	}
	if payload.Operation == "split" && len(payload.Sources) != 1 ||
		payload.Operation == "merge" && (len(payload.Sources) < 2 || len(payload.Targets) != 1) {
		return topologyPayload{}, errors.New("topology evolution source or target count is invalid")
	}
	for id, revision := range payload.Sources {
		if id == "" || id != strings.TrimSpace(id) || revision < 1 {
			return topologyPayload{}, errors.New("topology evolution source mapping is invalid")
		}
	}
	targets := make(map[string]topologyTarget, len(payload.Targets))
	for _, target := range payload.Targets {
		if target.Block.BlockID == "" || target.Block.BlockID != strings.TrimSpace(target.Block.BlockID) ||
			target.BackendPartitionID == "" || target.BackendPartitionID != strings.TrimSpace(target.BackendPartitionID) ||
			targets[target.Block.BlockID].Block.BlockID != "" {
			return topologyPayload{}, errors.New("topology evolution target mapping is invalid")
		}
		targets[target.Block.BlockID] = target
	}
	covered := make(map[string]bool, len(targets))
	for recordID, assignment := range payload.Assignments {
		if recordID == "" || recordID != strings.TrimSpace(recordID) ||
			assignment.RecordRevisionID == "" || assignment.RecordRevisionID != strings.TrimSpace(assignment.RecordRevisionID) ||
			assignment.TargetBlockID == "" || targets[assignment.TargetBlockID].Block.BlockID == "" {
			return topologyPayload{}, errors.New("topology evolution assignment is invalid")
		}
		covered[assignment.TargetBlockID] = true
	}
	if len(covered) != len(targets) {
		return topologyPayload{}, errors.New("topology evolution assignments must cover all targets")
	}
	return payload, nil
}

func (engine *Engine) topologyCurrentRows(ctx context.Context,
	sources map[string]int) (map[string]store.ProjectionRow, error) {
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	current := make(map[string]store.ProjectionRow)
	for _, row := range rows {
		record := row.Record
		if record.BlockMembership == nil || sources[record.BlockMembership.PrimaryBlockID] < 1 {
			continue
		}
		if row.Status == "pending" {
			return nil, errors.New("topology source has a pending canonical projection")
		}
		if !row.Current || record.Status == "superseded" || record.Status == "deprecated" ||
			record.Status == "quarantined" || record.Status == "retracted" || record.Status == "expired" {
			continue
		}
		if row.Status != "committed" || row.Backend != engine.recordNativeStore(record) || row.Revision < 1 {
			return nil, errors.New("topology source record is not a committed projection")
		}
		if _, duplicate := current[record.BlockID]; duplicate {
			return nil, errors.New("topology source record coverage overlaps")
		}
		current[record.BlockID] = row
	}
	return current, nil
}

func topologyExpectedRevisions(payload topologyPayload) map[string]string {
	expected := make(map[string]string, len(payload.Assignments))
	for id, assignment := range payload.Assignments {
		expected[id] = assignment.RecordRevisionID
	}
	return expected
}

func topologyActualRevisions(rows map[string]store.ProjectionRow) map[string]string {
	actual := make(map[string]string, len(rows))
	for id, row := range rows {
		actual[id] = row.Record.RevisionID
	}
	return actual
}

func (engine *Engine) abortTopologyMismatch(ctx context.Context,
	operationID, reason, message string) error {
	_, err := engine.topology.AbortTopologyEvolution(ctx, operationID, reason)
	return errors.Join(errors.New(message), err)
}

// ResumeTopologyEvolution executes only a previously durable exact mapping.
// The public split/merge constructors will use this same journal path.
func (engine *Engine) ResumeTopologyEvolution(ctx context.Context,
	operationID string) (map[string]any, error) {
	if operationID == "" || operationID != strings.TrimSpace(operationID) {
		return nil, errors.New("topology evolution requires an exact operation_id")
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	evolution, err := engine.topology.TopologyEvolution(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if evolution == nil {
		return nil, errors.New("topology evolution operation was not found")
	}
	if evolution.State == "aborted" {
		return nil, errors.New("topology evolution was aborted: " + evolution.AbortReason)
	}
	payload, err := parseTopologyPayload(evolution.Payload)
	if err != nil {
		return nil, err
	}
	if evolution.State == "published" {
		if err := engine.RefreshCatalog(); err != nil {
			return nil, err
		}
		return engine.topologyEvolutionReceipt(ctx, operationID, payload)
	}
	if evolution.State == "prepared" {
		current, err := engine.topologyCurrentRows(ctx, payload.Sources)
		if err != nil {
			return nil, err
		}
		targets := make(map[string]topologyTarget, len(payload.Targets))
		for _, target := range payload.Targets {
			targets[target.Block.BlockID] = target
		}
		ids := make([]string, 0, len(payload.Assignments))
		for id := range payload.Assignments {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			assignment := payload.Assignments[id]
			row, found := current[id]
			if !found || row.Record.RevisionID != assignment.RecordRevisionID {
				return nil, engine.abortTopologyMismatch(ctx, operationID,
					"source_record_revision_changed", "topology source record revision changed during projection")
			}
			if row.Record.BlockMembership == nil || payload.Sources[row.Record.BlockMembership.PrimaryBlockID] < 1 {
				return nil, engine.abortTopologyMismatch(ctx, operationID,
					"source_record_membership_changed", "topology source record has no primary membership")
			}
			target := targets[assignment.TargetBlockID]
			if err := engine.stageTopologyProjection(ctx, row, assignment.TargetBlockID,
				target.BackendPartitionID, operationID); err != nil {
				return nil, err
			}
		}
		if err := engine.topology.MarkTopologyProjectionsReady(ctx, operationID); err != nil {
			return nil, err
		}
	} else if evolution.State != "projections_ready" {
		return nil, errors.New("topology evolution journal state is invalid")
	}
	err = engine.writer.WithCanonicalWriteGate(ctx, func() error {
		latest, err := engine.topology.TopologyEvolution(ctx, operationID)
		if err != nil {
			return err
		}
		if latest == nil {
			return errors.New("topology evolution operation was not found")
		}
		if latest.State == "published" {
			return nil
		}
		if latest.State == "aborted" {
			return errors.New("topology evolution was aborted: " + latest.AbortReason)
		}
		current, err := engine.topologyCurrentRows(ctx, payload.Sources)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(topologyActualRevisions(current), topologyExpectedRevisions(payload)) {
			return engine.abortTopologyMismatch(ctx, operationID,
				"source_record_set_changed", "topology source record set changed before publication")
		}
		_, err = engine.topology.PublishTopologyEvolution(ctx, operationID)
		if errors.Is(err, catalog.ErrTopologyRevisionConflict) {
			return engine.abortTopologyMismatch(ctx, operationID,
				"catalog_revision_conflict", err.Error())
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	return engine.topologyEvolutionReceipt(ctx, operationID, payload)
}

func (engine *Engine) topologyEvolutionReceipt(ctx context.Context,
	operationID string, payload topologyPayload) (map[string]any, error) {
	sourceIDs := make([]string, 0, len(payload.Sources))
	for id := range payload.Sources {
		sourceIDs = append(sourceIDs, id)
	}
	sort.Strings(sourceIDs)
	sources := make([]map[string]any, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		state, err := engine.topology.GetBlock(ctx, id)
		if err != nil || state == nil {
			return nil, errors.Join(err, fmt.Errorf("published topology source %q is missing", id))
		}
		mapped, err := topologyBlockMapping(*state)
		if err != nil {
			return nil, err
		}
		sources = append(sources, mapped)
	}
	targets := make([]map[string]any, 0, len(payload.Targets))
	for _, target := range payload.Targets {
		state, err := engine.topology.GetBlock(ctx, target.Block.BlockID)
		if err != nil || state == nil {
			return nil, errors.Join(err, fmt.Errorf("published topology target %q is missing", target.Block.BlockID))
		}
		mapped, err := topologyBlockMapping(*state)
		if err != nil {
			return nil, err
		}
		targets = append(targets, mapped)
	}
	edges := make([]catalog.BlockGraphEdge, 0)
	for _, target := range payload.Targets {
		id := target.Block.BlockID
		cursor := ""
		for {
			page, err := engine.topology.PageEdges(ctx, id, 200, cursor, nil)
			if err != nil {
				return nil, err
			}
			for _, edge := range page.Edges {
				if edge.SrcID == id && payload.Sources[edge.DstID] > 0 && edge.EdgeType == "derived_from" {
					edges = append(edges, edge)
				}
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	assignments := make(map[string]string, len(payload.Assignments))
	for id, assignment := range payload.Assignments {
		assignments[id] = assignment.TargetBlockID
	}
	revision, err := engine.topology.CatalogRevision(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"ok": true, "operation": payload.Operation,
		"operation_id": operationID, "generation_status": "published",
		"record_assignments": assignments, "provenance_edges": edges,
		"catalog_revision": revision}
	if payload.Operation == "split" {
		result["source"], result["targets"] = sources[0], targets
	} else {
		result["sources"], result["target"] = sources, targets[0]
	}
	return result, nil
}

func topologyBlockMapping(state catalog.BlockState) (map[string]any, error) {
	raw, err := json.Marshal(state.Block)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	result["workspace_id"], result["profile_id"] = state.WorkspaceID, state.ProfileID
	result["revision"], result["status"] = state.Revision, state.Status
	result["protected"], result["backend_partition_id"] = state.Protected, state.BackendPartitionID
	result["created_at"], result["updated_at"] = state.CreatedAt, state.UpdatedAt
	return result, nil
}
