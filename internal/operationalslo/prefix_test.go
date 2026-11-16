package operationalslo

import (
	"context"
	"path/filepath"
	"testing"
)

func TestPrefixReportRequiresPairedCompleteSources(t *testing.T) {
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "service.slo.sqlite"), "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	ctx := context.Background()
	eligible, reused, invalidated := int64(100), int64(40), int64(10)
	input, baseline := int64(60), int64(100)
	compute, baselineCompute := 4.0, 10.0
	for _, source := range prefixSources[:3] {
		sample := PrefixCacheSample{Source: source, EligibleTokens: &eligible,
			ReusedTokens: &reused, InvalidatedTokens: &invalidated,
			InputTokens: &input, BaselineInputTokens: &baseline,
			TotalComputeMS: &compute, BaselineTotalComputeMS: &baselineCompute,
			ObservationID: "prefix:" + source}
		if err := ledger.RecordPrefixCache(ctx, sample); err != nil {
			t.Fatal(err)
		}
		if err := ledger.RecordPrefixCache(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	report, err := ledger.PrefixCacheReport(ctx)
	if err != nil || !report.Complete || !report.AllOptimizationsAccepted ||
		len(report.Sources) != 3 || report.Sources["static"].ReuseRate == nil ||
		*report.Sources["static"].ReuseRate != 0.4 ||
		report.Sources["static"].InputTokensSaved == nil ||
		*report.Sources["static"].InputTokensSaved != 40 {
		t.Fatalf("complete prefix report: %+v %v", report, err)
	}
	conflict := PrefixCacheSample{Source: "static", ObservationID: "prefix:static"}
	if err := ledger.RecordPrefixCache(ctx, conflict); err == nil {
		t.Fatal("prefix observation identity accepted changed values")
	}
	if err := ledger.RecordPrefixCache(ctx, PrefixCacheSample{Source: "static"}); err != nil {
		t.Fatal(err)
	}
	report, err = ledger.PrefixCacheReport(ctx)
	if err != nil || report.Complete || report.AllOptimizationsAccepted ||
		report.Sources["static"].ReuseRate != nil {
		t.Fatalf("missing prefix fields were treated as observed: %+v %v", report, err)
	}
	tooMany := int64(101)
	if err := ledger.RecordPrefixCache(ctx, PrefixCacheSample{Source: "static",
		EligibleTokens: &eligible, ReusedTokens: &tooMany}); err == nil {
		t.Fatal("reused tokens exceeded eligible tokens")
	}
}
