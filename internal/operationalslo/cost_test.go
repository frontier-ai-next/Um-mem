package operationalslo

import (
	"context"
	"path/filepath"
	"testing"
)

func TestHeadlineCostRequiresEveryCategoryAndKnownFields(t *testing.T) {
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "service.slo.sqlite"), "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	ctx := context.Background()
	latency, compute := 1.0, 2.0
	tokens, written := int64(3), int64(4)
	for _, category := range costCategories {
		sample := CostSample{Category: category, LatencyMS: &latency, ComputeMS: &compute,
			Tokens: &tokens, BytesWritten: &written, ObservationID: "cost:" + category}
		if err := ledger.RecordCost(ctx, sample); err != nil {
			t.Fatal(err)
		}
		if err := ledger.RecordCost(ctx, sample); err != nil {
			t.Fatal(err)
		}
	}
	report, err := ledger.HeadlineCostReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.MissingCategories) != 0 ||
		report.Totals.LatencyMS == nil || *report.Totals.LatencyMS != 8 ||
		report.Totals.ComputeMS == nil || *report.Totals.ComputeMS != 16 ||
		report.Totals.Tokens == nil || *report.Totals.Tokens != 24 ||
		report.Totals.BytesWritten == nil || *report.Totals.BytesWritten != 32 ||
		report.FieldCoverage["tokens"].Total != len(costCategories) ||
		len(report.Background) != len(costCategories)-2 {
		t.Fatalf("headline cost coverage or totals differ: %+v", report)
	}
	conflict := CostSample{Category: "capture", ObservationID: "cost:capture", Tokens: &tokens}
	if err := ledger.RecordCost(ctx, conflict); err == nil {
		t.Fatal("cost observation identity accepted different retained values")
	}
	if err := ledger.RecordCost(ctx, CostSample{Category: "capture"}); err != nil {
		t.Fatal(err)
	}
	report, err = ledger.HeadlineCostReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || report.Totals.Tokens != nil ||
		report.FieldCoverage["tokens"].Observed != len(costCategories) {
		t.Fatalf("unknown cost values were silently filled: %+v", report)
	}
}
