package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func leafBlock(id, parentID string) memory.Block {
	return memory.Block{
		BlockID: id, BlockType: "semantic_leaf", Temperature: "warm",
		Scope: "workspace", Authority: "advisory", DeliveryPolicy: "on_demand",
		Retention: "durable", Name: id, ParentID: parentID, AgentWritable: true,
		Owner: "test", Producer: "test", Consumers: []string{"memory_agent"},
		RecallPath: "hybrid_recall", LifecyclePolicy: map[string]any{
			"initial_status": "active", "mutable": true, "terminal_statuses": []string{"archived"},
		}, IsolationPolicy: map[string]any{"dimension": "workspace", "cross_workspace": false},
		TokenBudget: 128,
	}
}

// Fixture produced by Python WorkspaceCatalogStore version 2.
func TestOpenPythonV2CatalogAndExtend(t *testing.T) {
	repo := t.TempDir()
	path, err := Path(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/python_v2.sqlite")
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
	rows, err := store.ListBlocks(ctx, false)
	if err != nil || len(rows) != 2 || rows[1].Block.BlockID != "python-leaf" ||
		rows[1].BackendPartitionID != "python-partition" {
		t.Fatalf("Go could not read Python catalog: %+v, %v", rows, err)
	}
	rootID := rows[0].Block.BlockID
	_, err = store.CreateBlock(ctx, leafBlock("go-after-python", rootID), CreateOptions{
		BackendPartitionID: "go-partition", Actor: "test", Reason: "cross-language append",
	})
	if err != nil {
		t.Fatalf("Go could not append to Python catalog: %v", err)
	}
	if revision, err := store.CatalogRevision(ctx); err != nil || revision != 3 {
		t.Fatalf("cross-language catalog revision differs: %d, %v", revision, err)
	}
}

func TestCatalogCreatesRootAndPersistsScopedLeaf(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	store, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListBlocks(ctx, false)
	if err != nil || len(rows) != 1 || !rows[0].Protected || !rows[0].Block.IndexOnly || rows[0].Revision != 1 {
		t.Fatalf("catalog root differs from Python contract: %+v, %v", rows, err)
	}
	rootID := rows[0].Block.BlockID
	state, err := store.CreateBlock(ctx, leafBlock("leaf-one", rootID), CreateOptions{
		BackendPartitionID: "partition-one", Actor: "test", Reason: "create leaf",
	})
	if err != nil || state.Revision != 1 || state.Block.ParentID != rootID || state.Protected {
		t.Fatalf("catalog leaf creation failed: %+v, %v", state, err)
	}
	if _, err := store.CreateBlock(ctx, leafBlock("leaf-one", rootID), CreateOptions{Actor: "test", Reason: "duplicate"}); err == nil {
		t.Fatal("duplicate catalog block was accepted")
	}
	if revision, err := store.CatalogRevision(ctx); err != nil || revision != 2 {
		t.Fatalf("catalog mutation revision differs: %d, %v", revision, err)
	}
	path := store.FilePath()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(repo, " alpha ", " team ")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.FilePath() != path {
		t.Fatal("catalog profile path changed after restart")
	}
	rows, err = reopened.ListBlocks(ctx, false)
	if err != nil || len(rows) != 2 || rows[0].Block.BlockID != rootID || rows[1].Block.BlockID != "leaf-one" {
		t.Fatalf("catalog did not survive restart: %+v, %v", rows, err)
	}
	other, err := Open(repo, "alpha", "other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherRows, err := other.ListBlocks(ctx, false)
	if err != nil || len(otherRows) != 1 || otherRows[0].Block.BlockID == rootID {
		t.Fatalf("profile catalog was not isolated: %+v, %v", otherRows, err)
	}
}

func TestCatalogConcurrentAppendsAllocateUniqueRevisions(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	initial, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := initial.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	rootID := rows[0].Block.BlockID
	initial.Close()
	var group sync.WaitGroup
	errors := make(chan error, 8)
	for i := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			store, err := Open(repo, "alpha", "team")
			if err != nil {
				errors <- err
				return
			}
			defer store.Close()
			id := fmt.Sprintf("leaf-%d", i)
			_, err = store.CreateBlock(ctx, leafBlock(id, rootID), CreateOptions{
				BackendPartitionID: "partition-" + id, Actor: "test", Reason: "concurrent create",
			})
			if err != nil {
				errors <- err
			}
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	reopened, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if revision, err := reopened.CatalogRevision(ctx); err != nil || revision != 9 {
		t.Fatalf("concurrent catalog revision differs: %d, %v", revision, err)
	}
	rows, err = reopened.ListBlocks(ctx, false)
	if err != nil || len(rows) != 9 {
		t.Fatalf("concurrent catalog lost blocks: %d, %v", len(rows), err)
	}
}

func TestCatalogReplaceUsesBlockRevisionAndProtectsRoot(t *testing.T) {
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
	root := rows[0]
	leaf, err := store.CreateBlock(ctx, leafBlock("leaf", root.Block.BlockID), CreateOptions{
		BackendPartitionID: "partition", Actor: "test", Reason: "create leaf",
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := leaf.Block
	changed.Name = "Updated leaf"
	updated, err := store.ReplaceBlock(ctx, leaf, changed, ReplaceOptions{
		ExpectedRevision: 1, Actor: "test", Reason: "rename leaf", Operation: "amend_block",
	})
	if err != nil || updated.Revision != 2 || updated.Block.Name != changed.Name {
		t.Fatalf("catalog amendment failed: %+v, %v", updated, err)
	}
	if _, err := store.ReplaceBlock(ctx, leaf, changed, ReplaceOptions{
		ExpectedRevision: 1, Actor: "test", Reason: "stale rename", Operation: "amend_block",
	}); err == nil {
		t.Fatal("stale catalog revision was accepted")
	}
	if _, err := store.ReplaceBlock(ctx, root, root.Block, ReplaceOptions{
		ExpectedRevision: 1, Actor: "test", Reason: "edit root", Operation: "amend_block",
	}); err == nil {
		t.Fatal("protected root was amended")
	}
	if revision, err := store.CatalogRevision(ctx); err != nil || revision != 3 {
		t.Fatalf("failed replacements advanced catalog revision: %d, %v", revision, err)
	}
}

func TestCatalogNavigationPagesAndRejectsStaleCursor(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	roots, err := store.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	rootID := roots[0].Block.BlockID
	branch := leafBlock("branch", rootID)
	branch.IndexOnly = true
	branch.AgentWritable = false
	if _, err := store.CreateBlock(ctx, branch, CreateOptions{Actor: "test", Reason: "create branch"}); err != nil {
		t.Fatal(err)
	}
	var first BlockState
	for i := range 5 {
		id := fmt.Sprintf("topic-%d", i)
		topic := leafBlock(id, branch.BlockID)
		if i == 0 {
			topic.IndexOnly, topic.AgentWritable = true, false
		}
		state, err := store.CreateBlock(ctx, topic, CreateOptions{Actor: "test", Reason: "create topic"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = state
		}
	}
	if _, err := store.CreateBlock(ctx, leafBlock("evidence", first.Block.BlockID), CreateOptions{Actor: "test", Reason: "create evidence"}); err != nil {
		t.Fatal(err)
	}
	edge, err := store.AddBlockEdge(ctx, branch.BlockID, first.Block.BlockID,
		"related_to", "explicit relation", "test")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.Navigate(ctx, NavigateOptions{BlockID: branch.BlockID, Depth: 1,
		ChildLimit: 2, IncludeEdges: true})
	if err != nil || len(preview.Edges[branch.BlockID]) != 1 || preview.Edges[branch.BlockID][0].EdgeID != edge.EdgeID {
		t.Fatalf("catalog edge preview differs: %+v, %v", preview.Edges, err)
	}
	if _, err := store.AddBlockEdge(ctx, branch.BlockID, first.Block.BlockID,
		"related_to", "duplicate relation", "test"); err == nil {
		t.Fatal("duplicate block edge was accepted")
	}
	if _, err := store.RemoveBlockEdge(ctx, edge.EdgeID, 2, "remove relation", "test"); err == nil {
		t.Fatal("stale block edge removal was accepted")
	}
	if _, err := store.RemoveBlockEdge(ctx, edge.EdgeID, 1, "remove relation", "test"); err != nil {
		t.Fatal(err)
	}
	preview, err = store.Navigate(ctx, NavigateOptions{BlockID: branch.BlockID, Depth: 1,
		ChildLimit: 2, IncludeEdges: true})
	if err != nil || len(preview.Edges[branch.BlockID]) != 0 {
		t.Fatalf("removed catalog edge remained visible: %+v, %v", preview.Edges, err)
	}
	seen := make(map[string]bool)
	cursor := ""
	for page := 0; page < 3; page++ {
		nav, err := store.Navigate(ctx, NavigateOptions{BlockID: branch.BlockID, Depth: 2, ChildLimit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(nav.PathIDs) != 2 || nav.PathIDs[0] != rootID || nav.PathIDs[1] != branch.BlockID ||
			nav.ChildCounts[branch.BlockID] != 5 || len(nav.ChildIDs[branch.BlockID]) > 2 {
			t.Fatalf("bounded catalog navigation differs: %+v", nav)
		}
		for _, id := range nav.ChildIDs[branch.BlockID] {
			seen[id] = true
		}
		cursor = nav.NextCursors[branch.BlockID]
		if page < 2 && cursor == "" {
			t.Fatal("catalog page omitted continuation cursor")
		}
	}
	if len(seen) != 5 || cursor != "" {
		t.Fatalf("catalog pagination lost or duplicated children: %v, %q", seen, cursor)
	}
	firstPage, err := store.Navigate(ctx, NavigateOptions{BlockID: branch.BlockID, Depth: 1, ChildLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	stale := firstPage.NextCursors[branch.BlockID]
	if _, err := store.Navigate(ctx, NavigateOptions{BlockID: rootID, Cursor: stale}); err == nil {
		t.Fatal("navigation cursor crossed branches")
	}
	changed := first.Block
	changed.Name = "Corrected topic"
	if _, err := store.ReplaceBlock(ctx, first, changed, ReplaceOptions{
		ExpectedRevision: 1, Actor: "test", Reason: "rename topic", Operation: "amend_block",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Navigate(ctx, NavigateOptions{BlockID: branch.BlockID, Cursor: stale}); err == nil {
		t.Fatal("stale catalog navigation cursor was accepted")
	}
}

func TestNavigationCursorMatchesPythonFormat(t *testing.T) {
	got, err := NavigationCursor("alpha", "team", "branch", "8", "2024-01-01T00:00:00Z", "topic-2")
	if err != nil {
		t.Fatal(err)
	}
	const want = "WyJhbHBoYSIsInRlYW0iLCJicmFuY2giLCI4IiwiMjAyNC0wMS0wMVQwMDowMDowMFoiLCJ0b3BpYy0yIl0"
	if got != want {
		t.Fatalf("catalog navigation cursor differs from Python: %q", got)
	}
}
