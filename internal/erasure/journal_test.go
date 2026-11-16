package erasure

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestEraseJournalReplaysAndStoresOnlyHashes(t *testing.T) {
	repo := t.TempDir()
	journal, err := OpenJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	ctx := context.Background()
	workspace := strings.Repeat("a", 64)
	request := strings.Repeat("b", 64)
	fingerprint := strings.Repeat("c", 64)
	target := strings.Repeat("d", 64)
	first, created, err := journal.OpenRequest(ctx, workspace, request, fingerprint, target, "mempalace")
	if err != nil || !created || first.Status != "requested" {
		t.Fatalf("new erase state changed: %+v %v %v", first, created, err)
	}
	replayed, created, err := journal.OpenRequest(ctx, workspace, request, fingerprint, target, "mempalace")
	if err != nil || created || replayed != first {
		t.Fatalf("erase request did not replay: %+v %v %v", replayed, created, err)
	}
	if _, _, err := journal.OpenRequest(ctx, workspace, request, strings.Repeat("e", 64), target, "mempalace"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed fingerprint was accepted: %v", err)
	}
	completed, err := journal.RecordOutcome(ctx, first, "completed", "")
	if err != nil || completed.Status != "completed" || completed.CreatedAt != first.CreatedAt {
		t.Fatalf("erase outcome changed identity: %+v %v", completed, err)
	}
	replayed, created, err = journal.OpenRequest(ctx, workspace, request, fingerprint, target, "mempalace")
	if err != nil || created || replayed.Status != "completed" {
		t.Fatalf("completed request did not replay: %+v %v %v", replayed, created, err)
	}
	if _, err := journal.RecordOutcome(ctx, first, "purging", "native_exact_id_not_found"); !errors.Is(err, ErrRequestUnavailable) {
		t.Fatalf("stale worker downgraded a completed erase: %v", err)
	}
	if erased, err := journal.HasCompletedTarget(ctx, workspace, []string{target}); err != nil || !erased {
		t.Fatalf("completed target tombstone disappeared: %v %v", erased, err)
	}
	invalid := first
	invalid.FingerprintSHA256 = strings.Repeat("f", 64)
	if _, err := journal.RecordOutcome(ctx, invalid, "completed", ""); !errors.Is(err, ErrRequestUnavailable) {
		t.Fatalf("stale erase fingerprint was accepted: %v", err)
	}
	db, err := sql.Open("sqlite", JournalPath(repo))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var actualTarget string
	if err := db.QueryRow(`SELECT target_id_sha256 FROM erase_requests`).Scan(&actualTarget); err != nil || actualTarget != target {
		t.Fatalf("erase journal lost target hash: %q %v", actualTarget, err)
	}
	var columns string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='erase_requests'`).Scan(&columns); err != nil ||
		strings.Contains(columns, "target_id TEXT") || strings.Contains(columns, "request_id TEXT") {
		t.Fatalf("erase journal schema can retain plaintext identifiers: %q %v", columns, err)
	}
}
