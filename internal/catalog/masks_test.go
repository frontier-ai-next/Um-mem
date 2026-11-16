package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// The JSON and SQLite catalog were produced by Python ActiveMemoryMask and
// WorkspaceCatalogStore.put_mask(). Go reads and advances the same row.
func TestPythonMaskFixtureInPythonCatalog(t *testing.T) {
	encoded, err := os.ReadFile("testdata/python_mask_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var mask memory.ActiveMemoryMask
	if err := json.Unmarshal(encoded, &mask); err != nil {
		t.Fatal(err)
	}
	if err := mask.Normalize(); err != nil {
		t.Fatal(err)
	}
	got, err := canonical(mask)
	if err != nil || strings.TrimSpace(string(encoded)) != got {
		t.Fatalf("Python mask payload changed during Go roundtrip: %v", err)
	}
	repo := t.TempDir()
	path, err := Path(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/python_mask_v2.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.ValidateMaskBlocks(ctx, mask); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetMask(ctx, mask.MaskID)
	if err != nil || stored == nil || stored.Selectors[0].RecordIDs[0] != "record-1" {
		t.Fatalf("Python-stored mask was not read by Go: %+v, %v", stored, err)
	}
	advanced, err := store.TransitionMask(ctx, mask.MaskID, 1, "suspended", "Go continuation")
	if err != nil || advanced.Revision != 2 {
		t.Fatalf("Go could not advance Python mask: %+v, %v", advanced, err)
	}
}

func maskForTest(blockID string) memory.ActiveMemoryMask {
	return memory.ActiveMemoryMask{
		MaskID: "7b1b2c34-9c8c-4b63-8d8d-349917d259ab", WorkspaceID: "alpha", ProfileID: "team",
		SessionID: "session-1", TaskID: "task-1", Revision: 1, Status: "active",
		SelectedBlockIDs: []string{blockID}, TokenBudget: 512, ActivationReason: "focused task",
		Lifecycle: "durable", CreatedAt: "2026-10-23T10:00:00+00:00", UpdatedAt: "2026-10-23T10:00:00+00:00",
		Selectors: []memory.ActiveMemorySelector{{BlockID: blockID, SelectorType: "canonical_heads", Limit: 8}},
	}
}

func TestMaskPersistenceCASAndLiveReferences(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	store, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	rootID := rows[0].Block.BlockID
	_, err = store.CreateBlock(ctx, leafBlock("mask-leaf", rootID), CreateOptions{Actor: "test", Reason: "mask test"})
	if err != nil {
		t.Fatal(err)
	}
	mask := maskForTest("mask-leaf")
	if err := store.ValidateMaskBlocks(ctx, mask); err != nil {
		t.Fatal(err)
	}
	created, err := store.PutMask(ctx, mask, nil)
	if err != nil || len(created.ContextRefs) != 0 || created.ContextRefs == nil {
		t.Fatalf("mask creation or empty list representation failed: %+v, %v", created, err)
	}
	selectedState, err := store.GetBlock(ctx, "mask-leaf")
	if err != nil || selectedState == nil {
		t.Fatalf("selected block is unavailable: %+v, %v", selectedState, err)
	}
	if _, err := store.ReplaceBlock(ctx, *selectedState, selectedState.Block, ReplaceOptions{
		ExpectedRevision: selectedState.Revision, Status: "archived", Actor: "test",
		Reason: "try archive selected block", Operation: "archive_block"}); err == nil {
		t.Fatal("live active mask did not prevent block archival")
	}
	if _, err := store.PutMask(ctx, mask, nil); err == nil {
		t.Fatal("duplicate mask was accepted")
	}
	if revision, err := store.CatalogRevision(ctx); err != nil || revision != 2 {
		t.Fatalf("mask unexpectedly changed topology revision: %d, %v", revision, err)
	}
	current, err := store.CurrentMask(ctx, "session-1", "task-1")
	if err != nil || current == nil || current.MaskID != mask.MaskID {
		t.Fatalf("current mask differs: %+v, %v", current, err)
	}
	if live, err := store.HasLiveMaskForScope(ctx, "session-1", "task-1", ""); err != nil || !live {
		t.Fatalf("live mask was not found: %v, %v", live, err)
	}
	if live, err := store.HasLiveMaskForScope(ctx, "session-1", "task-1", mask.MaskID); err != nil || live {
		t.Fatalf("excluded live mask was counted: %v, %v", live, err)
	}
	if referenced, err := store.BlockHasActiveMaskReference(ctx, "mask-leaf"); err != nil || !referenced {
		t.Fatalf("live block reference missing: %v, %v", referenced, err)
	}
	stale := 0
	mask.Revision = 2
	mask.Status = "suspended"
	mask.UpdatedAt = "2026-10-23T10:01:00+00:00"
	if _, err := store.PutMask(ctx, mask, &stale); err == nil {
		t.Fatal("stale mask revision was accepted")
	}
	expected := 1
	if _, err := store.PutMask(ctx, mask, &expected); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stored, err := reopened.GetMask(ctx, mask.MaskID)
	if err != nil || stored == nil || stored.Revision != 2 || stored.Status != "suspended" {
		t.Fatalf("mask did not survive restart: %+v, %v", stored, err)
	}
	mask.Revision = 3
	mask.Status = "expired"
	mask.UpdatedAt = "2026-10-23T10:02:00+00:00"
	expected = 2
	if _, err := reopened.PutMask(ctx, mask, &expected); err != nil {
		t.Fatal(err)
	}
	if live, err := reopened.HasLiveMaskForScope(ctx, "session-1", "task-1", ""); err != nil || live {
		t.Fatalf("expired mask remained live: %v, %v", live, err)
	}
	if referenced, err := reopened.BlockHasActiveMaskReference(ctx, "mask-leaf"); err != nil || referenced {
		t.Fatalf("expired mask still protects block: %v, %v", referenced, err)
	}
}

func TestMaskContractValidationAndElapsedDeadline(t *testing.T) {
	mask := maskForTest("leaf")
	mask.ExpiresAt = "2020-01-01T00:00:00Z"
	if err := mask.Normalize(); err != nil || !mask.IsExpired(time.Now().UTC()) {
		t.Fatalf("elapsed mask contract failed: %v", err)
	}
	mask.ExpiresAt = "2026-10-23T10:00:00"
	if err := mask.Normalize(); err == nil {
		t.Fatal("naive deadline was accepted")
	}
	mask = maskForTest("leaf")
	mask.Selectors[0].BlockID = "outside"
	if err := mask.Normalize(); err == nil {
		t.Fatal("selector escaped selected blocks")
	}
	mask = maskForTest("leaf")
	mask.Selectors[0].SelectorType = "backend_query"
	if err := mask.Normalize(); err == nil {
		t.Fatal("backend query without query was accepted")
	}
}

func TestMaskPublicActivationAndTransitions(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateBlock(ctx, leafBlock("mask-leaf", rows[0].Block.BlockID),
		CreateOptions{Actor: "test", Reason: "mask test"})
	if err != nil {
		t.Fatal(err)
	}
	first := maskForTest("mask-leaf")
	first.MaskID = ""
	first.CreatedAt, first.UpdatedAt = "", ""
	first, err = store.ActivateMask(ctx, first)
	if err != nil || first.MaskID == "" || first.CatalogRevision != 2 {
		t.Fatalf("mask activation failed: %+v, %v", first, err)
	}
	duplicate := maskForTest("mask-leaf")
	if _, err := store.ActivateMask(ctx, duplicate); err == nil {
		t.Fatal("second live mask was accepted for exact scope")
	}
	suspended, err := store.TransitionMask(ctx, first.MaskID, 1, "suspended", "pause")
	if err != nil || suspended.Revision != 2 || suspended.Status != "suspended" {
		t.Fatalf("suspend failed: %+v, %v", suspended, err)
	}
	if _, err := store.ActivateMask(ctx, duplicate); err == nil {
		t.Fatal("suspended mask did not reserve its scope")
	}
	if _, err := store.TransitionMask(ctx, first.MaskID, 1, "active", "stale"); err == nil {
		t.Fatal("stale resume was accepted")
	}
	resumed, err := store.TransitionMask(ctx, first.MaskID, 2, "active", "resume")
	if err != nil || resumed.Revision != 3 || resumed.Status != "active" {
		t.Fatalf("resume failed: %+v, %v", resumed, err)
	}
	deactivated, err := store.TransitionMask(ctx, first.MaskID, 3, "expired", "done")
	if err != nil || deactivated.Status != "expired" {
		t.Fatalf("deactivation failed: %+v, %v", deactivated, err)
	}
	if _, err := store.TransitionMask(ctx, first.MaskID, 4, "active", "invalid"); err == nil {
		t.Fatal("expired mask was resumed")
	}
	if _, err := store.ActivateMask(ctx, duplicate); err != nil {
		t.Fatalf("deactivated scope was not reusable: %v", err)
	}
	if _, err := store.TransitionMask(ctx, duplicate.MaskID, 1, "expired", "release block"); err != nil {
		t.Fatalf("replacement mask did not release its selected block: %v", err)
	}
	archived, err := store.GetBlock(ctx, "mask-leaf")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReplaceBlock(ctx, *archived, archived.Block, ReplaceOptions{
		ExpectedRevision: archived.Revision, Status: "archived", Actor: "test",
		Reason: "test archive", Operation: "archive_block",
	})
	if err != nil {
		t.Fatal(err)
	}
	third := maskForTest("mask-leaf")
	third.MaskID = "05bddb9b-42ab-4882-9c46-53500924b96f"
	third.SessionID = "another-session"
	if _, err := store.ActivateMask(ctx, third); err == nil {
		t.Fatal("archived block was selected")
	}
}

func TestConcurrentMaskActivationKeepsOneLiveScope(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateBlock(ctx, leafBlock("mask-leaf", rows[0].Block.BlockID),
		CreateOptions{Actor: "test", Reason: "mask test"})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			mask := maskForTest("mask-leaf")
			mask.MaskID = ""
			_, err := store.ActivateMask(ctx, mask)
			results <- err
		}()
	}
	workers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly one live mask under contention, got %d", succeeded)
	}
}
