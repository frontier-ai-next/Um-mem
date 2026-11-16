package operationalslo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const storageSchema = `CREATE TABLE IF NOT EXISTS storage_samples (
    storage_id INTEGER PRIMARY KEY AUTOINCREMENT,
    canonical_bytes INTEGER NOT NULL,
    index_bytes INTEGER NOT NULL,
    artifact_bytes INTEGER NOT NULL,
    total_write_bytes INTEGER NOT NULL,
    catalog_revision_sha256 TEXT NOT NULL,
    guidance_tokens INTEGER NOT NULL,
    compaction_compute_ms REAL NOT NULL,
    refinement_compute_ms REAL NOT NULL,
    recorded_at TEXT NOT NULL
);`

type StorageSample struct {
	CanonicalBytes      int64
	IndexBytes          int64
	ArtifactBytes       int64
	TotalWriteBytes     int64
	CatalogRevision     string
	GuidanceTokens      int64
	CompactionComputeMS float64
	RefinementComputeMS float64
}

func (sample StorageSample) validate() error {
	if sample.CanonicalBytes < 0 || sample.IndexBytes < 0 || sample.ArtifactBytes < 0 ||
		sample.TotalWriteBytes < 0 || sample.GuidanceTokens < 0 ||
		strings.TrimSpace(sample.CatalogRevision) == "" {
		return errors.New("storage sample requires non-negative sizes and a catalog revision")
	}
	for _, value := range []float64{sample.CompactionComputeMS, sample.RefinementComputeMS} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("storage compute time must be finite and non-negative")
		}
	}
	return nil
}

func (ledger *Ledger) RecordStorage(ctx context.Context, sample StorageSample) error {
	if err := sample.validate(); err != nil {
		return err
	}
	revision := fmt.Sprintf("%x", sha256.Sum256([]byte(sample.CatalogRevision)))
	_, err := ledger.db.ExecContext(ctx, `INSERT INTO storage_samples(
canonical_bytes,index_bytes,artifact_bytes,total_write_bytes,catalog_revision_sha256,
guidance_tokens,compaction_compute_ms,refinement_compute_ms,recorded_at)
VALUES(?,?,?,?,?,?,?,?,?)`, sample.CanonicalBytes, sample.IndexBytes, sample.ArtifactBytes,
		sample.TotalWriteBytes, revision, sample.GuidanceTokens,
		sample.CompactionComputeMS, sample.RefinementComputeMS,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

type storageRow struct {
	canonical, index, artifact, written int64
	revision                            string
	guidance                            int64
	compaction, refinement              float64
}

func (ledger *Ledger) StorageReport(ctx context.Context) (map[string]any, error) {
	rows, err := ledger.db.QueryContext(ctx, `SELECT canonical_bytes,index_bytes,artifact_bytes,
total_write_bytes,catalog_revision_sha256,guidance_tokens,compaction_compute_ms,
refinement_compute_ms FROM storage_samples ORDER BY storage_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	var first, previous, last storageRow
	churn := 0
	for rows.Next() {
		var current storageRow
		if err := rows.Scan(&current.canonical, &current.index, &current.artifact,
			&current.written, &current.revision, &current.guidance,
			&current.compaction, &current.refinement); err != nil {
			return nil, err
		}
		if count == 0 {
			first = current
		} else if previous.revision != current.revision {
			churn++
		}
		previous, last = current, current
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count < 2 {
		return map[string]any{"complete": false, "sample_count": count}, nil
	}
	canonicalGrowth := last.canonical - first.canonical
	var amplification any
	if canonicalGrowth > 0 {
		amplification = float64(last.written-first.written) / float64(canonicalGrowth)
	}
	return map[string]any{"complete": true, "sample_count": count,
		"storage_growth_bytes": map[string]int64{"canonical": canonicalGrowth,
			"index": last.index - first.index, "artifact": last.artifact - first.artifact},
		"write_amplification": amplification, "catalog_churn": churn,
		"guidance_overhead_tokens": last.guidance,
		"compaction_compute_ms":    last.compaction - first.compaction,
		"refinement_compute_ms":    last.refinement - first.refinement}, nil
}
