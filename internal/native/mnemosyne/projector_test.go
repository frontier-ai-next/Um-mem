package mnemosyne

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
	_ "modernc.org/sqlite"
)

func TestNativeSearchDoesNotLimitBeforeFilteringHistoricalRows(t *testing.T) {
	ctx := context.Background()
	base, _ := boundFixture(t)
	projector := Projector{DataDir: t.TempDir()}
	partitionID, err := PartitionID(base)
	if err != nil {
		t.Fatal(err)
	}
	partition := recallPartition{id: partitionID,
		primaryBlockID: base.BlockMembership.PrimaryBlockID,
		records:        make(map[string]store.ProjectionRow)}
	for i := 0; i < 72; i++ {
		record := base
		record.BlockID = fmt.Sprintf("historical-%03d", i)
		record.RevisionID = fmt.Sprintf("historical-revision-%03d", i)
		record.Content = strings.Repeat("needle ", 8)
		membership := *base.BlockMembership
		membership.RecordID, membership.RecordRevisionID = record.BlockID, record.RevisionID
		membership.MembershipRevisionID = membership.RevisionHash()
		record.BlockMembership = &membership
		if i == 71 {
			record.BlockID, record.RevisionID = "current-record", "current-revision"
			record.Content = "needle"
			membership.RecordID, membership.RecordRevisionID = record.BlockID, record.RevisionID
			membership.MembershipRevisionID = membership.RevisionHash()
		}
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		projection, err := projector.Project(ctx, record, string(payload), int64(i+1))
		if err != nil {
			t.Fatalf("native projection %d failed: %v", i, err)
		}
		if i == 71 {
			partition.records[record.BlockID] = store.ProjectionRow{Record: record,
				NativeID: projection.NativeID, Revision: int64(i + 1), Current: true}
		}
	}
	path, err := projector.partitionPath(base)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ranked, err := db.QueryContext(ctx, `SELECT f.id FROM fts_working f JOIN working_memory w ON w.id=f.id
		WHERE fts_working MATCH ? AND w.session_id=? AND w.source=?
		ORDER BY bm25(fts_working)`, ftsQuery("needle"), partition.id,
		"um_mem:block:"+partition.primaryBlockID)
	if err != nil {
		t.Fatal(err)
	}
	currentRank := 0
	for ranked.Next() {
		var id string
		if err := ranked.Scan(&id); err != nil {
			t.Fatal(err)
		}
		currentRank++
		if id == partition.records["current-record"].NativeID {
			break
		}
	}
	if err := ranked.Err(); err != nil {
		t.Fatal(err)
	}
	ranked.Close()
	if currentRank <= 64 {
		t.Fatalf("test setup did not place current row beyond the old native limit: rank=%d", currentRank)
	}
	rows, err := searchNative(ctx, db, partition, "needle", 64, false)
	if err != nil || len(rows) != 1 || rows[0].ID != partition.records["current-record"].NativeID {
		t.Fatalf("historical native rows displaced the current projection: %+v, %v", rows, err)
	}
}

func boundFixture(t *testing.T) (memory.MemoryRecord, string) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(payload)
	if err != nil {
		t.Fatal(err)
	}
	return record, string(payload)
}

func TestConcurrentNativeProjectionHasOneProviderID(t *testing.T) {
	record, payload := boundFixture(t)
	projector := Projector{DataDir: t.TempDir()}
	const workers = 8
	ids := make([]string, workers)
	errors := make([]error, workers)
	var group sync.WaitGroup
	for i := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := projector.Project(context.Background(), record, payload, 9)
			ids[i], errors[i] = result.NativeID, err
		}()
	}
	group.Wait()
	for i := range workers {
		if errors[i] != nil || ids[i] == "" || ids[i] != ids[0] {
			t.Fatalf("concurrent replay produced multiple native IDs: ids=%+v errors=%+v", ids, errors)
		}
	}
}

func TestNativeProjectionPersistsAndRejectsConflictingReplay(t *testing.T) {
	ctx := context.Background()
	record, payload := boundFixture(t)
	projector := Projector{DataDir: t.TempDir()}
	partition, err := PartitionID(record)
	if err != nil || !strings.HasPrefix(partition, "um-mem:8ed3f6ad685b959e:partition:") {
		t.Fatalf("native session partition differs: %q, %v", partition, err)
	}
	first, err := projector.Project(ctx, record, payload, 7)
	if err != nil || !first.Verified || first.NativeID == "" {
		t.Fatalf("native projection failed: %+v, %v", first, err)
	}
	path, err := projector.partitionPath(record)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM working_memory WHERE source=? AND session_id=?`, source(record), partition).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("native BEAM row is missing: %d, %v", rows, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := projector.Project(ctx, record, payload, 7)
	if err != nil || !second.Verified || second.NativeID != first.NativeID {
		t.Fatalf("replay duplicated native row: %+v, %v", second, err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE working_memory SET content='forged' WHERE id=?`, first.NativeID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(ctx, record, payload, 7); err == nil || !strings.Contains(err.Error(), "content differs") {
		t.Fatalf("tampered native row passed verification: %v", err)
	}
}

func TestReadsPythonMnemosyneNativePartition(t *testing.T) {
	record, _ := boundFixture(t)
	db, err := sql.Open("sqlite", filepath.Join("testdata", "python_beam.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := readNativeRows(context.Background(), db, source(record))
	if err != nil || len(rows) != 1 {
		t.Fatalf("Python BEAM source is missing: %+v, %v", rows, err)
	}
	partition, err := PartitionID(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNative(rows[0], record, 7, partition); err != nil {
		t.Fatalf("Python native row differs from Go canonical contract: %v", err)
	}
}

func TestPythonNativePartitionReplaysGoProjection(t *testing.T) {
	ctx := context.Background()
	record, payload := boundFixture(t)
	root := t.TempDir()
	projector := Projector{DataDir: filepath.Join(root, "native")}
	path, err := projector.partitionPath(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("testdata", "python_beam.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := projector.Project(ctx, record, payload, 7)
	if err != nil || first.NativeID != "7264bf599bd3b26b" {
		t.Fatalf("Go did not recognize Python's existing projection: %+v, %v", first, err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM working_memory WHERE source=?`, source(record)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay duplicated Python native row: %d, %v", count, err)
	}
}

func TestGoProjectionFixtureRetainsProviderReadableRow(t *testing.T) {
	record, _ := boundFixture(t)
	path := filepath.Join("testdata", "go_probe", "partitions",
		"e6e07eb1c0c8f3eec5cb112b5b4e8f0c6efe39565267308dda7dc6d44632cdbd", "mnemosyne.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := readNativeRows(context.Background(), db, source(record))
	if err != nil || len(rows) != 1 || rows[0].ID != "8732bd3e6d301b45" {
		t.Fatalf("Go-written BEAM fixture changed: %+v, %v", rows, err)
	}
	partition, err := PartitionID(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNative(rows[0], record, 7, partition); err != nil {
		t.Fatal(err)
	}
}
