package operationalslo

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

const costSchema = `
CREATE TABLE IF NOT EXISTS cost_samples (
    cost_id INTEGER PRIMARY KEY AUTOINCREMENT,
    category TEXT NOT NULL,
    latency_ms REAL NOT NULL,
    compute_ms REAL NOT NULL,
    tokens INTEGER NOT NULL,
    bytes_written INTEGER NOT NULL,
    recorded_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS cost_observations (
    sample_id INTEGER PRIMARY KEY AUTOINCREMENT,
    observation_id TEXT UNIQUE,
    category TEXT NOT NULL,
    latency_ms REAL,
    compute_ms REAL,
    tokens INTEGER,
    bytes_written INTEGER,
    recorded_at TEXT NOT NULL
);`

var costCategories = []string{"capture", "embedding", "online_recall", "model",
	"consolidation", "policy_qualification", "rebuild", "background_maintenance"}

type CostSample struct {
	Category      string
	LatencyMS     *float64
	ComputeMS     *float64
	Tokens        *int64
	BytesWritten  *int64
	ObservationID string
}

func (sample CostSample) validate() error {
	if !slices.Contains(costCategories, sample.Category) || len(sample.ObservationID) > 256 ||
		sample.ObservationID != "" && strings.TrimSpace(sample.ObservationID) == "" {
		return errors.New("cost observation category or identity is invalid")
	}
	for _, value := range []*float64{sample.LatencyMS, sample.ComputeMS} {
		if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return errors.New("cost latency and compute must be finite and non-negative")
		}
	}
	for _, value := range []*int64{sample.Tokens, sample.BytesWritten} {
		if value != nil && *value < 0 {
			return errors.New("cost tokens and bytes must be non-negative")
		}
	}
	return nil
}

func nullableFloat(value *float64) sql.NullFloat64 {
	if value == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *value, Valid: true}
}

func nullableInt(value *int64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *value, Valid: true}
}

func (ledger *Ledger) RecordCost(ctx context.Context, sample CostSample) error {
	if err := sample.validate(); err != nil {
		return err
	}
	tx, err := ledger.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	latency, compute := nullableFloat(sample.LatencyMS), nullableFloat(sample.ComputeMS)
	tokens, written := nullableInt(sample.Tokens), nullableInt(sample.BytesWritten)
	if sample.ObservationID != "" {
		var category string
		var retainedLatency, retainedCompute sql.NullFloat64
		var retainedTokens, retainedWritten sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT category,latency_ms,compute_ms,tokens,bytes_written
FROM cost_observations WHERE observation_id=?`, sample.ObservationID).Scan(
			&category, &retainedLatency, &retainedCompute, &retainedTokens, &retainedWritten)
		if err == nil {
			if category != sample.Category || retainedLatency != latency || retainedCompute != compute ||
				retainedTokens != tokens || retainedWritten != written {
				return errors.New("cost observation identity conflicts with retained values")
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
	_, err = tx.ExecContext(ctx, `INSERT INTO cost_observations(
observation_id,category,latency_ms,compute_ms,tokens,bytes_written,recorded_at)
VALUES(?,?,?,?,?,?,?)`, observationID, sample.Category, latency, compute, tokens, written,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

type CostValues struct {
	LatencyMS    *float64 `json:"latency_ms"`
	ComputeMS    *float64 `json:"compute_ms"`
	Tokens       *int64   `json:"tokens"`
	BytesWritten *int64   `json:"bytes_written"`
}

type FieldCoverage struct {
	Observed int `json:"observed"`
	Total    int `json:"total"`
}

type HeadlineCostReport struct {
	Complete          bool                     `json:"complete"`
	MissingCategories []string                 `json:"missing_categories"`
	Categories        map[string]CostValues    `json:"categories"`
	Background        map[string]CostValues    `json:"background"`
	Totals            CostValues               `json:"totals"`
	FieldCoverage     map[string]FieldCoverage `json:"field_coverage"`
}

type storedCost struct {
	category         string
	latency, compute sql.NullFloat64
	tokens, written  sql.NullInt64
}

func sumFloat(rows []storedCost, value func(storedCost) sql.NullFloat64) *float64 {
	if len(rows) == 0 {
		return nil
	}
	var sum float64
	for _, row := range rows {
		field := value(row)
		if !field.Valid {
			return nil
		}
		sum += field.Float64
	}
	return &sum
}

func sumInt(rows []storedCost, value func(storedCost) sql.NullInt64) *int64 {
	if len(rows) == 0 {
		return nil
	}
	var sum int64
	for _, row := range rows {
		field := value(row)
		if !field.Valid {
			return nil
		}
		sum += field.Int64
	}
	return &sum
}

func costValues(rows []storedCost) CostValues {
	return CostValues{
		LatencyMS:    sumFloat(rows, func(row storedCost) sql.NullFloat64 { return row.latency }),
		ComputeMS:    sumFloat(rows, func(row storedCost) sql.NullFloat64 { return row.compute }),
		Tokens:       sumInt(rows, func(row storedCost) sql.NullInt64 { return row.tokens }),
		BytesWritten: sumInt(rows, func(row storedCost) sql.NullInt64 { return row.written }),
	}
}

func (ledger *Ledger) HeadlineCostReport(ctx context.Context) (HeadlineCostReport, error) {
	rows, err := ledger.db.QueryContext(ctx, `SELECT category,latency_ms,compute_ms,tokens,bytes_written FROM cost_samples
UNION ALL SELECT category,latency_ms,compute_ms,tokens,bytes_written FROM cost_observations`)
	if err != nil {
		return HeadlineCostReport{}, err
	}
	defer rows.Close()
	all := []storedCost{}
	byCategory := map[string][]storedCost{}
	for rows.Next() {
		var item storedCost
		if err := rows.Scan(&item.category, &item.latency, &item.compute, &item.tokens, &item.written); err != nil {
			return HeadlineCostReport{}, err
		}
		all = append(all, item)
		byCategory[item.category] = append(byCategory[item.category], item)
	}
	if err := rows.Err(); err != nil {
		return HeadlineCostReport{}, err
	}
	report := HeadlineCostReport{MissingCategories: []string{}, Categories: map[string]CostValues{},
		Background: map[string]CostValues{}, Totals: costValues(all), FieldCoverage: map[string]FieldCoverage{}}
	for category, selected := range byCategory {
		values := costValues(selected)
		report.Categories[category] = values
		if category != "online_recall" && category != "model" {
			report.Background[category] = values
		}
	}
	for _, category := range costCategories {
		if _, present := byCategory[category]; !present {
			report.MissingCategories = append(report.MissingCategories, category)
		}
	}
	sort.Strings(report.MissingCategories)
	for field, observed := range map[string]int{
		"latency_ms":    countFloat(all, func(row storedCost) sql.NullFloat64 { return row.latency }),
		"compute_ms":    countFloat(all, func(row storedCost) sql.NullFloat64 { return row.compute }),
		"tokens":        countInt(all, func(row storedCost) sql.NullInt64 { return row.tokens }),
		"bytes_written": countInt(all, func(row storedCost) sql.NullInt64 { return row.written }),
	} {
		report.FieldCoverage[field] = FieldCoverage{Observed: observed, Total: len(all)}
	}
	report.Complete = len(report.MissingCategories) == 0 && report.Totals.LatencyMS != nil &&
		report.Totals.ComputeMS != nil && report.Totals.Tokens != nil && report.Totals.BytesWritten != nil
	return report, nil
}

func countFloat(rows []storedCost, value func(storedCost) sql.NullFloat64) int {
	count := 0
	for _, row := range rows {
		if value(row).Valid {
			count++
		}
	}
	return count
}

func countInt(rows []storedCost, value func(storedCost) sql.NullInt64) int {
	count := 0
	for _, row := range rows {
		if value(row).Valid {
			count++
		}
	}
	return count
}
