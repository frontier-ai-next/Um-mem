package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	_ "modernc.org/sqlite"
)

const IndexSchemaVersion = 16

// Index reads canonical records from the versioned SQLite store. This handle
// is read-only; canonical mutations use a separate transactional writer.
type Index struct {
	db            *sql.DB
	path          string
	workspaceID   string
	profileID     string
	topology      *catalog.Store
	metricsWriter *Writer
}

// BindTopologyCatalog enables published split/merge membership overlays for
// this selected profile. The engine owns and closes the catalog handle.
func (index *Index) BindTopologyCatalog(topology *catalog.Store) {
	index.topology = topology
}

// BindPartitionMetricsWriter attaches the engine's transactional writer for
// native operation observations. Canonical reads remain on the read-only DB.
func (index *Index) BindPartitionMetricsWriter(writer *Writer) {
	index.metricsWriter = writer
}

func OpenIndex(path, workspaceID, profileID string) (*Index, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteFileURI(absPath, "ro"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	err = db.QueryRow("SELECT value FROM schema_metadata WHERE key='schema_version'").Scan(&version)
	if err == nil && version != IndexSchemaVersion {
		err = fmt.Errorf("unsupported canonical index schema version %d", version)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Index{db: db, path: absPath, workspaceID: workspaceID, profileID: profileID}, nil
}

func sqliteFileURI(absPath, mode string) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absPath), RawQuery: "mode=" + mode}
	if filepath.VolumeName(absPath) != "" {
		uri.Path = "/" + uri.Path
	}
	return uri.String()
}

func (index *Index) Close() error { return index.db.Close() }

// ReadStoredRecord returns a committed canonical payload with its published
// topology membership overlay when the engine bound a catalog handle.
func (index *Index) ReadStoredRecord(ctx context.Context, recordID string) (*memory.MemoryRecord, error) {
	if strings.TrimSpace(recordID) == "" {
		return nil, fmt.Errorf("record ID is required")
	}
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	record, err := index.readStoredRecordTx(ctx, tx, recordID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if record != nil && index.topology != nil {
		effective, err := index.topology.EffectiveRecord(ctx, *record)
		if err != nil {
			return nil, err
		}
		return &effective, nil
	}
	return record, nil
}

func (index *Index) readStoredRecordTx(ctx context.Context, tx *sql.Tx, recordID string) (*memory.MemoryRecord, error) {
	var headStatus string
	err := tx.QueryRowContext(ctx,
		"SELECT status FROM canonical_head_outbox WHERE successor_id=? ORDER BY commit_revision DESC LIMIT 1",
		recordID,
	).Scan(&headStatus)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil && headStatus != "committed" {
		return nil, nil
	}
	var writeStatus string
	err = tx.QueryRowContext(ctx,
		"SELECT status FROM write_outbox WHERE block_id=? ORDER BY commit_revision DESC LIMIT 1",
		recordID,
	).Scan(&writeStatus)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil && (writeStatus == "failed" || writeStatus == "pending" && headStatus != "committed") {
		return nil, nil
	}
	var payload string
	err = tx.QueryRowContext(ctx,
		"SELECT payload_json FROM blocks WHERE block_id=? AND workspace_id=?",
		recordID, index.workspaceID,
	).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	record, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil {
		return nil, fmt.Errorf("decode canonical record: %w", err)
	}
	if record.ProfileID != index.profileID {
		return nil, nil
	}
	if record.BlockID != recordID || record.WorkspaceID != index.workspaceID {
		return nil, fmt.Errorf("canonical record identity mismatch")
	}
	if record.SchemaVersion != memory.CurrentSchemaVersion {
		return nil, fmt.Errorf("unsupported memory record schema version %d", record.SchemaVersion)
	}
	return &record, nil
}
