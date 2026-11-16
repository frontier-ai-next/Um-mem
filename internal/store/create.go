package store

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"database/sql"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// CreateIndex initializes a new canonical store. Existing stores must be opened
// through an explicit recovery path rather than silently reinitialized.
func CreateIndex(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(absPath); err == nil {
		return fmt.Errorf("canonical index already exists: %s", absPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o700); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;"); err != nil {
		return err
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("create canonical schema: %w", err)
	}
	if _, err := db.Exec("INSERT INTO schema_metadata(key, value) VALUES('schema_version', ?)", IndexSchemaVersion); err != nil {
		return err
	}
	return nil
}
