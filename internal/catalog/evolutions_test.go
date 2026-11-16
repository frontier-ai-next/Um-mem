package catalog

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestPrepareTopologyEvolutionReservesSourcesAndReplaysExactMapping(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	payload := func(operation string, sources ...string) map[string]any {
		claims := make(map[string]any, len(sources))
		for _, source := range sources {
			claims[source] = 3
		}
		return map[string]any{"operation": operation, "sources": claims,
			"targets":     []any{map[string]any{"block_id": "target"}},
			"assignments": map[string]any{"record": "target"}}
	}
	first, err := selected.PrepareTopologyEvolution(ctx, " first ", payload("split", "source-a"))
	if err != nil || first.OperationID != "first" || first.State != "prepared" {
		t.Fatalf("source reservation failed: %+v, %v", first, err)
	}
	read, err := selected.TopologyEvolution(ctx, "first")
	if err != nil || read == nil || !reflect.DeepEqual(first, *read) {
		t.Fatalf("prepared mapping was not persisted exactly: %+v, %v", read, err)
	}
	replay, err := selected.PrepareTopologyEvolution(ctx, "first", payload("split", "source-a"))
	if err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("exact operation replay failed: %+v, %v", replay, err)
	}
	if _, err := selected.PrepareTopologyEvolution(ctx, "first", payload("merge", "source-a")); err == nil {
		t.Fatal("same operation ID accepted a different mapping")
	}
	if _, err := selected.PrepareTopologyEvolution(ctx, "second", payload("merge", "source-a")); err == nil {
		t.Fatal("a pending generation did not reserve its source block")
	}
	if _, err := selected.PrepareTopologyEvolution(ctx, "independent", payload("split", "source-b")); err != nil {
		t.Fatalf("independent source claim failed: %v", err)
	}
	if err := selected.MarkTopologyProjectionsReady(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if err := selected.MarkTopologyProjectionsReady(ctx, "first"); err != nil {
		t.Fatalf("ready replay failed: %v", err)
	}
	ready, err := selected.TopologyEvolution(ctx, "first")
	if err != nil || ready == nil || ready.State != "projections_ready" ||
		!reflect.DeepEqual(ready.Payload, first.Payload) {
		t.Fatalf("ready transition changed mapping: %+v, %v", ready, err)
	}
	if _, err := selected.PrepareTopologyEvolution(ctx, "second", payload("merge", "source-a")); err == nil {
		t.Fatal("ready generation released its source block")
	}
	if _, err := selected.AbortTopologyEvolution(ctx, "first", "projection cannot publish"); err != nil {
		t.Fatal(err)
	}
	if err := selected.MarkTopologyProjectionsReady(ctx, "first"); err != nil {
		t.Fatalf("aborted ready replay failed: %v", err)
	}
	if _, err := selected.PrepareTopologyEvolution(ctx, "second", payload("merge", "source-a")); err != nil {
		t.Fatalf("aborted source claim was not released: %v", err)
	}
	if _, err := selected.TopologyEvolution(ctx, "unknown"); err != nil {
		t.Fatal(err)
	}
	if err := selected.MarkTopologyProjectionsReady(ctx, "unknown"); err == nil {
		t.Fatal("unknown generation was marked ready")
	}
}

func TestPublishTopologyEvolutionAtomicallySwapsCatalogAndMemberships(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	blocks, err := selected.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	parentID := blocks[0].Block.BlockID
	source, err := selected.CreateBlock(ctx, leafBlock("source", parentID), CreateOptions{
		BackendPartitionID: "source-partition", Actor: "test", Reason: "create source",
	})
	if err != nil {
		t.Fatal(err)
	}
	target := leafBlock("target", parentID)
	target.Name = "Published target"
	payload := map[string]any{
		"operation": "split", "sources": map[string]any{"source": source.Revision},
		"targets": []any{map[string]any{"block": target, "backend_partition_id": "target-partition"}},
		"assignments": map[string]any{"record-one": map[string]any{
			"record_revision_id": "revision-one", "target_block_id": "target"}},
		"reason": "split source by topic", "actor": "test", "request_hash": "request-one",
	}
	if _, err := selected.PrepareTopologyEvolution(ctx, "publish-one", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PublishTopologyEvolution(ctx, "publish-one"); err == nil {
		t.Fatal("unprojected generation was published")
	}
	if err := selected.MarkTopologyProjectionsReady(ctx, "publish-one"); err != nil {
		t.Fatal(err)
	}
	// A previously published ID in another profile must block this operation.
	foreign := map[string]any{"operation": "split", "sources": map[string]any{},
		"targets": []any{}, "assignments": map[string]any{}}
	foreignRaw, err := canonical(foreign)
	if err != nil {
		t.Fatal(err)
	}
	foreignHash, err := fingerprint(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.db.ExecContext(ctx, `INSERT INTO topology_evolutions(
		operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,created_at,updated_at)
		VALUES('foreign-op','alpha','other','published',?,?,?,?)`, foreignRaw, foreignHash,
		"2026-01-01", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.db.ExecContext(ctx, `INSERT INTO published_record_memberships(
		record_id,workspace_id,profile_id,record_revision_id,block_id,
		backend_partition_id,catalog_revision_id,operation_id,published_at)
		VALUES('record-one','alpha','other','foreign-revision','foreign-block',
		'foreign-partition','topology:foreign-op','foreign-op','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	revisionBefore, err := selected.CatalogRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PublishTopologyEvolution(ctx, "publish-one"); err == nil {
		t.Fatal("foreign record membership did not block publication")
	}
	if state, err := selected.GetBlock(ctx, "target"); err != nil || state != nil {
		t.Fatalf("failed publication leaked target block: %+v, %v", state, err)
	}
	if state, err := selected.GetBlock(ctx, "source"); err != nil || state.Status != "active" {
		t.Fatalf("failed publication archived source: %+v, %v", state, err)
	}
	if revision, err := selected.CatalogRevision(ctx); err != nil || revision != revisionBefore {
		t.Fatalf("failed publication advanced catalog: %d, %v", revision, err)
	}
	if foreignPointer, err := selected.PublishedRecordMembership(ctx, "record-one", ""); err != nil || foreignPointer != nil {
		t.Fatalf("another profile's published pointer leaked: %+v, %v", foreignPointer, err)
	}
	if _, err := selected.db.ExecContext(ctx, `DELETE FROM published_record_memberships
		WHERE record_id='record-one'`); err != nil {
		t.Fatal(err)
	}
	published, err := selected.PublishTopologyEvolution(ctx, "publish-one")
	if err != nil || published.State != "published" || published.OperationID != "publish-one" {
		t.Fatalf("ready generation was not published: %+v, %v", published, err)
	}
	again, err := selected.PublishTopologyEvolution(ctx, "publish-one")
	if err != nil || !reflect.DeepEqual(again, published) {
		t.Fatalf("published replay changed journal: %+v, %v", again, err)
	}
	if state, err := selected.GetBlock(ctx, "source"); err != nil || state == nil ||
		state.Status != "archived" || state.Revision != source.Revision+1 {
		t.Fatalf("published source did not archive: %+v, %v", state, err)
	}
	if state, err := selected.GetBlock(ctx, "target"); err != nil || state == nil ||
		state.Status != "active" || state.BackendPartitionID != "target-partition" {
		t.Fatalf("published target is missing: %+v, %v", state, err)
	}
	var blockID, partitionID, revisionID, operationID string
	if err := selected.db.QueryRowContext(ctx, `SELECT block_id,backend_partition_id,
		catalog_revision_id,operation_id FROM published_record_memberships WHERE record_id='record-one'`).Scan(
		&blockID, &partitionID, &revisionID, &operationID); err != nil || blockID != "target" ||
		partitionID != "target-partition" || revisionID != "topology:publish-one" ||
		operationID != "publish-one" {
		t.Fatalf("published record pointer differs: %q %q %q %q, %v",
			blockID, partitionID, revisionID, operationID, err)
	}
	membership, err := selected.PublishedRecordMembership(ctx, "record-one", "revision-one")
	if err != nil || membership == nil || membership.BlockID != "target" ||
		membership.BackendPartitionID != "target-partition" {
		t.Fatalf("published pointer lookup failed: %+v, %v", membership, err)
	}
	if stale, err := selected.PublishedRecordMembership(ctx, "record-one", "revision-two"); err != nil || stale != nil {
		t.Fatalf("stale record revision reused a published pointer: %+v, %v", stale, err)
	}
	byRecord, err := selected.PublishedMembershipsForRecords(ctx, []string{"record-one", "missing"})
	if err != nil || len(byRecord) != 1 || byRecord["record-one"].BlockID != "target" {
		t.Fatalf("published record batch lookup differs: %+v, %v", byRecord, err)
	}
	byBlock, err := selected.PublishedMembershipsForBlocks(ctx, []string{"source", "target", "target"})
	if err != nil || len(byBlock) != 1 || byBlock["record-one"].BlockID != "target" {
		t.Fatalf("published block lookup differs: %+v, %v", byBlock, err)
	}
	if empty, err := selected.PublishedMembershipsForBlocks(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty published block lookup differs: %+v, %v", empty, err)
	}
	partitions, err := selected.PublishedPartitionMemberships(ctx)
	if err != nil || len(partitions) != 1 || partitions[0].BlockID != "target" ||
		partitions[0].BackendPartitionID != "target-partition" ||
		partitions[0].CatalogRevisionID != "topology:"+operationID {
		t.Fatalf("published partition identities differ: %+v, %v", partitions, err)
	}
	stored := memory.MemoryRecord{BlockID: "record-one", RevisionID: "revision-one",
		WorkspaceID: "alpha", ProfileID: "team", BlockMembership: &memory.RecordBlockMembership{
			PrimaryBlockID: "source", BackendPartitionID: "source-partition",
			CatalogRevisionID: "old-catalog", SecondaryBlockIDs: []string{"target", "other"},
			RecordID: "record-one", RecordRevisionID: "revision-one", WorkspaceID: "alpha",
		}}
	effective, err := selected.EffectiveRecord(ctx, stored)
	if err != nil || effective.BlockMembership.PrimaryBlockID != "target" ||
		effective.BlockMembership.CatalogRevisionID != "topology:publish-one" ||
		!reflect.DeepEqual(effective.BlockMembership.SecondaryBlockIDs, []string{"other"}) ||
		effective.BlockMembership.MembershipRevisionID != effective.BlockMembership.RevisionHash() {
		t.Fatalf("published membership was not applied to exact revision: %+v, %v", effective.BlockMembership, err)
	}
	if stored.BlockMembership.PrimaryBlockID != "source" || len(stored.BlockMembership.SecondaryBlockIDs) != 2 {
		t.Fatal("effective record changed immutable stored membership")
	}
	staleRecord := stored
	staleRecord.RevisionID = "revision-two"
	staleEffective, err := selected.EffectiveRecord(ctx, staleRecord)
	if err != nil || staleEffective.BlockMembership.PrimaryBlockID != "source" {
		t.Fatalf("published pointer applied to another record revision: %+v, %v", staleEffective, err)
	}
	batch, err := selected.EffectiveRecords(ctx, []memory.MemoryRecord{stored, staleRecord})
	if err != nil || len(batch) != 2 || batch[0].BlockMembership.PrimaryBlockID != "target" ||
		batch[1].BlockMembership.PrimaryBlockID != "source" {
		t.Fatalf("catalog snapshot did not preserve exact revision overlays: %+v, %v", batch, err)
	}
	var edgeCount int
	if err := selected.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM topology_edges WHERE
		src_id='target' AND dst_id='source' AND edge_type='derived_from'`).Scan(&edgeCount); err != nil || edgeCount != 1 {
		t.Fatalf("source provenance edge differs: %d, %v", edgeCount, err)
	}
	if revision, err := selected.CatalogRevision(ctx); err != nil || revision != revisionBefore+1 {
		t.Fatalf("publication did not advance catalog once: %d, %v", revision, err)
	}
}

func TestConcurrentTopologyPreparationsCannotClaimTheSameSource(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	first, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	stores := []*Store{first, second}
	results := make(chan error, len(stores))
	var group sync.WaitGroup
	for i, selected := range stores {
		group.Add(1)
		go func() {
			defer group.Done()
			payload := map[string]any{"operation": "split",
				"sources": map[string]any{"shared-source": 1},
				"targets": []any{}, "assignments": map[string]any{}}
			_, err := selected.PrepareTopologyEvolution(ctx, []string{"one", "two"}[i], payload)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	succeeded, failed := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			failed++
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("competing catalog writers both claimed a source: success=%d failure=%d",
			succeeded, failed)
	}
	pending, err := first.PendingTopologyEvolutions(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("source claim was not durable: %+v, %v", pending, err)
	}
}

func TestPublishTopologyEvolutionRejectsChangedSourceRevision(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	blocks, err := selected.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	parentID := blocks[0].Block.BlockID
	source, err := selected.CreateBlock(ctx, leafBlock("source", parentID), CreateOptions{
		BackendPartitionID: "source-partition", Actor: "test", Reason: "create source",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"operation": "split", "sources": map[string]any{"source": 1},
		"targets": []any{map[string]any{"block": leafBlock("target", parentID),
			"backend_partition_id": "target-partition"}},
		"assignments": map[string]any{"record-one": map[string]any{
			"record_revision_id": "revision-one", "target_block_id": "target"}},
		"reason": "split source", "actor": "test"}
	if _, err := selected.PrepareTopologyEvolution(ctx, "stale-source", payload); err != nil {
		t.Fatal(err)
	}
	if err := selected.MarkTopologyProjectionsReady(ctx, "stale-source"); err != nil {
		t.Fatal(err)
	}
	changed := source.Block
	changed.Name = "Changed while projecting"
	if _, err := selected.ReplaceBlock(ctx, source, changed, ReplaceOptions{
		ExpectedRevision: 1, Actor: "test", Reason: "change source", Operation: "amend_block",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := selected.CatalogRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PublishTopologyEvolution(ctx, "stale-source"); err == nil {
		t.Fatal("changed source revision was published")
	}
	if target, err := selected.GetBlock(ctx, "target"); err != nil || target != nil {
		t.Fatalf("failed publication leaked target block: %+v, %v", target, err)
	}
	if actual, err := selected.CatalogRevision(ctx); err != nil || actual != before {
		t.Fatalf("failed publication changed catalog revision: %d, %v", actual, err)
	}
	if journal, err := selected.TopologyEvolution(ctx, "stale-source"); err != nil ||
		journal == nil || journal.State != "projections_ready" {
		t.Fatalf("failed publication lost resumable journal: %+v, %v", journal, err)
	}
}

func TestPendingTopologyEvolutionsReadsScopedPythonJournal(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	insert := func(id, workspace, profile, state, created string, payload map[string]any) {
		t.Helper()
		raw, err := canonical(payload)
		if err != nil {
			t.Fatal(err)
		}
		checksum, err := fingerprint(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := selected.db.ExecContext(ctx, `INSERT INTO topology_evolutions(
			operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,
			abort_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,'',?,?)`,
			id, workspace, profile, state, raw, checksum, created, created); err != nil {
			t.Fatal(err)
		}
	}
	insert("later", "alpha", "team", "projections_ready", "2026-01-02",
		map[string]any{"operation": "merge", "sources": map[string]any{},
			"targets": []any{}, "assignments": map[string]any{}, "label": "Привет"})
	insert("earlier", "alpha", "team", "prepared", "2026-01-01",
		map[string]any{"operation": "split", "sources": map[string]any{},
			"targets": []any{}, "assignments": map[string]any{}})
	insert("published", "alpha", "team", "published", "2026-01-00",
		map[string]any{"operation": "split"})
	insert("other-profile", "alpha", "other", "prepared", "2026-01-00",
		map[string]any{"operation": "split"})
	insert("other-workspace", "beta", "team", "prepared", "2026-01-00",
		map[string]any{"operation": "split"})
	got, err := selected.PendingTopologyEvolutions(ctx)
	if err != nil || len(got) != 2 || got[0].OperationID != "earlier" ||
		got[0].Operation != "split" || got[1].OperationID != "later" ||
		got[1].State != "projections_ready" {
		t.Fatalf("pending journal scope or order differs: %+v, %v", got, err)
	}
	if _, err := selected.db.ExecContext(ctx, `UPDATE topology_evolutions
		SET payload_sha256='forged' WHERE operation_id='earlier'`); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PendingTopologyEvolutions(ctx); err == nil {
		t.Fatal("pending journal accepted a mismatched Python payload hash")
	}
}

func TestAbortTopologyEvolutionPreservesJournalAndRejectsPublished(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(t.TempDir(), "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	payload := map[string]any{"operation": "merge", "sources": map[string]any{},
		"targets": []any{}, "assignments": map[string]any{}}
	raw, err := canonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := fingerprint(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, state, profile string }{
		{"ready", "projections_ready", "team"},
		{"published", "published", "team"},
		{"foreign", "prepared", "other"},
	} {
		if _, err := selected.db.ExecContext(ctx, `INSERT INTO topology_evolutions(
			operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,
			abort_reason,created_at,updated_at) VALUES(?,'alpha',?,?,?,?, '',?,?)`,
			row.id, row.profile, row.state, raw, checksum, "2026-01-01", "2026-01-01"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := selected.AbortTopologyEvolution(ctx, "published", "wrong"); err == nil {
		t.Fatal("published generation was aborted")
	}
	if _, err := selected.AbortTopologyEvolution(ctx, "foreign", "wrong"); err == nil {
		t.Fatal("another profile's generation was aborted")
	}
	aborted, err := selected.AbortTopologyEvolution(ctx, "ready", " native projection failed ")
	if err != nil || aborted.State != "aborted" || aborted.Reason != "native projection failed" {
		t.Fatalf("prepared generation was not aborted: %+v, %v", aborted, err)
	}
	repeated, err := selected.AbortTopologyEvolution(ctx, "ready", "new reason")
	if err != nil || repeated != aborted {
		t.Fatalf("abort replay changed the retained reason: %+v, %v", repeated, err)
	}
	var persistedRaw, persistedChecksum, persistedState, persistedReason string
	if err := selected.db.QueryRowContext(ctx, `SELECT payload_json,payload_sha256,state,abort_reason
		FROM topology_evolutions WHERE operation_id='ready' AND profile_id='team'`).Scan(
		&persistedRaw, &persistedChecksum, &persistedState, &persistedReason); err != nil ||
		persistedRaw != raw || persistedChecksum != checksum ||
		persistedState != "aborted" || persistedReason != aborted.Reason {
		t.Fatalf("abort rewrote or lost the source mapping: %q %q %q %q, %v",
			persistedRaw, persistedChecksum, persistedState, persistedReason, err)
	}
	pending, err := selected.PendingTopologyEvolutions(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("aborted generation remains pending: %+v, %v", pending, err)
	}
}
