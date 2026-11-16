package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestPageEdgesPaginatesBothDirectionsAndPinsRevision(t *testing.T) {
	ctx := context.Background()
	opened, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	rootID := states[0].Block.BlockID
	ids := []string{"edge-a", "edge-b", "edge-c"}
	for _, id := range ids {
		if _, err := opened.CreateBlock(ctx, leafBlock(id, rootID),
			CreateOptions{Actor: "test", Reason: "edge fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		src, dst := rootID, id
		if i == 1 {
			src, dst = id, rootID
		}
		if _, err := opened.AddBlockEdge(ctx, src, dst, "related_to", "fixture", "test"); err != nil {
			t.Fatal(err)
		}
	}
	first, err := opened.PageEdges(ctx, rootID, 1, "", nil)
	if err != nil || first.Total != 3 || len(first.Edges) != 1 || first.NextCursor == "" {
		t.Fatalf("invalid first edge page: %+v, %v", first, err)
	}
	second, err := opened.PageEdges(ctx, rootID, 1, first.NextCursor, &first.CatalogRevision)
	if err != nil || len(second.Edges) != 1 || second.NextCursor == "" ||
		second.Edges[0].EdgeID == first.Edges[0].EdgeID || second.Edges[0].SrcID != ids[1] {
		t.Fatalf("incoming edge was not paged: %+v, %v", second, err)
	}
	third, err := opened.PageEdges(ctx, rootID, 1, second.NextCursor, nil)
	if err != nil || len(third.Edges) != 1 || third.NextCursor != "" ||
		third.Edges[0].EdgeID == second.Edges[0].EdgeID {
		t.Fatalf("last edge page is invalid: %+v, %v", third, err)
	}
	if _, err := opened.PageEdges(ctx, ids[0], 1, first.NextCursor, nil); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("foreign block cursor was accepted: %v", err)
	}
	if _, err := opened.PageEdges(ctx, rootID, 0, "", nil); err == nil {
		t.Fatal("zero page limit was accepted")
	}
	if _, err := opened.AddBlockEdge(ctx, ids[0], ids[2], "supports", "revision change", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.PageEdges(ctx, rootID, 1, first.NextCursor, nil); err == nil || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale graph cursor was accepted: %v", err)
	}
	if _, err := opened.PageEdges(ctx, rootID, 1, "", &first.CatalogRevision); err == nil || !strings.Contains(err.Error(), "catalog changed") {
		t.Fatalf("stale expected catalog revision was accepted: %v", err)
	}
}
