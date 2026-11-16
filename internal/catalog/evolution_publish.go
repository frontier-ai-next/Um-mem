package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

var ErrTopologyRevisionConflict = errors.New("topology evolution revision conflict")

func topologyRevisionConflict(reason string) error {
	return fmt.Errorf("%w: %s", ErrTopologyRevisionConflict, reason)
}

type evolutionTarget struct {
	Block              memory.Block `json:"block"`
	BackendPartitionID string       `json:"backend_partition_id"`
}

type evolutionAssignment struct {
	RecordRevisionID string `json:"record_revision_id"`
	TargetBlockID    string `json:"target_block_id"`
}

type evolutionMapping struct {
	Operation   string                         `json:"operation"`
	Sources     map[string]int                 `json:"sources"`
	Targets     []evolutionTarget              `json:"targets"`
	Assignments map[string]evolutionAssignment `json:"assignments"`
	Reason      string                         `json:"reason"`
	Actor       string                         `json:"actor"`
}

func decodeEvolutionMapping(payload map[string]any) (evolutionMapping, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return evolutionMapping{}, err
	}
	var mapping evolutionMapping
	if err := json.Unmarshal(raw, &mapping); err != nil {
		return evolutionMapping{}, errors.New("topology evolution mapping is invalid")
	}
	if (mapping.Operation != "split" && mapping.Operation != "merge") ||
		len(mapping.Sources) == 0 || len(mapping.Targets) == 0 || len(mapping.Assignments) == 0 ||
		strings.TrimSpace(mapping.Reason) == "" || strings.TrimSpace(mapping.Actor) == "" {
		return evolutionMapping{}, errors.New("topology evolution requires nonempty exact mappings")
	}
	for id, revision := range mapping.Sources {
		if strings.TrimSpace(id) != id || id == "" || revision < 1 {
			return evolutionMapping{}, errors.New("topology evolution source mapping is invalid")
		}
	}
	targets := make(map[string]bool, len(mapping.Targets))
	for i := range mapping.Targets {
		target := &mapping.Targets[i]
		target.Block.Normalize()
		if err := target.Block.Validate(); err != nil {
			return evolutionMapping{}, err
		}
		if target.Block.ParentID == "" || target.Block.IndexOnly ||
			strings.TrimSpace(target.BackendPartitionID) == "" || targets[target.Block.BlockID] {
			return evolutionMapping{}, errors.New("topology evolution target mapping is invalid")
		}
		targets[target.Block.BlockID] = true
	}
	covered := make(map[string]bool, len(targets))
	for id, assignment := range mapping.Assignments {
		if id == "" || strings.TrimSpace(id) != id ||
			strings.TrimSpace(assignment.RecordRevisionID) == "" || !targets[assignment.TargetBlockID] {
			return evolutionMapping{}, errors.New("topology evolution assignment is invalid")
		}
		covered[assignment.TargetBlockID] = true
	}
	if len(covered) != len(targets) {
		return evolutionMapping{}, errors.New("topology evolution assignments must cover all targets")
	}
	return mapping, nil
}

func (store *Store) evolutionSourceConn(ctx context.Context, conn *sql.Conn,
	id string, revision int, operation string) error {
	state, err := scanBlock(conn.QueryRowContext(ctx, `SELECT `+blockColumns+`
		FROM topology_blocks WHERE block_id=? AND workspace_id=? AND profile_id=?`,
		id, store.workspaceID, store.profileID))
	if err == sql.ErrNoRows {
		return topologyRevisionConflict("topology evolution source changed before publication")
	}
	if err != nil {
		return err
	}
	if state.Status != "active" || state.Revision != revision || state.Protected || state.Block.IndexOnly {
		return topologyRevisionConflict("topology evolution source changed before publication")
	}
	var children int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_blocks WHERE
		workspace_id=? AND profile_id=? AND parent_id=? AND status='active'`,
		store.workspaceID, store.profileID, id).Scan(&children); err != nil {
		return err
	}
	if children != 0 {
		return topologyRevisionConflict("topology evolution source gained active children")
	}
	rows, err := conn.QueryContext(ctx, `SELECT `+maskColumns+` FROM active_masks WHERE
		workspace_id=? AND profile_id=? AND status IN ('active','suspended')`,
		store.workspaceID, store.profileID)
	if err != nil {
		return err
	}
	for rows.Next() {
		mask, err := scanMask(rows, store.workspaceID, store.profileID)
		if err != nil {
			rows.Close()
			return err
		}
		if !mask.IsExpired(time.Now().UTC()) {
			for _, selected := range mask.SelectedBlockIDs {
				if selected == id {
					rows.Close()
					return topologyRevisionConflict("topology evolution source has active mask references")
				}
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
	var conflictingEdges int
	if operation == "split" {
		err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_edges WHERE
			workspace_id=? AND profile_id=? AND (src_id=? OR dst_id=?)
			AND NOT (src_id=? AND edge_type='derived_from')`,
			store.workspaceID, store.profileID, id, id, id).Scan(&conflictingEdges)
	} else {
		err = conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_edges WHERE
			workspace_id=? AND profile_id=? AND dst_id=?`,
			store.workspaceID, store.profileID, id).Scan(&conflictingEdges)
	}
	if err != nil {
		return err
	}
	if conflictingEdges != 0 {
		return topologyRevisionConflict("topology evolution source graph edges changed")
	}
	return nil
}

// PublishTopologyEvolution atomically swaps the visible catalog topology and
// exact record membership pointers. The caller must hold the canonical write
// gate and verify the current record set before entering this catalog method.
func (store *Store) PublishTopologyEvolution(ctx context.Context,
	operationID string) (TopologyEvolution, error) {
	if operationID == "" || operationID != strings.TrimSpace(operationID) {
		return TopologyEvolution{}, errors.New("topology evolution requires an exact operation_id")
	}
	var published TopologyEvolution
	err := immediate(ctx, store.db, func(conn *sql.Conn) error {
		evolution, err := store.topologyEvolutionConn(ctx, conn, operationID)
		if err != nil {
			return err
		}
		if evolution == nil {
			return errors.New("topology evolution operation was not found")
		}
		if evolution.State == "published" {
			published = *evolution
			return nil
		}
		if evolution.State != "projections_ready" {
			return errors.New("topology native projections are not ready")
		}
		mapping, err := decodeEvolutionMapping(evolution.Payload)
		if err != nil {
			return err
		}
		sourceIDs := make([]string, 0, len(mapping.Sources))
		for id := range mapping.Sources {
			sourceIDs = append(sourceIDs, id)
		}
		sort.Strings(sourceIDs)
		for _, id := range sourceIDs {
			if err := store.evolutionSourceConn(ctx, conn, id, mapping.Sources[id], mapping.Operation); err != nil {
				return err
			}
		}
		targetByID := make(map[string]evolutionTarget, len(mapping.Targets))
		targetIDs := make([]string, 0, len(mapping.Targets))
		for _, target := range mapping.Targets {
			id := target.Block.BlockID
			var existing int
			err := conn.QueryRowContext(ctx, `SELECT 1 FROM topology_blocks WHERE block_id=?`, id).Scan(&existing)
			if err == nil {
				return topologyRevisionConflict("topology evolution target already exists")
			}
			if err != sql.ErrNoRows {
				return err
			}
			parent, err := scanBlock(conn.QueryRowContext(ctx, `SELECT `+blockColumns+`
				FROM topology_blocks WHERE block_id=? AND workspace_id=? AND profile_id=?`,
				target.Block.ParentID, store.workspaceID, store.profileID))
			if err == sql.ErrNoRows {
				return topologyRevisionConflict("topology evolution parent changed")
			}
			if err != nil {
				return err
			}
			if parent.Status != "active" || !parent.Block.IndexOnly {
				return topologyRevisionConflict("topology evolution parent changed")
			}
			targetByID[id] = target
			targetIDs = append(targetIDs, id)
		}
		revision, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		stamp := now()
		for _, target := range mapping.Targets {
			blockJSON, err := canonical(target.Block)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO topology_blocks(
				block_id,workspace_id,profile_id,parent_id,status,revision,protected,
				backend_partition_id,payload_json,created_at,updated_at)
				VALUES(?,?,?,?,'active',1,0,?,?,?,?)`, target.Block.BlockID,
				store.workspaceID, store.profileID, target.Block.ParentID,
				target.BackendPartitionID, blockJSON, stamp, stamp); err != nil {
				return err
			}
			for _, sourceID := range sourceIDs {
				edgeID, err := uuid4()
				if err != nil {
					return err
				}
				if _, err := conn.ExecContext(ctx, `INSERT INTO topology_edges(
					edge_id,workspace_id,profile_id,src_id,dst_id,edge_type,reason,revision,created_at)
					VALUES(?,?,?,?,?,'derived_from',?,1,?)`, edgeID, store.workspaceID,
					store.profileID, target.Block.BlockID, sourceID, mapping.Reason, stamp); err != nil {
					return err
				}
			}
		}
		for _, sourceID := range sourceIDs {
			result, err := conn.ExecContext(ctx, `UPDATE topology_blocks SET
				status='archived',revision=revision+1,updated_at=? WHERE
				block_id=? AND workspace_id=? AND profile_id=? AND revision=? AND status='active'`,
				stamp, sourceID, store.workspaceID, store.profileID, mapping.Sources[sourceID])
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil || count != 1 {
				return topologyRevisionConflict("topology evolution source CAS conflict")
			}
		}
		for recordID, assignment := range mapping.Assignments {
			var priorWorkspace, priorProfile string
			err := conn.QueryRowContext(ctx, `SELECT workspace_id,profile_id FROM
				published_record_memberships WHERE record_id=?`, recordID).Scan(&priorWorkspace, &priorProfile)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil && (priorWorkspace != store.workspaceID || priorProfile != store.profileID) {
				return errors.New("topology membership scope changed")
			}
			target := targetByID[assignment.TargetBlockID]
			if _, err := conn.ExecContext(ctx, `INSERT INTO published_record_memberships(
				record_id,workspace_id,profile_id,record_revision_id,block_id,
				backend_partition_id,catalog_revision_id,operation_id,published_at)
				VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(record_id) DO UPDATE SET
				record_revision_id=excluded.record_revision_id,block_id=excluded.block_id,
				backend_partition_id=excluded.backend_partition_id,
				catalog_revision_id=excluded.catalog_revision_id,operation_id=excluded.operation_id,
				published_at=excluded.published_at`, recordID, store.workspaceID,
				store.profileID, assignment.RecordRevisionID, assignment.TargetBlockID,
				target.BackendPartitionID, "topology:"+operationID, operationID, stamp); err != nil {
				return err
			}
		}
		result, err := conn.ExecContext(ctx, `UPDATE topology_evolutions SET
			state='published',updated_at=? WHERE operation_id=? AND workspace_id=?
			AND profile_id=? AND state='projections_ready'`,
			stamp, operationID, store.workspaceID, store.profileID)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return topologyRevisionConflict("topology evolution changed before publication")
		}
		affected := append(sourceIDs, targetIDs...)
		if err := store.mutationTx(ctx, conn, revision, mapping.Operation+"_blocks",
			mapping.Actor, mapping.Reason, affected,
			map[string]any{"sources": evolution.Payload["sources"],
				"assignments": evolution.Payload["assignments"]},
			map[string]any{"targets": evolution.Payload["targets"],
				"operation_id": operationID}); err != nil {
			return err
		}
		published = *evolution
		published.State = "published"
		return nil
	})
	return published, err
}
