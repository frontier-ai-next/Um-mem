package catalog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	_ "modernc.org/sqlite"
)

const SchemaVersion = 2

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db          *sql.DB
	path        string
	workspaceID string
	profileID   string
}

type BlockState struct {
	Block              memory.Block `json:"block"`
	WorkspaceID        string       `json:"workspace_id"`
	ProfileID          string       `json:"profile_id"`
	Revision           int          `json:"revision"`
	Status             string       `json:"status"`
	Protected          bool         `json:"protected"`
	BackendPartitionID string       `json:"backend_partition_id"`
	CreatedAt          string       `json:"created_at"`
	UpdatedAt          string       `json:"updated_at"`
}

type CreateOptions struct {
	BackendPartitionID string
	Actor              string
	Reason             string
	Protected          bool
}

type ReplaceOptions struct {
	ExpectedRevision   int
	Status             string
	Actor              string
	Reason             string
	Operation          string
	AllowProtected     bool
	BackendPartitionID *string
}

func component(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return "", fmt.Errorf("invalid catalog %s", name)
	}
	return value, nil
}

func Path(repoRoot, workspaceID, profileID string) (string, error) {
	workspaceID, err := component(workspaceID, "workspace_id")
	if err != nil {
		return "", err
	}
	profileID, err = component(profileID, "profile_id")
	if err != nil {
		return "", err
	}
	root, err := paths.WorkspaceMemoryRoot(repoRoot, workspaceID)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(profileID))
	return filepath.Join(root, "catalogs", hex.EncodeToString(hash[:])+".sqlite"), nil
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00") }

func uuid4() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func canonical(value any) (string, error) {
	encoded, err := jsoncompat.MarshalPython(value, true)
	return string(encoded), err
}

func fingerprint(value any) (string, error) {
	encoded, err := canonical(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(encoded))
	return hex.EncodeToString(hash[:]), nil
}

func immediate(ctx context.Context, db *sql.DB, work func(*sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err := work(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func Open(repoRoot, workspaceID, profileID string) (*Store, error) {
	var err error
	workspaceID, err = component(workspaceID, "workspace_id")
	if err != nil {
		return nil, err
	}
	profileID, err = component(profileID, "profile_id")
	if err != nil {
		return nil, err
	}
	path, err := Path(repoRoot, workspaceID, profileID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path, workspaceID: workspaceID, profileID: profileID}
	if _, err := db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;"); err != nil {
		db.Close()
		return nil, err
	}
	err = immediate(context.Background(), db, func(conn *sql.Conn) error {
		var tables int
		if err := conn.QueryRowContext(context.Background(), `SELECT count(*) FROM sqlite_master WHERE type='table'`).Scan(&tables); err != nil {
			return err
		}
		if tables == 0 {
			if _, err := conn.ExecContext(context.Background(), schemaSQL); err != nil {
				return err
			}
			if _, err := conn.ExecContext(context.Background(), `INSERT INTO catalog_metadata(key,value) VALUES('schema_version',?),('catalog_revision',0)`, SchemaVersion); err != nil {
				return err
			}
		}
		var version int
		if err := conn.QueryRowContext(context.Background(), `SELECT value FROM catalog_metadata WHERE key='schema_version'`).Scan(&version); err != nil {
			return err
		}
		if version != SchemaVersion {
			return fmt.Errorf("unsupported workspace catalog schema version %d", version)
		}
		for _, statement := range []string{
			`CREATE INDEX IF NOT EXISTS idx_topology_navigation_order ON topology_blocks(workspace_id,profile_id,parent_id,status,created_at,block_id)`,
			`CREATE INDEX IF NOT EXISTS idx_topology_edges_src_order ON topology_edges(workspace_id,profile_id,src_id,created_at,edge_id)`,
			`CREATE INDEX IF NOT EXISTS idx_topology_edges_dst_order ON topology_edges(workspace_id,profile_id,dst_id,created_at,edge_id)`,
			`CREATE INDEX IF NOT EXISTS idx_topology_edges_profile_recent ON topology_edges(workspace_id,profile_id,created_at,edge_id)`,
		} {
			if _, err := conn.ExecContext(context.Background(), statement); err != nil {
				return err
			}
		}
		var rootID string
		err := conn.QueryRowContext(context.Background(), `SELECT block_id FROM topology_blocks LIMIT 1`).Scan(&rootID)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == sql.ErrNoRows {
			return store.createRootTx(context.Background(), conn)
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Close() error     { return store.db.Close() }
func (store *Store) FilePath() string { return store.path }

func rootBlock(id string) memory.Block {
	return memory.Block{
		BlockID: id, BlockType: "workspace_profile_root", Temperature: "warm",
		Scope: "workspace", Authority: "advisory", DeliveryPolicy: "on_demand",
		Retention: "durable", Name: "Memory", Guidance: map[string]any{
			"synopsis": "Protected root for this workspace and profile.",
			"use_when": []string{}, "avoid_when": []string{},
		}, IndexOnly: true, AgentWritable: false, Owner: "UM-MEM", Producer: "catalog",
		Consumers: []string{"memory_agent"}, RecallPath: "hybrid_recall",
		LifecyclePolicy: map[string]any{"initial_status": "active", "mutable": false,
			"terminal_statuses": []string{"archived"}},
		IsolationPolicy: map[string]any{"dimension": "workspace", "cross_workspace": false},
		TokenBudget:     0,
	}
}

func (store *Store) mutationTx(ctx context.Context, conn *sql.Conn, previous int, operation, actor, reason string, affected []string, input, output any) error {
	operation = strings.TrimSpace(operation)
	actor = strings.TrimSpace(actor)
	reason = strings.TrimSpace(reason)
	if operation == "" || actor == "" || reason == "" {
		return errors.New("catalog mutation requires operation, actor and reason")
	}
	result, err := conn.ExecContext(ctx, `UPDATE catalog_metadata SET value=? WHERE key='catalog_revision' AND value=?`, previous+1, previous)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("catalog revision conflict")
	}
	inputHash, err := fingerprint(input)
	if err != nil {
		return err
	}
	outputHash, err := fingerprint(output)
	if err != nil {
		return err
	}
	affectedJSON, err := canonical(affected)
	if err != nil {
		return err
	}
	id, err := uuid4()
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO catalog_mutations(
		mutation_id,workspace_id,profile_id,previous_revision,next_revision,actor,reason,
		affected_ids_json,operation,input_fingerprint,output_fingerprint,validator_result,created_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,'accepted',?)`, id, store.workspaceID, store.profileID,
		previous, previous+1, actor, reason, affectedJSON, operation, inputHash, outputHash, now())
	return err
}

func (store *Store) revisionConn(ctx context.Context, conn *sql.Conn) (int, error) {
	var revision int
	err := conn.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE key='catalog_revision'`).Scan(&revision)
	return revision, err
}

func (store *Store) createRootTx(ctx context.Context, conn *sql.Conn) error {
	id, err := uuid4()
	if err != nil {
		return err
	}
	block := rootBlock(id)
	block.Normalize()
	if err := block.Validate(); err != nil {
		return err
	}
	payload, err := canonical(block)
	if err != nil {
		return err
	}
	stamp := now()
	if _, err := conn.ExecContext(ctx, `INSERT INTO topology_blocks(
		block_id,workspace_id,profile_id,parent_id,status,revision,protected,
		backend_partition_id,payload_json,created_at,updated_at
	) VALUES(?,?,?,'','active',1,1,'',?,?,?)`, id, store.workspaceID, store.profileID, payload, stamp, stamp); err != nil {
		return err
	}
	return store.mutationTx(ctx, conn, 0, "create_root", "system", "initialize workspace/profile catalog",
		[]string{id}, map[string]any{}, block)
}

func (store *Store) CatalogRevision(ctx context.Context) (int, error) {
	var revision int
	err := store.db.QueryRowContext(ctx, `SELECT value FROM catalog_metadata WHERE key='catalog_revision'`).Scan(&revision)
	return revision, err
}

func scanBlock(rows interface{ Scan(...any) error }) (BlockState, error) {
	var result BlockState
	var rowID, parentID, payload string
	var protected int
	err := rows.Scan(&rowID, &result.WorkspaceID, &result.ProfileID, &parentID,
		&result.Status, &result.Revision, &protected, &result.BackendPartitionID,
		&payload, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return BlockState{}, err
	}
	if result.Status != "active" && result.Status != "archived" {
		return BlockState{}, errors.New("stored catalog block status is invalid")
	}
	result.Protected = protected == 1
	if err := json.Unmarshal([]byte(payload), &result.Block); err != nil {
		return BlockState{}, err
	}
	result.Block.Normalize()
	if err := result.Block.Validate(); err != nil {
		return BlockState{}, err
	}
	if result.Block.BlockID != rowID || result.Block.ParentID != parentID {
		return BlockState{}, errors.New("stored catalog block identity differs from row")
	}
	return result, nil
}

const blockColumns = `block_id,workspace_id,profile_id,parent_id,status,revision,protected,backend_partition_id,payload_json,created_at,updated_at`

func (store *Store) ListBlocks(ctx context.Context, includeArchived bool) ([]BlockState, error) {
	query := `SELECT ` + blockColumns + ` FROM topology_blocks WHERE workspace_id=? AND profile_id=?`
	if !includeArchived {
		query += ` AND status='active'`
	}
	query += ` ORDER BY created_at,block_id`
	rows, err := store.db.QueryContext(ctx, query, store.workspaceID, store.profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]BlockState, 0)
	for rows.Next() {
		state, err := scanBlock(rows)
		if err != nil {
			return nil, err
		}
		if state.WorkspaceID != store.workspaceID || state.ProfileID != store.profileID {
			return nil, errors.New("catalog block escaped selected scope")
		}
		result = append(result, state)
	}
	return result, rows.Err()
}

func (store *Store) GetBlock(ctx context.Context, id string) (*BlockState, error) {
	row := store.db.QueryRowContext(ctx, `SELECT `+blockColumns+` FROM topology_blocks WHERE workspace_id=? AND profile_id=? AND block_id=?`, store.workspaceID, store.profileID, id)
	state, err := scanBlock(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if state.WorkspaceID != store.workspaceID || state.ProfileID != store.profileID {
		return nil, errors.New("catalog block escaped selected scope")
	}
	return &state, nil
}

func (store *Store) CreateBlock(ctx context.Context, block memory.Block, options CreateOptions) (BlockState, error) {
	block.Normalize()
	if err := block.Validate(); err != nil {
		return BlockState{}, err
	}
	if block.ParentID == "" {
		return BlockState{}, errors.New("new catalog block requires an existing parent")
	}
	if strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "" {
		return BlockState{}, errors.New("catalog create requires actor and reason")
	}
	payload, err := canonical(block)
	if err != nil {
		return BlockState{}, err
	}
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		var parentStatus string
		var parentPayload string
		if err := conn.QueryRowContext(ctx, `SELECT status,payload_json FROM topology_blocks WHERE block_id=? AND workspace_id=? AND profile_id=?`,
			block.ParentID, store.workspaceID, store.profileID).Scan(&parentStatus, &parentPayload); err != nil {
			return err
		}
		if parentStatus != "active" {
			return errors.New("catalog parent is archived")
		}
		var parent memory.Block
		if err := json.Unmarshal([]byte(parentPayload), &parent); err != nil {
			return err
		}
		if parent.BlockID != block.ParentID || !parent.IndexOnly {
			return errors.New("dynamic memory blocks require an index-only parent")
		}
		var existing string
		err := conn.QueryRowContext(ctx, `SELECT block_id FROM topology_blocks WHERE block_id=?`, block.BlockID).Scan(&existing)
		if err == nil {
			return errors.New("memory block already exists")
		}
		if err != sql.ErrNoRows {
			return err
		}
		revision, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		stamp := now()
		protected := 0
		if options.Protected {
			protected = 1
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO topology_blocks(
			block_id,workspace_id,profile_id,parent_id,status,revision,protected,
			backend_partition_id,payload_json,created_at,updated_at
		) VALUES(?,?,?,?,'active',1,?,?,?,?,?)`, block.BlockID, store.workspaceID, store.profileID,
			block.ParentID, protected, options.BackendPartitionID, payload, stamp, stamp); err != nil {
			return err
		}
		return store.mutationTx(ctx, conn, revision, "create_block", options.Actor, options.Reason,
			[]string{block.BlockID}, map[string]string{"parent_id": block.ParentID}, block)
	})
	if err != nil {
		return BlockState{}, err
	}
	state, err := store.GetBlock(ctx, block.BlockID)
	if err != nil {
		return BlockState{}, err
	}
	if state == nil {
		return BlockState{}, errors.New("created catalog block is missing")
	}
	return *state, nil
}

func blockStateMapping(state BlockState) (map[string]any, error) {
	encoded, err := json.Marshal(state.Block)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	result["workspace_id"] = state.WorkspaceID
	result["profile_id"] = state.ProfileID
	result["revision"] = state.Revision
	result["status"] = state.Status
	result["protected"] = state.Protected
	result["backend_partition_id"] = state.BackendPartitionID
	result["created_at"] = state.CreatedAt
	result["updated_at"] = state.UpdatedAt
	return result, nil
}

// ReplaceBlock advances one block revision only when the observed revision
// still matches the committed row. Root protection is explicit.
func (store *Store) ReplaceBlock(ctx context.Context, state BlockState, block memory.Block, options ReplaceOptions) (BlockState, error) {
	if state.WorkspaceID != store.workspaceID || state.ProfileID != store.profileID || state.Block.BlockID == "" {
		return BlockState{}, errors.New("catalog block state does not belong to selected scope")
	}
	if state.Protected && !options.AllowProtected {
		return BlockState{}, errors.New("protected root cannot be amended")
	}
	if options.ExpectedRevision != state.Revision || options.ExpectedRevision < 1 {
		return BlockState{}, errors.New("expected_revision does not match current block revision")
	}
	block.Normalize()
	if err := block.Validate(); err != nil {
		return BlockState{}, err
	}
	if block.BlockID != state.Block.BlockID {
		return BlockState{}, errors.New("catalog block identity cannot change")
	}
	status := options.Status
	if status == "" {
		status = state.Status
	}
	if status != "active" && status != "archived" {
		return BlockState{}, errors.New("catalog block lifecycle is unsupported")
	}
	payload, err := canonical(block)
	if err != nil {
		return BlockState{}, err
	}
	input, err := blockStateMapping(state)
	if err != nil {
		return BlockState{}, err
	}
	output, err := blockStateMapping(BlockState{Block: block})
	if err != nil {
		return BlockState{}, err
	}
	delete(output, "workspace_id")
	delete(output, "profile_id")
	delete(output, "revision")
	delete(output, "protected")
	delete(output, "backend_partition_id")
	delete(output, "created_at")
	delete(output, "updated_at")
	output["status"] = status
	partition := state.BackendPartitionID
	if options.BackendPartitionID != nil {
		partition = *options.BackendPartitionID
	}
	err = immediate(ctx, store.db, func(conn *sql.Conn) error {
		previous, err := store.revisionConn(ctx, conn)
		if err != nil {
			return err
		}
		if status == "active" && block.ParentID != "" {
			var parentStatus, parentPayload string
			if err := conn.QueryRowContext(ctx, `SELECT status,payload_json FROM topology_blocks WHERE
				block_id=? AND workspace_id=? AND profile_id=?`, block.ParentID,
				store.workspaceID, store.profileID).Scan(&parentStatus, &parentPayload); err != nil {
				return err
			}
			var parent memory.Block
			if err := json.Unmarshal([]byte(parentPayload), &parent); err != nil {
				return err
			}
			if parentStatus != "active" || parent.BlockID != block.ParentID || !parent.IndexOnly {
				return errors.New("dynamic memory blocks require an active index-only parent")
			}
			seen := map[string]bool{block.BlockID: true}
			for ancestor := block.ParentID; ancestor != ""; {
				if seen[ancestor] {
					return errors.New("dynamic memory block hierarchy would create a cycle")
				}
				seen[ancestor] = true
				var next string
				if err := conn.QueryRowContext(ctx, `SELECT parent_id FROM topology_blocks WHERE
					block_id=? AND workspace_id=? AND profile_id=?`, ancestor,
					store.workspaceID, store.profileID).Scan(&next); err != nil {
					return err
				}
				ancestor = next
			}
		}
		if status == "archived" {
			if err := store.archiveGuardsConn(ctx, conn, block.BlockID); err != nil {
				return err
			}
		}
		stamp := now()
		result, err := conn.ExecContext(ctx, `UPDATE topology_blocks SET
			parent_id=?,status=?,revision=?,payload_json=?,updated_at=?,backend_partition_id=?
			WHERE block_id=? AND workspace_id=? AND profile_id=? AND revision=?`,
			block.ParentID, status, state.Revision+1, payload, stamp, partition,
			block.BlockID, store.workspaceID, store.profileID, options.ExpectedRevision)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("expected_revision does not match current block revision")
		}
		return store.mutationTx(ctx, conn, previous, options.Operation, options.Actor, options.Reason,
			[]string{block.BlockID}, input, output)
	})
	if err != nil {
		return BlockState{}, err
	}
	updated, err := store.GetBlock(ctx, block.BlockID)
	if err != nil {
		return BlockState{}, err
	}
	if updated == nil {
		return BlockState{}, errors.New("updated catalog block is missing")
	}
	return *updated, nil
}

func (store *Store) archiveGuardsConn(ctx context.Context, conn *sql.Conn, blockID string) error {
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_blocks WHERE
		workspace_id=? AND profile_id=? AND parent_id=? AND status='active'`,
		store.workspaceID, store.profileID, blockID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("dynamic memory block with active children cannot be archived")
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_edges WHERE
		workspace_id=? AND profile_id=? AND dst_id=?`,
		store.workspaceID, store.profileID, blockID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("dynamic memory block with incoming graph edges cannot be archived")
	}
	rows, err := conn.QueryContext(ctx, `SELECT `+maskColumns+` FROM active_masks
		WHERE workspace_id=? AND profile_id=? AND status IN ('active','suspended')`,
		store.workspaceID, store.profileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		mask, err := scanMask(rows, store.workspaceID, store.profileID)
		if err != nil {
			return err
		}
		if mask.IsExpired(time.Now().UTC()) {
			continue
		}
		for _, selected := range mask.SelectedBlockIDs {
			if selected == blockID {
				return errors.New("dynamic memory block with active mask references cannot be archived")
			}
		}
	}
	return rows.Err()
}
