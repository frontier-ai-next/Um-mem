package memory

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const DefaultProcedureStaleAfterDays = 90

type ProcedureFreshness struct {
	Freshness  string `json:"freshness"`
	VerifiedAt string `json:"verified_at"`
	AgeDays    int    `json:"age_days"`
	Reason     string `json:"reason"`
}

func procedureText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func procedureDays(value any) int {
	if value == nil {
		return DefaultProcedureStaleAfterDays
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1
		}
		return DefaultProcedureStaleAfterDays
	case float64:
		maxInt := int(^uint(0) >> 1)
		if typed == 0 || math.IsNaN(typed) || math.IsInf(typed, 0) ||
			typed > float64(maxInt) || typed < float64(-maxInt-1) {
			return DefaultProcedureStaleAfterDays
		}
		return int(typed)
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(procedureText(value)))
	if err != nil {
		return DefaultProcedureStaleAfterDays
	}
	if parsed == 0 {
		return DefaultProcedureStaleAfterDays
	}
	return parsed
}

func procedureTime(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// EvaluateProcedureFreshness follows the Python procedure labels and reasons.
// The caller supplies the current environment descriptor and clock so the
// result can be checked against a stable snapshot.
func EvaluateProcedureFreshness(metadata map[string]any, descriptor map[string]any,
	now time.Time) ProcedureFreshness {
	verifiedAt := strings.TrimSpace(procedureText(metadata["verified_at"]))
	staleAfter := procedureDays(metadata["stale_after_days"])
	confirmed, validTime := procedureTime(verifiedAt)
	ageDays := 0
	if validTime {
		ageDays = max(0, int(now.UTC().Sub(confirmed).Hours()/24))
	}
	result := ProcedureFreshness{VerifiedAt: verifiedAt, AgeDays: ageDays}
	if fingerprint, ok := metadata["fingerprint"].(map[string]any); ok && descriptor != nil {
		keys := make([]string, 0, len(descriptor))
		for key := range descriptor {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		changed := make([]string, 0)
		for _, key := range keys {
			oldValue := procedureText(fingerprint[key])
			newValue := procedureText(descriptor[key])
			if oldValue != newValue {
				if oldValue == "" {
					oldValue = "-"
				}
				if newValue == "" {
					newValue = "-"
				}
				changed = append(changed, key+": "+oldValue+" -> "+newValue)
			}
		}
		if len(changed) > 0 {
			result.Freshness = "stale"
			result.Reason = "the environment changed (" + strings.Join(changed, "; ") + ")"
			return result
		}
	}
	if verifiedAt == "" || !validTime {
		result.Freshness, result.Reason = "unverified", "nobody confirmed this procedure yet"
	} else if ageDays > staleAfter {
		result.Freshness, result.Reason = "stale", fmt.Sprintf("not confirmed for %d days", ageDays)
	} else if ageDays > max(1, staleAfter/3) {
		result.Freshness, result.Reason = "aging", fmt.Sprintf("last confirmed %d days ago", ageDays)
	} else {
		result.Freshness, result.Reason = "fresh", fmt.Sprintf("confirmed %d days ago", ageDays)
	}
	return result
}

// ProcedureEntry renders a canonical procedure as the public checklist shape.
func ProcedureEntry(record MemoryRecord, descriptor map[string]any, now time.Time) map[string]any {
	metadata := record.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	steps := make([]string, 0)
	switch values := metadata["steps"].(type) {
	case string:
		if values != "" {
			steps = append(steps, values)
		}
	case []string:
		steps = append(steps, values...)
	case []any:
		for _, value := range values {
			steps = append(steps, procedureText(value))
		}
	}
	freshness := EvaluateProcedureFreshness(metadata, descriptor, now)
	tags := append([]string{}, record.Tags...)
	return map[string]any{"record_id": record.BlockID, "title": record.Title,
		"purpose": record.Content, "steps": steps, "verify": procedureText(metadata["verify"]),
		"applies_to":  procedureText(metadata["applies_to"]),
		"verified_at": freshness.VerifiedAt, "age_days": freshness.AgeDays,
		"freshness": freshness.Freshness, "reason": freshness.Reason,
		"tags": tags, "recorded_at": record.RecordedAt}
}
