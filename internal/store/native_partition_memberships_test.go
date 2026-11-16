package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestNativePartitionMembershipsAreProfileScopedAndContentFree(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	if _, err := writer.PrepareRootWrite(ctx, record); err != nil {
		t.Fatal(err)
	}
	selected, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	identities, err := selected.NativePartitionMemberships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := []NativePartitionMembership{{LogicalBlockID: record.BlockMembership.PrimaryBlockID,
		CatalogRevisionID:  record.BlockMembership.CatalogRevisionID,
		BackendPartitionID: record.BlockMembership.BackendPartitionID}}
	if !reflect.DeepEqual(identities, expected) {
		t.Fatalf("native partition identities = %+v, want %+v", identities, expected)
	}
	foreign, err := OpenIndex(path, "alpha", "other")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if leaked, err := foreign.NativePartitionMemberships(ctx); err != nil || len(leaked) != 0 {
		t.Fatalf("foreign profile saw native partitions: %+v, %v", leaked, err)
	}
}
