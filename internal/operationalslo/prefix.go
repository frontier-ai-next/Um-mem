package operationalslo

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

const prefixSchema = `
CREATE TABLE IF NOT EXISTS prefix_samples (
    prefix_id INTEGER PRIMARY KEY AUTOINCREMENT,
    source TEXT NOT NULL,
    eligible_tokens INTEGER NOT NULL,
    reused_tokens INTEGER NOT NULL,
    invalidated_tokens INTEGER NOT NULL,
    input_tokens INTEGER NOT NULL,
    baseline_input_tokens INTEGER NOT NULL,
    total_compute_ms REAL NOT NULL,
    baseline_total_compute_ms REAL NOT NULL,
    recorded_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS prefix_observations (
    sample_id INTEGER PRIMARY KEY AUTOINCREMENT,
    observation_id TEXT UNIQUE,
    source TEXT NOT NULL,
    eligible_tokens INTEGER,
    reused_tokens INTEGER,
    invalidated_tokens INTEGER,
    input_tokens INTEGER,
    baseline_input_tokens INTEGER,
    total_compute_ms REAL,
    baseline_total_compute_ms REAL,
    recorded_at TEXT NOT NULL
);`

var prefixSources = []string{"static", "dynamic_guidance", "working_fold", "request"}

type PrefixCacheSample struct {
	Source                 string
	EligibleTokens         *int64
	ReusedTokens           *int64
	InvalidatedTokens      *int64
	InputTokens            *int64
	BaselineInputTokens    *int64
	TotalComputeMS         *float64
	BaselineTotalComputeMS *float64
	ObservationID          string
}

func (sample PrefixCacheSample) validate() error {
	if !slices.Contains(prefixSources, sample.Source) || len(sample.ObservationID) > 256 ||
		sample.ObservationID != "" && strings.TrimSpace(sample.ObservationID) == "" {
		return errors.New("prefix cache source or observation identity is invalid")
	}
	for _, value := range []*int64{sample.EligibleTokens, sample.ReusedTokens,
		sample.InvalidatedTokens, sample.InputTokens, sample.BaselineInputTokens} {
		if value != nil && *value < 0 {
			return errors.New("prefix cache token counts must be non-negative")
		}
	}
	for _, value := range []*float64{sample.TotalComputeMS, sample.BaselineTotalComputeMS} {
		if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return errors.New("prefix cache compute time must be finite and non-negative")
		}
	}
	if sample.EligibleTokens != nil {
		var reused, invalidated int64
		if sample.ReusedTokens != nil {
			reused = *sample.ReusedTokens
		}
		if sample.InvalidatedTokens != nil {
			invalidated = *sample.InvalidatedTokens
		}
		if reused > *sample.EligibleTokens || invalidated > *sample.EligibleTokens-reused {
			return errors.New("prefix cache accounting exceeds eligible tokens")
		}
	}
	return nil
}

type storedPrefix struct {
	source                                         string
	eligible, reused, invalidated, input, baseline sql.NullInt64
	compute, baselineCompute                       sql.NullFloat64
}

func (ledger *Ledger) RecordPrefixCache(ctx context.Context, sample PrefixCacheSample) error {
	if err := sample.validate(); err != nil {
		return err
	}
	tx, err := ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	value := storedPrefix{source: sample.Source, eligible: nullableInt(sample.EligibleTokens),
		reused: nullableInt(sample.ReusedTokens), invalidated: nullableInt(sample.InvalidatedTokens),
		input: nullableInt(sample.InputTokens), baseline: nullableInt(sample.BaselineInputTokens),
		compute: nullableFloat(sample.TotalComputeMS), baselineCompute: nullableFloat(sample.BaselineTotalComputeMS)}
	if sample.ObservationID != "" {
		var retained storedPrefix
		err = tx.QueryRowContext(ctx, `SELECT source,eligible_tokens,reused_tokens,invalidated_tokens,
input_tokens,baseline_input_tokens,total_compute_ms,baseline_total_compute_ms
FROM prefix_observations WHERE observation_id=?`, sample.ObservationID).Scan(
			&retained.source, &retained.eligible, &retained.reused, &retained.invalidated,
			&retained.input, &retained.baseline, &retained.compute, &retained.baselineCompute)
		if err == nil {
			if retained != value {
				return errors.New("prefix observation identity conflicts with retained values")
			}
			return tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	var observationID any
	if sample.ObservationID != "" {
		observationID = sample.ObservationID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO prefix_observations(
observation_id,source,eligible_tokens,reused_tokens,invalidated_tokens,input_tokens,
baseline_input_tokens,total_compute_ms,baseline_total_compute_ms,recorded_at)
VALUES(?,?,?,?,?,?,?,?,?,?)`, observationID, value.source, value.eligible, value.reused,
		value.invalidated, value.input, value.baseline, value.compute,
		value.baselineCompute, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

type PrefixSourceReport struct {
	SampleCount            int      `json:"sample_count"`
	EligibleTokens         *int64   `json:"eligible_tokens"`
	ReuseRate              *float64 `json:"reuse_rate"`
	InvalidationRate       *float64 `json:"invalidation_rate"`
	InputTokensSaved       *int64   `json:"input_tokens_saved"`
	TotalComputeMS         *float64 `json:"total_compute_ms"`
	BaselineTotalComputeMS *float64 `json:"baseline_total_compute_ms"`
	OptimizationAccepted   *bool    `json:"optimization_accepted"`
	ComparisonComplete     bool     `json:"comparison_complete"`
}

type PrefixCacheReport struct {
	Complete                 bool                          `json:"complete"`
	Sources                  map[string]PrefixSourceReport `json:"sources"`
	AllOptimizationsAccepted bool                          `json:"all_optimizations_accepted"`
}

func prefixIntSum(rows []storedPrefix, field func(storedPrefix) sql.NullInt64) *int64 {
	var total int64
	for _, row := range rows {
		value := field(row)
		if !value.Valid {
			return nil
		}
		total += value.Int64
	}
	return &total
}

func prefixFloatSum(rows []storedPrefix, field func(storedPrefix) sql.NullFloat64) *float64 {
	var total float64
	for _, row := range rows {
		value := field(row)
		if !value.Valid {
			return nil
		}
		total += value.Float64
	}
	return &total
}

func summarizePrefix(rows []storedPrefix) PrefixSourceReport {
	eligible := prefixIntSum(rows, func(row storedPrefix) sql.NullInt64 { return row.eligible })
	reused := prefixIntSum(rows, func(row storedPrefix) sql.NullInt64 { return row.reused })
	invalidated := prefixIntSum(rows, func(row storedPrefix) sql.NullInt64 { return row.invalidated })
	input := prefixIntSum(rows, func(row storedPrefix) sql.NullInt64 { return row.input })
	baseline := prefixIntSum(rows, func(row storedPrefix) sql.NullInt64 { return row.baseline })
	compute := prefixFloatSum(rows, func(row storedPrefix) sql.NullFloat64 { return row.compute })
	baselineCompute := prefixFloatSum(rows, func(row storedPrefix) sql.NullFloat64 { return row.baselineCompute })
	result := PrefixSourceReport{SampleCount: len(rows), EligibleTokens: eligible,
		TotalComputeMS: compute, BaselineTotalComputeMS: baselineCompute,
		ComparisonComplete: input != nil && baseline != nil && compute != nil && baselineCompute != nil}
	if eligible != nil && *eligible != 0 {
		if reused != nil {
			rate := float64(*reused) / float64(*eligible)
			result.ReuseRate = &rate
		}
		if invalidated != nil {
			rate := float64(*invalidated) / float64(*eligible)
			result.InvalidationRate = &rate
		}
	}
	if input != nil && baseline != nil {
		saved := *baseline - *input
		result.InputTokensSaved = &saved
	}
	if result.ComparisonComplete {
		accepted := *input <= *baseline && *compute <= *baselineCompute
		result.OptimizationAccepted = &accepted
	}
	return result
}

func (ledger *Ledger) PrefixCacheReport(ctx context.Context) (PrefixCacheReport, error) {
	rows, err := ledger.db.QueryContext(ctx, `SELECT source,eligible_tokens,reused_tokens,
invalidated_tokens,input_tokens,baseline_input_tokens,total_compute_ms,
baseline_total_compute_ms FROM prefix_samples UNION ALL SELECT source,eligible_tokens,
reused_tokens,invalidated_tokens,input_tokens,baseline_input_tokens,total_compute_ms,
baseline_total_compute_ms FROM prefix_observations`)
	if err != nil {
		return PrefixCacheReport{}, err
	}
	defer rows.Close()
	bySource := map[string][]storedPrefix{}
	for rows.Next() {
		var sample storedPrefix
		if err := rows.Scan(&sample.source, &sample.eligible, &sample.reused,
			&sample.invalidated, &sample.input, &sample.baseline,
			&sample.compute, &sample.baselineCompute); err != nil {
			return PrefixCacheReport{}, err
		}
		bySource[sample.source] = append(bySource[sample.source], sample)
	}
	if err := rows.Err(); err != nil {
		return PrefixCacheReport{}, err
	}
	report := PrefixCacheReport{Sources: map[string]PrefixSourceReport{},
		AllOptimizationsAccepted: true}
	for _, source := range prefixSources {
		if selected := bySource[source]; len(selected) != 0 {
			result := summarizePrefix(selected)
			report.Sources[source] = result
			report.AllOptimizationsAccepted = report.AllOptimizationsAccepted &&
				result.OptimizationAccepted != nil && *result.OptimizationAccepted
		}
	}
	report.Complete = true
	for _, required := range prefixSources[:3] {
		if _, present := report.Sources[required]; !present {
			report.Complete = false
		}
	}
	for _, result := range report.Sources {
		if !result.ComparisonComplete || result.ReuseRate == nil || result.InvalidationRate == nil {
			report.Complete = false
		}
	}
	report.AllOptimizationsAccepted = len(report.Sources) > 0 && report.AllOptimizationsAccepted
	return report, nil
}
