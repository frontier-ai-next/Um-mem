package operationalslo

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLatencyLedgerPersistsNativeEvidenceAndFailsClosedOnSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.slo.sqlite")
	ledger, err := OpenLedger(path, "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sample := OperationSample{Backend: "mempalace", CorpusTier: "1k", Concurrency: 1,
		QueryMode: "warm_exact", Component: "warm_recall", LatencyMS: 10,
		OperationCount: 10, WindowMS: 100, NativeOK: true, EffectiveMode: "selected_native",
		QueueLagMS: 5, ProjectionLagMS: 6, RecoveryTimeMS: 7, AgentTurnMS: 100}
	if err := ledger.RecordOperation(ctx, sample); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, err = OpenLedger(path, "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	report, err := ledger.LatencyReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 1 || !report.AllNativeQualified || !report.AllObservedSLOsPassed ||
		report.Groups[0].P95MS == nil || *report.Groups[0].P95MS != 10 ||
		report.Groups[0].MemoryLatencyFractionOfAgentTurn == nil ||
		*report.Groups[0].MemoryLatencyFractionOfAgentTurn != 0.1 {
		t.Fatalf("persisted native SLO evidence: %+v", report)
	}
	sample.NativeOK = false
	sample.EffectiveMode = "sidecar"
	sample.LatencyMS = 1
	if err := ledger.RecordOperation(ctx, sample); err != nil {
		t.Fatal(err)
	}
	report, err = ledger.LatencyReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.AllNativeQualified || report.AllObservedSLOsPassed || report.Groups[0].SLOPassed ||
		report.Groups[0].SampleCount != 1 {
		t.Fatalf("sidecar sample was allowed to qualify the native SLO: %+v", report)
	}
	wrong := sample
	wrong.Backend = "hindsight"
	if err := ledger.RecordOperation(ctx, wrong); err == nil {
		t.Fatal("foreign selected backend was recorded")
	}
}

func TestMeasuredAgentTurnRejectsDominantMemoryLatency(t *testing.T) {
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "service.slo.sqlite"), "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	sample := OperationSample{Backend: "mempalace", CorpusTier: "1k", Concurrency: 1,
		QueryMode: "warm_exact", Component: "warm_recall", LatencyMS: 200,
		OperationCount: 10, WindowMS: 100, NativeOK: true,
		EffectiveMode: "selected_native", AgentTurnMS: 300}
	if err := ledger.RecordOperation(context.Background(), sample); err != nil {
		t.Fatal(err)
	}
	report, err := ledger.LatencyReport(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 1 || report.Groups[0].SLOPassed ||
		report.Groups[0].MemoryLatencyFractionOfAgentTurn == nil ||
		*report.Groups[0].MemoryLatencyFractionOfAgentTurn <= 0.5 {
		t.Fatalf("dominant memory latency passed its agent-turn SLO: %+v", report)
	}
}
