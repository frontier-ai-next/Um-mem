package engine

import (
	"context"
	"os"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestListRecordsPythonCursorFixture(t *testing.T) {
	rows := [][]string{{"mem_one", "rev_one"}, {"mem_two", "rev_two"}}
	payload, err := jsoncompat.MarshalPython(rows, false)
	if err != nil {
		t.Fatal(err)
	}
	const expectedID = "db37333920adfbb5d02c21546958469766273b40d3a32e56ca97ed4b571daf21"
	if string(payload) != `[["mem_one","rev_one"],["mem_two","rev_two"]]` || snapshotHash(payload) != expectedID {
		t.Fatalf("Go page hash differs from Python: %s, %s", payload, snapshotHash(payload))
	}
	const pythonCursor = "eyJ2ZXJzaW9uIjoyLCJ3b3Jrc3BhY2VfaWQiOiJhbHBoYSIsInByb2ZpbGVfaWQiOiJ0ZWFtIiwic2NvcGUiOiJ3b3Jrc3BhY2UiLCJzY29wZV9pZCI6ImFscGhhIiwibG9naWNhbF9ibG9ja19pZCI6IiIsInBvc2l0aW9uIjoxLCJzbmFwc2hvdF9pZCI6ImRiMzczMzM5MjBhZGZiYjVkMDJjMjE1NDY5NTg0Njk3NjYyNzNiNDBkM2EzMmU1NmNhOTdlZDRiNTcxZGFmMjEifQ"
	cursor, err := decodePageCursor(pythonCursor)
	if err != nil || cursor.SnapshotID != expectedID || cursor.Position != 1 ||
		cursor.WorkspaceID != "alpha" || cursor.ProfileID != "team" {
		t.Fatalf("Go could not read Python page cursor: %+v, %v", cursor, err)
	}
}

func TestListRecordsUsesFrozenBoundedSnapshot(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	selected.projector = verifiedProjector{}
	ctx := context.Background()
	write := func(id, revision, key, date string) {
		record := base
		record.BlockID, record.RevisionID, record.CanonicalKey = id, revision, key
		record.BlockMembership = nil
		record.SemanticCatalogVersion = ""
		record.RecordedAt, record.ValidFrom, record.UpdatedAt = date, date, date
		if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
			t.Fatal(err)
		}
		if _, err := selected.PutRoot(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	write("mem_one", "rev_one", "alpha:one", "2024-01-01T00:00:00Z")
	write("mem_two", "rev_two", "alpha:two", "2024-02-01T00:00:00Z")
	request := ListRecordsRequest{Scope: "workspace", ScopeID: "alpha", Limit: 1}
	first, err := selected.ListRecords(ctx, request)
	if err != nil || first.SnapshotCount != 2 || len(first.Records) != 1 ||
		first.Records[0]["record_id"] != "mem_one" || first.NextCursor == "" {
		t.Fatalf("first frozen page differs: %+v, %v", first, err)
	}
	write("mem_three", "rev_three", "alpha:three", "2024-03-01T00:00:00Z")
	request.Cursor = first.NextCursor
	second, err := selected.ListRecords(ctx, request)
	if err != nil || second.SnapshotCount != 2 || len(second.Records) != 1 ||
		second.Records[0]["record_id"] != "mem_two" || second.NextCursor != "" {
		t.Fatalf("second page changed after a new write: %+v, %v", second, err)
	}
	request.ScopeID = "other"
	if _, err := selected.ListRecords(ctx, request); err == nil || err.Error() != "list_records cursor scope does not match" {
		t.Fatalf("cursor crossed exact scope: %v", err)
	}
	request.ScopeID, request.Cursor = "alpha", ""
	request.IncludeRetracted = false
	full, err := selected.ListRecords(ctx, ListRecordsRequest{Scope: "workspace", ScopeID: "alpha", Limit: 32})
	if err != nil || full.SnapshotCount != 3 || len(full.Records) != 3 {
		t.Fatalf("fresh snapshot missed a new record: %+v, %v", full, err)
	}
}
