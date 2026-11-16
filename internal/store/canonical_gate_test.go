package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestCanonicalWriteGateSerializesAcrossWriterHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	first, err := OpenWriter(path, "alpha", "team", "mnemosyne")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenWriter(path, "alpha", "team", "mnemosyne")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- first.WithCanonicalWriteGate(context.Background(), func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("first canonical gate did not enter")
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := second.WithCanonicalWriteGate(blockedCtx, func() error {
		t.Fatal("second writer entered an occupied canonical gate")
		return nil
	}); err == nil {
		close(release)
		t.Fatal("second writer crossed an occupied canonical gate")
	}
	raw, err := sql.Open("sqlite", sqliteFileURI(path, "rw"))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1)
	if _, err := raw.ExecContext(context.Background(), "PRAGMA busy_timeout=50"); err != nil {
		close(release)
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err == nil {
		raw.ExecContext(context.Background(), "ROLLBACK")
		close(release)
		t.Fatal("SQLite writer crossed an occupied canonical gate without the file lock")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("first canonical gate failed to release: %v", err)
	}
	if err := second.WithCanonicalWriteGate(context.Background(), func() error { return nil }); err != nil {
		t.Fatalf("second writer could not enter after release: %v", err)
	}
}
