package operationalslo

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const operationSchema = `CREATE TABLE IF NOT EXISTS operation_samples (
    sample_id INTEGER PRIMARY KEY AUTOINCREMENT,
    backend TEXT NOT NULL,
    corpus_tier TEXT NOT NULL,
    concurrency INTEGER NOT NULL,
    query_mode TEXT NOT NULL,
    component TEXT NOT NULL,
    latency_ms REAL NOT NULL,
    steps INTEGER NOT NULL,
    tokens INTEGER NOT NULL,
    candidates INTEGER NOT NULL,
    operation_count INTEGER NOT NULL,
    window_ms REAL NOT NULL,
    native_ok INTEGER NOT NULL,
    effective_mode TEXT NOT NULL,
    queue_lag_ms REAL NOT NULL,
    projection_lag_ms REAL NOT NULL,
    recovery_time_ms REAL NOT NULL,
    agent_turn_ms REAL NOT NULL,
    recorded_at TEXT NOT NULL
);`

var components = []string{"cold_start", "warm_recall", "reconstruction", "model"}

type OperationSample struct {
	Backend         string
	CorpusTier      string
	Concurrency     int
	QueryMode       string
	Component       string
	LatencyMS       float64
	Steps           int
	Tokens          int
	Candidates      int
	OperationCount  int
	WindowMS        float64
	NativeOK        bool
	EffectiveMode   string
	QueueLagMS      float64
	ProjectionLagMS float64
	RecoveryTimeMS  float64
	AgentTurnMS     float64
}

func (sample OperationSample) validate(selectedBackend string) error {
	if !slices.Contains(Backends, sample.Backend) || sample.Backend != selectedBackend ||
		!slices.Contains(CorpusTiers, sample.CorpusTier) ||
		!slices.Contains(ConcurrencyLevels, sample.Concurrency) ||
		!slices.Contains(QueryModes, sample.QueryMode) ||
		!slices.Contains(components, sample.Component) ||
		sample.Steps < 0 || sample.Tokens < 0 || sample.Candidates < 0 ||
		sample.OperationCount < 1 || strings.TrimSpace(sample.EffectiveMode) == "" {
		return errors.New("invalid service SLO operation sample")
	}
	for _, value := range []float64{sample.LatencyMS, sample.WindowMS, sample.QueueLagMS,
		sample.ProjectionLagMS, sample.RecoveryTimeMS, sample.AgentTurnMS} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("service SLO numeric values must be finite and non-negative")
		}
	}
	if sample.AgentTurnMS > 0 && sample.LatencyMS > sample.AgentTurnMS {
		return errors.New("memory latency exceeds the measured agent turn")
	}
	return nil
}

type Ledger struct {
	selectedBackend string
	db              *sql.DB
}

func OpenLedger(path, selectedBackend string) (*Ledger, error) {
	if path == "" || !slices.Contains(Backends, selectedBackend) {
		return nil, errors.New("service SLO path or backend is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(operationSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(costSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(storageSchema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(prefixSchema); err != nil {
		db.Close()
		return nil, err
	}
	return &Ledger{selectedBackend: selectedBackend, db: db}, nil
}

func (ledger *Ledger) Close() error { return ledger.db.Close() }

func (ledger *Ledger) RecordOperation(ctx context.Context, sample OperationSample) error {
	if err := sample.validate(ledger.selectedBackend); err != nil {
		return err
	}
	native := 0
	if sample.NativeOK {
		native = 1
	}
	_, err := ledger.db.ExecContext(ctx, `INSERT INTO operation_samples(
backend,corpus_tier,concurrency,query_mode,component,latency_ms,steps,tokens,candidates,
operation_count,window_ms,native_ok,effective_mode,queue_lag_ms,projection_lag_ms,
recovery_time_ms,agent_turn_ms,recorded_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		sample.Backend, sample.CorpusTier, sample.Concurrency, sample.QueryMode, sample.Component,
		sample.LatencyMS, sample.Steps, sample.Tokens, sample.Candidates,
		sample.OperationCount, sample.WindowMS, native, sample.EffectiveMode,
		sample.QueueLagMS, sample.ProjectionLagMS, sample.RecoveryTimeMS,
		sample.AgentTurnMS, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type latencyKey struct {
	backend, tier   string
	concurrency     int
	mode, component string
}

type latencyGroup struct {
	samples     []OperationSample
	unavailable bool
}

type LatencySummary struct {
	Backend                          string   `json:"backend"`
	CorpusTier                       string   `json:"corpus_tier"`
	Concurrency                      int      `json:"concurrency"`
	QueryMode                        string   `json:"query_mode"`
	Component                        string   `json:"component"`
	SampleCount                      int      `json:"sample_count"`
	P50MS                            *float64 `json:"p50_ms"`
	P95MS                            *float64 `json:"p95_ms"`
	P99MS                            *float64 `json:"p99_ms"`
	ThroughputPerSecond              float64  `json:"throughput_per_second"`
	MaxQueueLagMS                    float64  `json:"max_queue_lag_ms"`
	MaxProjectionLagMS               float64  `json:"max_projection_lag_ms"`
	MaxRecoveryTimeMS                float64  `json:"max_recovery_time_ms"`
	MemoryLatencyFractionOfAgentTurn *float64 `json:"memory_latency_fraction_of_agent_turn"`
	NativeQualified                  bool     `json:"native_qualified"`
	SLOPassed                        bool     `json:"slo_passed"`
}

type LatencyReport struct {
	SLOContractRevision   string           `json:"slo_contract_revision"`
	Groups                []LatencySummary `json:"groups"`
	AllNativeQualified    bool             `json:"all_native_qualified"`
	AllObservedSLOsPassed bool             `json:"all_observed_slos_passed"`
}

func percentile(values []float64, fraction float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	ordered := slices.Clone(values)
	sort.Float64s(ordered)
	value := ordered[max(0, int(math.Ceil(float64(len(ordered))*fraction))-1)]
	return &value
}

func (ledger *Ledger) LatencyReport(ctx context.Context) (LatencyReport, error) {
	rows, err := ledger.db.QueryContext(ctx, `SELECT backend,corpus_tier,concurrency,query_mode,component,
latency_ms,operation_count,window_ms,native_ok,effective_mode,queue_lag_ms,
projection_lag_ms,recovery_time_ms,agent_turn_ms FROM operation_samples ORDER BY sample_id`)
	if err != nil {
		return LatencyReport{}, err
	}
	defer rows.Close()
	groups := map[latencyKey]*latencyGroup{}
	for rows.Next() {
		var sample OperationSample
		var native int
		if err := rows.Scan(&sample.Backend, &sample.CorpusTier, &sample.Concurrency,
			&sample.QueryMode, &sample.Component, &sample.LatencyMS, &sample.OperationCount,
			&sample.WindowMS, &native, &sample.EffectiveMode, &sample.QueueLagMS,
			&sample.ProjectionLagMS, &sample.RecoveryTimeMS, &sample.AgentTurnMS); err != nil {
			return LatencyReport{}, err
		}
		key := latencyKey{sample.Backend, sample.CorpusTier, sample.Concurrency, sample.QueryMode, sample.Component}
		group := groups[key]
		if group == nil {
			group = &latencyGroup{}
			groups[key] = group
		}
		if native != 1 || sample.EffectiveMode != "selected_native" {
			group.unavailable = true
		} else {
			group.samples = append(group.samples, sample)
		}
	}
	if err := rows.Err(); err != nil {
		return LatencyReport{}, err
	}
	revision, err := ContractRevision()
	if err != nil {
		return LatencyReport{}, err
	}
	report := LatencyReport{SLOContractRevision: revision, Groups: []LatencySummary{},
		AllNativeQualified: len(groups) > 0, AllObservedSLOsPassed: len(groups) > 0}
	keys := make([]latencyKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.backend != b.backend {
			return a.backend < b.backend
		}
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.concurrency != b.concurrency {
			return a.concurrency < b.concurrency
		}
		if a.mode != b.mode {
			return a.mode < b.mode
		}
		return a.component < b.component
	})
	thresholds := map[latencyKey]Threshold{}
	for _, threshold := range Matrix() {
		thresholds[latencyKey{threshold.Backend, threshold.CorpusTier,
			threshold.Concurrency, threshold.QueryMode, ""}] = threshold
	}
	for _, key := range keys {
		group := groups[key]
		threshold, exists := thresholds[latencyKey{key.backend, key.tier, key.concurrency, key.mode, ""}]
		if !exists || !slices.Contains(components, key.component) {
			return LatencyReport{}, errors.New("service SLO ledger contains an invalid axis")
		}
		summary := summarizeLatency(key, group, threshold)
		report.Groups = append(report.Groups, summary)
		report.AllNativeQualified = report.AllNativeQualified && summary.NativeQualified
		report.AllObservedSLOsPassed = report.AllObservedSLOsPassed && summary.SLOPassed
	}
	return report, nil
}

func summarizeLatency(key latencyKey, group *latencyGroup, threshold Threshold) LatencySummary {
	summary := LatencySummary{Backend: key.backend, CorpusTier: key.tier,
		Concurrency: key.concurrency, QueryMode: key.mode, Component: key.component,
		SampleCount: len(group.samples), NativeQualified: !group.unavailable && len(group.samples) > 0}
	latencies := make([]float64, 0, len(group.samples))
	var count int
	var window, totalLatency, totalTurn float64
	for _, sample := range group.samples {
		latencies = append(latencies, sample.LatencyMS)
		count += sample.OperationCount
		window += sample.WindowMS
		totalLatency += sample.LatencyMS
		totalTurn += sample.AgentTurnMS
		summary.MaxQueueLagMS = max(summary.MaxQueueLagMS, sample.QueueLagMS)
		summary.MaxProjectionLagMS = max(summary.MaxProjectionLagMS, sample.ProjectionLagMS)
		summary.MaxRecoveryTimeMS = max(summary.MaxRecoveryTimeMS, sample.RecoveryTimeMS)
	}
	if window > 0 {
		summary.ThroughputPerSecond = float64(count) * 1000 / window
	}
	if totalTurn > 0 {
		fraction := totalLatency / totalTurn
		summary.MemoryLatencyFractionOfAgentTurn = &fraction
	}
	summary.P50MS = percentile(latencies, 0.5)
	summary.P95MS = percentile(latencies, 0.95)
	summary.P99MS = percentile(latencies, 0.99)
	recallMode := strings.HasPrefix(key.mode, "cold_") || strings.HasPrefix(key.mode, "warm_")
	summary.SLOPassed = summary.NativeQualified && summary.P50MS != nil &&
		*summary.P50MS <= float64(threshold.P50MS) && *summary.P95MS <= float64(threshold.P95MS) &&
		*summary.P99MS <= float64(threshold.P99MS) &&
		summary.ThroughputPerSecond >= threshold.MinThroughputPerSecond &&
		summary.MaxQueueLagMS <= float64(threshold.MaxQueueLagMS) &&
		summary.MaxProjectionLagMS <= float64(threshold.MaxProjectionLagMS) &&
		summary.MaxRecoveryTimeMS <= float64(threshold.RTOMS) &&
		(!recallMode || summary.MemoryLatencyFractionOfAgentTurn != nil &&
			*summary.MemoryLatencyFractionOfAgentTurn <= threshold.MaxMemoryTurnFraction)
	return summary
}
