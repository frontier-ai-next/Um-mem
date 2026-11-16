package engine

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	_ "modernc.org/sqlite"
)

type ListRecordsRequest struct {
	Scope            string
	ScopeID          string
	LogicalBlockID   string
	Limit            int
	Cursor           string
	IncludeRetracted bool
}

type RecordPage struct {
	Records        []map[string]string `json:"records"`
	NextCursor     string              `json:"next_cursor"`
	SnapshotCount  int                 `json:"snapshot_count"`
	Scope          string              `json:"scope"`
	ScopeID        string              `json:"scope_id"`
	LogicalBlockID string              `json:"logical_block_id"`
}

type recordPageCursor struct {
	Version        int    `json:"version"`
	WorkspaceID    string `json:"workspace_id"`
	ProfileID      string `json:"profile_id"`
	Scope          string `json:"scope"`
	ScopeID        string `json:"scope_id"`
	LogicalBlockID string `json:"logical_block_id"`
	Position       int    `json:"position"`
	SnapshotID     string `json:"snapshot_id"`
}

func validRecordScope(scope string) bool {
	switch scope {
	case "user_global", "workspace", "project", "session", "task", "subtask":
		return true
	default:
		return false
	}
}

func (engine *Engine) pageDatabase() (*sql.DB, error) {
	root, err := paths.WorkspaceMemoryRoot(engine.config.RepoRoot, engine.config.WorkspaceID)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(root, "catalogs")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(engine.config.ProfileID))
	path := filepath.Join(directory, hex.EncodeToString(hash[:])+".record-pages.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=10000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS snapshots (id TEXT PRIMARY KEY, payload TEXT NOT NULL, touched REAL NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func snapshotRowsValid(rows [][]string) bool {
	if len(rows) > 2048 {
		return false
	}
	for _, row := range rows {
		if len(row) != 2 || row[0] == "" || row[1] == "" {
			return false
		}
	}
	return true
}

func snapshotHash(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func (engine *Engine) snapshotRows(ctx context.Context, id string, selected [][]string) ([][]string, string, error) {
	db, err := engine.pageDatabase()
	if err != nil {
		return nil, "", err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	now := float64(time.Now().UnixNano()) / 1e9
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE touched < ?`, now-86400); err != nil {
		return nil, "", err
	}
	if selected != nil {
		payload, err := jsoncompat.MarshalPython(selected, false)
		if err != nil {
			return nil, "", err
		}
		if len(payload) > 450_000 {
			return nil, "", errors.New("list_records snapshot exceeds cursor budget")
		}
		id = snapshotHash(payload)
		if _, err := tx.ExecContext(ctx, `INSERT INTO snapshots(id,payload,touched) VALUES(?,?,?)
			ON CONFLICT(id) DO UPDATE SET touched=excluded.touched`, id, string(payload), now); err != nil {
			return nil, "", err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE id NOT IN
			(SELECT id FROM snapshots ORDER BY touched DESC,id LIMIT 64)`); err != nil {
			return nil, "", err
		}
	} else {
		if len(id) != 64 {
			return nil, "", errors.New("list_records cursor is invalid")
		}
		if _, err := hex.DecodeString(id); err != nil || strings.ToLower(id) != id {
			return nil, "", errors.New("list_records cursor is invalid")
		}
		var payload string
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM snapshots WHERE id=?`, id).Scan(&payload); err != nil {
			if err == sql.ErrNoRows {
				return nil, "", errors.New("list_records snapshot expired; restart enumeration")
			}
			return nil, "", err
		}
		if snapshotHash([]byte(payload)) != id {
			return nil, "", errors.New("list_records snapshot integrity failure")
		}
		if err := json.Unmarshal([]byte(payload), &selected); err != nil {
			return nil, "", errors.New("list_records cursor is invalid")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE snapshots SET touched=? WHERE id=?`, now, id); err != nil {
			return nil, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return selected, id, nil
}

func decodePageCursor(encoded string) (recordPageCursor, error) {
	invalid := errors.New("list_records cursor is invalid")
	if len(encoded) > 4096 {
		return recordPageCursor{}, errors.New("list_records cursor exceeds the bounded snapshot size")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
	if err != nil || len(data) > 3072 {
		return recordPageCursor{}, invalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 8 {
		return recordPageCursor{}, invalid
	}
	for _, key := range []string{"version", "workspace_id", "profile_id", "scope", "scope_id", "logical_block_id", "position", "snapshot_id"} {
		if _, exists := fields[key]; !exists {
			return recordPageCursor{}, invalid
		}
	}
	var cursor recordPageCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != 2 {
		return recordPageCursor{}, invalid
	}
	return cursor, nil
}

// ListRecords enumerates one explicit scope from a bounded frozen ID/revision
// snapshot. The page cache uses Python's path, table and cursor contract.
func (engine *Engine) ListRecords(ctx context.Context, request ListRecordsRequest) (RecordPage, error) {
	page := RecordPage{Records: []map[string]string{}, Scope: request.Scope,
		ScopeID: request.ScopeID, LogicalBlockID: request.LogicalBlockID}
	if !validRecordScope(request.Scope) {
		return page, errors.New("list_records requires a typed scope")
	}
	if request.ScopeID == "" || request.ScopeID != strings.TrimSpace(request.ScopeID) {
		return page, errors.New("list_records requires an exact scope_id")
	}
	if request.LogicalBlockID != strings.TrimSpace(request.LogicalBlockID) {
		return page, errors.New("list_records requires an exact logical_block_id")
	}
	if request.Limit < 1 || request.Limit > 128 {
		return page, errors.New("list_records limit must be 1-128")
	}
	var selected [][]string
	position := 0
	var snapshotID string
	if request.Cursor != "" {
		cursor, err := decodePageCursor(request.Cursor)
		if err != nil {
			return page, err
		}
		if cursor.WorkspaceID != engine.config.WorkspaceID || cursor.ProfileID != engine.config.ProfileID ||
			cursor.Scope != request.Scope || cursor.ScopeID != request.ScopeID ||
			cursor.LogicalBlockID != request.LogicalBlockID {
			return page, errors.New("list_records cursor scope does not match")
		}
		selected, snapshotID, err = engine.snapshotRows(ctx, cursor.SnapshotID, nil)
		if err != nil {
			return page, err
		}
		if !snapshotRowsValid(selected) || cursor.Position < 0 || cursor.Position >= len(selected) {
			return page, errors.New("list_records cursor is invalid")
		}
		position = cursor.Position
	} else {
		rows, err := engine.index.ProjectionRows(ctx)
		if err != nil {
			return page, err
		}
		records := make([]memory.MemoryRecord, 0)
		key := request.Scope + ":" + request.ScopeID
		for _, row := range rows {
			record := row.Record
			if row.Status != "committed" || row.Backend != engine.recordNativeStore(row.Record) ||
				record.Scope != request.Scope || record.DeliveryPolicy == "hidden" ||
				record.BlockMembership == nil || !request.IncludeRetracted && !row.Current {
				continue
			}
			actual, err := record.ScopeKey()
			if err != nil || actual != key || request.LogicalBlockID != "" &&
				record.BlockMembership.PrimaryBlockID != request.LogicalBlockID {
				continue
			}
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].RecordedAt != records[j].RecordedAt {
				return records[i].RecordedAt < records[j].RecordedAt
			}
			return records[i].BlockID < records[j].BlockID
		})
		if len(records) > 2048 {
			return page, errors.New("list_records scope exceeds 2048 records; select a narrower scope")
		}
		selected = make([][]string, 0, len(records))
		for _, record := range records {
			selected = append(selected, []string{record.BlockID, record.RevisionID})
		}
	}
	page.SnapshotCount = len(selected)
	end := min(len(selected), position+request.Limit)
	for _, pair := range selected[position:end] {
		record, err := engine.ReadStored(ctx, pair[0])
		if err != nil {
			return page, err
		}
		if record == nil || record.RevisionID != pair[1] || record.Scope != request.Scope ||
			record.DeliveryPolicy == "hidden" || record.BlockMembership == nil ||
			request.LogicalBlockID != "" && record.BlockMembership.PrimaryBlockID != request.LogicalBlockID {
			return page, errors.New("list_records snapshot was invalidated")
		}
		key, err := record.ScopeKey()
		if err != nil || key != request.Scope+":"+request.ScopeID {
			return page, errors.New("list_records snapshot was invalidated")
		}
		page.Records = append(page.Records, map[string]string{"record_id": record.BlockID,
			"revision_id": record.RevisionID, "logical_block_id": record.BlockMembership.PrimaryBlockID})
	}
	next := position + len(page.Records)
	if next < len(selected) {
		if snapshotID == "" {
			var err error
			_, snapshotID, err = engine.snapshotRows(ctx, "", selected)
			if err != nil {
				return page, err
			}
		}
		cursor := recordPageCursor{Version: 2, WorkspaceID: engine.config.WorkspaceID,
			ProfileID: engine.config.ProfileID, Scope: request.Scope, ScopeID: request.ScopeID,
			LogicalBlockID: request.LogicalBlockID, Position: next, SnapshotID: snapshotID}
		encoded, err := jsoncompat.MarshalPython(cursor, false)
		if err != nil {
			return page, err
		}
		if len(encoded) > 3072 {
			return page, errors.New("list_records snapshot exceeds cursor budget")
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}
