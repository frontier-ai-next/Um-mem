package operationalslo

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStorageReportRequiresTwoSamplesAndTracksChurn(t *testing.T) {
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "service.slo.sqlite"), "mnemosyne")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	ctx := context.Background()
	report, err := ledger.StorageReport(ctx)
	if err != nil || report["complete"] != false || report["sample_count"] != 0 {
		t.Fatalf("empty storage report: %+v %v", report, err)
	}
	first := StorageSample{CanonicalBytes: 100, IndexBytes: 40, ArtifactBytes: 10,
		TotalWriteBytes: 200, CatalogRevision: "catalog:a", GuidanceTokens: 3,
		CompactionComputeMS: 2, RefinementComputeMS: 4}
	if err := ledger.RecordStorage(ctx, first); err != nil {
		t.Fatal(err)
	}
	report, err = ledger.StorageReport(ctx)
	if err != nil || report["complete"] != false || report["sample_count"] != 1 {
		t.Fatalf("single-sample storage report: %+v %v", report, err)
	}
	second := StorageSample{CanonicalBytes: 200, IndexBytes: 65, ArtifactBytes: 12,
		TotalWriteBytes: 450, CatalogRevision: "catalog:b", GuidanceTokens: 5,
		CompactionComputeMS: 8, RefinementComputeMS: 9}
	if err := ledger.RecordStorage(ctx, second); err != nil {
		t.Fatal(err)
	}
	report, err = ledger.StorageReport(ctx)
	if err != nil || report["complete"] != true || report["catalog_churn"] != 1 ||
		report["write_amplification"] != 2.5 || report["guidance_overhead_tokens"] != int64(5) ||
		report["storage_growth_bytes"].(map[string]int64)["index"] != 25 {
		t.Fatalf("storage growth report: %+v %v", report, err)
	}
	second.CatalogRevision = ""
	if err := ledger.RecordStorage(ctx, second); err == nil {
		t.Fatal("storage sample without a catalog revision was accepted")
	}
}
