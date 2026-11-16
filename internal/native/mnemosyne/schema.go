package mnemosyne

import (
	"context"
	"database/sql"
)

// These are the BEAM tables and triggers that UM-MEM's native projection
// writes or queries. Existing provider databases keep their additional tables
// and columns; CREATE IF NOT EXISTS never replaces them.
var schemaStatements = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA busy_timeout=5000`,
	`CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY, content TEXT NOT NULL, source TEXT, timestamp TEXT,
		session_id TEXT DEFAULT 'default', importance REAL DEFAULT 0.5,
		metadata_json TEXT, created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS working_memory (
		id TEXT PRIMARY KEY, content TEXT NOT NULL, source TEXT, timestamp TEXT,
		session_id TEXT DEFAULT 'default', importance REAL DEFAULT 0.5,
		metadata_json TEXT, veracity TEXT DEFAULT 'unknown',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		memory_type TEXT DEFAULT 'unknown', consolidated_at TEXT,
		consolidation_claimed_at TEXT, recall_count INTEGER DEFAULT 0,
		last_recalled TIMESTAMP, pinned INTEGER DEFAULT 0, valid_until TIMESTAMP,
		superseded_by TEXT, scope TEXT DEFAULT 'global', author_id TEXT,
		author_type TEXT, channel_id TEXT, trust_tier TEXT DEFAULT 'STATED',
		validator TEXT, validated_at TIMESTAMP, validation_count INTEGER DEFAULT 0,
		event_date TEXT, event_date_precision TEXT DEFAULT 'unknown',
		temporal_tags TEXT DEFAULT '[]', corrected_by INTEGER
	)`,
	`CREATE TABLE IF NOT EXISTS episodic_memory (
		rowid INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL,
		content TEXT NOT NULL, source TEXT, timestamp TEXT,
		session_id TEXT DEFAULT 'default', importance REAL DEFAULT 0.5,
		metadata_json TEXT, summary_of TEXT DEFAULT '', veracity TEXT DEFAULT 'unknown',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, valid_until TIMESTAMP,
		superseded_by TEXT, scope TEXT DEFAULT 'global'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_wm_session ON working_memory(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_wm_source ON working_memory(source)`,
	`CREATE INDEX IF NOT EXISTS idx_em_session ON episodic_memory(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_em_source ON episodic_memory(source)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS fts_working USING fts5(id UNINDEXED, content)`,
	`CREATE TRIGGER IF NOT EXISTS wm_ai AFTER INSERT ON working_memory BEGIN
		INSERT INTO fts_working(id,content) VALUES (new.id,new.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS wm_ad AFTER DELETE ON working_memory BEGIN
		DELETE FROM fts_working WHERE id=old.id;
	END`,
	`CREATE TRIGGER IF NOT EXISTS wm_au AFTER UPDATE ON working_memory BEGIN
		DELETE FROM fts_working WHERE id=old.id;
		INSERT INTO fts_working(id,content) VALUES (new.id,new.content);
	END`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS fts_episodes USING fts5(
		content, content='episodic_memory', content_rowid='rowid'
	)`,
	`CREATE TRIGGER IF NOT EXISTS em_ai AFTER INSERT ON episodic_memory BEGIN
		INSERT INTO fts_episodes(rowid,content) VALUES (new.rowid,new.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS em_ad AFTER DELETE ON episodic_memory BEGIN
		INSERT INTO fts_episodes(fts_episodes,rowid,content) VALUES ('delete',old.rowid,old.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS em_au AFTER UPDATE ON episodic_memory BEGIN
		INSERT INTO fts_episodes(fts_episodes,rowid,content) VALUES ('delete',old.rowid,old.content);
		INSERT INTO fts_episodes(rowid,content) VALUES (new.rowid,new.content);
	END`,
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	for _, statement := range schemaStatements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
