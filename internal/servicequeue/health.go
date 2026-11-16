package servicequeue

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (store *Store) Health(ctx context.Context, now time.Time) (Health, error) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return Health{}, err
	}
	defer conn.Close()
	result := Health{Lanes: make([]LaneHealth, 0, len(Lanes)), NoSilentDrop: true}
	for _, lane := range Lanes {
		policy := store.policies[lane]
		laneHealth := LaneHealth{Lane: lane, Capacity: policy.Capacity,
			TargetLatencyMS: policy.TargetLatencyMS}
		rows, err := conn.QueryContext(ctx, `SELECT state,COUNT(*) FROM work_items
 WHERE lane=? GROUP BY state`, lane)
		if err != nil {
			return Health{}, err
		}
		for rows.Next() {
			var state string
			var count int
			if err := rows.Scan(&state, &count); err != nil {
				rows.Close()
				return Health{}, err
			}
			switch state {
			case "pending":
				laneHealth.Pending = count
			case "running":
				laneHealth.Running = count
			case "completed":
				laneHealth.Completed = count
			case "poisoned":
				laneHealth.Poisoned = count
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return Health{}, err
		}
		if err := rows.Close(); err != nil {
			return Health{}, err
		}
		var oldest sql.NullString
		if err := conn.QueryRowContext(ctx, `SELECT MIN(submitted_at) FROM work_items
 WHERE lane=? AND state='pending'`, lane).Scan(&oldest); err != nil {
			return Health{}, err
		}
		if oldest.Valid && oldest.String != "" {
			submitted, err := time.Parse(time.RFC3339Nano, oldest.String)
			if err != nil {
				return Health{}, err
			}
			age := now.Sub(submitted)
			if age > 0 {
				laneHealth.OldestPendingAgeMS = int(age.Milliseconds())
			}
		}
		laneHealth.SLOBreached = laneHealth.OldestPendingAgeMS > laneHealth.TargetLatencyMS
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM admission_receipts
 WHERE lane=? AND status='backpressured'`, lane).Scan(&laneHealth.Backpressured); err != nil {
			return Health{}, err
		}
		result.Lanes = append(result.Lanes, laneHealth)
	}
	var readOnly string
	err = conn.QueryRowContext(ctx, `SELECT value FROM queue_metadata
 WHERE key='emergency_read_only'`).Scan(&readOnly)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Health{}, err
	}
	result.EmergencyReadOnly = readOnly == "1"
	return result, nil
}
