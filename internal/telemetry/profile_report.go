package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/gofrs/flock"
)

func readEventRows(repoRoot string, limit int) ([]map[string]any, int, error) {
	if limit < 0 || limit > 100_000 {
		return nil, 0, errors.New("profile report limit must be 0-100000")
	}
	if limit == 0 {
		return []map[string]any{}, 0, nil
	}
	path := Path(repoRoot)
	paths := []string{path + ".3", path + ".2", path + ".1", path}
	available := false
	for _, candidate := range paths {
		if _, err := os.Stat(candidate); err == nil {
			available = true
			break
		} else if !os.IsNotExist(err) {
			return nil, 0, err
		}
	}
	if !available {
		return []map[string]any{}, 0, nil
	}
	lock := flock.New(path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, 0, err
	}
	if !locked {
		return nil, 0, context.DeadlineExceeded
	}
	defer lock.Unlock()
	rows := make([]map[string]any, 0)
	invalid := 0
	for _, candidate := range paths {
		file, err := os.Open(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, invalid, err
		}
		reader := bufio.NewReader(file)
		for {
			line, readErr := reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				if len(line) > 4<<20 {
					invalid++
				} else {
					decoder := json.NewDecoder(bytes.NewReader(line))
					decoder.UseNumber()
					var row map[string]any
					if err := decoder.Decode(&row); err != nil || row == nil {
						invalid++
					} else if hash, ok := row["event_sha256"].(string); !ok {
						invalid++
					} else {
						delete(row, "event_sha256")
						actual, hashErr := eventHash(row)
						if hashErr != nil || hash != actual || number(row["schema_version"]) != 1 {
							invalid++
						} else {
							row["event_sha256"] = hash
							rows = append(rows, row)
							if len(rows) > limit {
								rows = rows[len(rows)-limit:]
							}
						}
					}
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				file.Close()
				return nil, invalid, readErr
			}
		}
		if err := file.Close(); err != nil {
			return nil, invalid, err
		}
	}
	return rows, invalid, nil
}

func number(value any) float64 {
	switch item := value.(type) {
	case json.Number:
		result, _ := item.Float64()
		return result
	case float64:
		return item
	case int:
		return float64(item)
	}
	return 0
}

func textField(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

func rounded(value float64) float64 { return math.Round(value*1000) / 1000 }

func profilePercentile(values []float64, fraction float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := slices.Clone(values)
	sort.Float64s(ordered)
	index := max(0, min(len(ordered)-1, int(math.Ceil(float64(len(ordered))*fraction))-1))
	return rounded(ordered[index])
}

type profileStats struct {
	count, failed, inputTokens, outputTokens, maxInFlight int
	processCPU, threadCPU                                 float64
	durations                                             []float64
	components                                            map[string]int
}

// ProfileReport verifies event hashes and summarizes the newest developer
// profile spans. It never returns recorded input or output content.
func ProfileReport(repoRoot string, limit int) (map[string]any, error) {
	rows, invalid, err := readEventRows(repoRoot, limit)
	if err != nil {
		return nil, err
	}
	byOperation := make(map[string]*profileStats)
	byComponent := make(map[string]int)
	durations := make([]float64, 0)
	slowest := make([]map[string]any, 0)
	maxInFlight, parallel := 0, 0
	processCPU, threadCPU := 0.0, 0.0
	for _, row := range rows {
		if textField(row, "event_type") != "memory_profile" {
			continue
		}
		operation, component := textField(row, "operation"), textField(row, "component")
		if operation == "" {
			operation = "unknown"
		}
		if component == "" {
			component = "unknown"
		}
		stats := byOperation[operation]
		if stats == nil {
			stats = &profileStats{components: make(map[string]int)}
			byOperation[operation] = stats
		}
		stats.count++
		status := textField(row, "status")
		if status == "failed" || status == "blocked" || status == "unavailable" {
			stats.failed++
		}
		duration := number(row["duration_ms"])
		stats.durations = append(stats.durations, duration)
		durations = append(durations, duration)
		if data, ok := row["data"].(map[string]any); ok {
			if input, ok := data["input"].(map[string]any); ok {
				stats.inputTokens += int(number(input["estimated_tokens"]))
			}
			if output, ok := data["output"].(map[string]any); ok {
				stats.outputTokens += int(number(output["estimated_tokens"]))
			}
		}
		if resources, ok := row["resources"].(map[string]any); ok {
			cpu, thread := number(resources["process_cpu_ms"]), number(resources["thread_cpu_ms"])
			stats.processCPU += cpu
			stats.threadCPU += thread
			processCPU += cpu
			threadCPU += thread
		}
		if concurrency, ok := row["parallelism"].(map[string]any); ok {
			active := int(number(concurrency["max_in_flight"]))
			stats.maxInFlight = max(stats.maxInFlight, active)
			maxInFlight = max(maxInFlight, active)
			if active > 1 {
				parallel++
			}
		}
		stats.components[component]++
		byComponent[component]++
		slowest = append(slowest, map[string]any{"event_id": textField(row, "event_id"),
			"operation": operation, "component": component, "backend": textField(row, "backend"),
			"run_id": textField(row, "run_id"), "user_id": textField(row, "user_id"),
			"chat_id": textField(row, "chat_id"), "duration_ms": duration,
			"status": status, "reason": textField(row, "reason"), "outcome": textField(row, "outcome")})
	}
	sort.SliceStable(slowest, func(left, right int) bool {
		return number(slowest[left]["duration_ms"]) > number(slowest[right]["duration_ms"])
	})
	if len(slowest) > 10 {
		slowest = slowest[:10]
	}
	operations := make(map[string]any, len(byOperation))
	for name, stats := range byOperation {
		total, maximum := 0.0, 0.0
		for _, duration := range stats.durations {
			total += duration
			maximum = max(maximum, duration)
		}
		operations[name] = map[string]any{"count": stats.count, "failed": stats.failed,
			"input_estimated_tokens":  stats.inputTokens,
			"output_estimated_tokens": stats.outputTokens,
			"process_cpu_ms":          rounded(stats.processCPU),
			"thread_cpu_ms":           rounded(stats.threadCPU),
			"max_in_flight":           stats.maxInFlight, "components": stats.components,
			"total_ms": rounded(total), "p50_ms": profilePercentile(stats.durations, .5),
			"p95_ms": profilePercentile(stats.durations, .95), "max_ms": rounded(maximum)}
	}
	total := 0.0
	for _, value := range durations {
		total += value
	}
	return map[string]any{"schema_version": 1, "invalid_event_count": invalid,
		"dropped_event_count": 0, "profiled_operation_count": len(durations),
		"total_duration_ms": rounded(total), "total_process_cpu_ms": rounded(processCPU),
		"total_thread_cpu_ms": rounded(threadCPU), "p50_ms": profilePercentile(durations, .5),
		"p95_ms": profilePercentile(durations, .95), "max_in_flight": maxInFlight,
		"parallelism_observed_count": parallel, "by_operation": operations,
		"by_component": byComponent, "slowest": slowest}, nil
}
