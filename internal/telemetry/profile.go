package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"runtime"
	runtimemetrics "runtime/metrics"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/google/uuid"
)

const maxProfileDepth = 3

type profileContextKey struct{}

type profileContext struct {
	spanID, profileID, userID, chatID, runID string
}

type profileCount struct{ active, peak int }

var profileCounts = struct {
	sync.Mutex
	byKey map[string]profileCount
}{byKey: make(map[string]profileCount)}

type ProfileOptions struct {
	Operation   string
	Component   string
	WorkspaceID string
	ProfileID   string
	UserID      string
	ChatID      string
	RunID       string
	StageID     string
	Backend     string
	Input       any
}

type ProfileSpan struct {
	repoRoot, operation, component, workspaceID, profileID string
	userID, chatID, runID, stageID, backend                string
	spanID, parentSpanID, concurrencyKey                   string
	started                                                time.Time
	startedCPU                                             float64
	startedAlloc                                           uint64
	inputSummary, outputSummary                            map[string]any
	stages                                                 []map[string]any
	status, decision, outcome, reason                      string
	maxInFlight                                            int
	ended                                                  bool
}

func profileDigest(value any) (string, int) {
	encoded, err := jsoncompat.MarshalPython(value, false)
	if err != nil {
		encoded = []byte(fmt.Sprintf("%T", value))
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), len(encoded)
}

// ProfileSummary retains bounded size, type and digest metadata without the
// source text or a rendered context. This shape is compatible with the Python
// developer profile trace for common JSON values.
func ProfileSummary(value any) map[string]any { return profileSummary(value, 0) }

func profileSummary(value any, depth int) map[string]any {
	if value == nil {
		return map[string]any{"type": "null"}
	}
	switch item := value.(type) {
	case bool:
		return map[string]any{"type": "bool", "value": item}
	case string:
		digest, size := profileDigest(item)
		return map[string]any{"type": "text", "chars": utf8.RuneCountInString(item),
			"bytes": len([]byte(item)), "estimated_tokens": memory.EstimateTokens(item),
			"sha256": digest, "serialized_bytes": size}
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return map[string]any{"type": "int", "value": item}
	case float32, float64:
		return map[string]any{"type": "float", "value": item}
	case map[string]any:
		fields := make([]string, 0, len(item))
		for key := range item {
			fields = append(fields, key)
		}
		slices.Sort(fields)
		visible := fields[:min(len(fields), 32)]
		digest, size := profileDigest(item)
		result := map[string]any{"type": "object", "field_count": len(fields),
			"fields": visible, "sha256": digest, "bytes": size,
			"estimated_tokens": max(1, size/4)}
		if depth < maxProfileDepth {
			summaries := make(map[string]any, len(visible))
			for _, key := range visible {
				summaries[key] = profileSummary(item[key], depth+1)
			}
			result["field_summaries"] = summaries
		}
		return result
	}
	reflected := reflect.ValueOf(value)
	if reflected.IsValid() && (reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array) {
		length := reflected.Len()
		items := make([]any, 0, min(length, 16))
		types := make(map[string]bool)
		for index := 0; index < length; index++ {
			element := reflected.Index(index).Interface()
			types[fmt.Sprintf("%T", element)] = true
			if index < 16 {
				items = append(items, element)
			}
		}
		itemTypes := make([]string, 0, len(types))
		for name := range types {
			itemTypes = append(itemTypes, name)
		}
		slices.Sort(itemTypes)
		digest, size := profileDigest(value)
		result := map[string]any{"type": "array", "item_count": length,
			"item_types": itemTypes, "sha256": digest, "bytes": size,
			"estimated_tokens": max(1, size/4)}
		if depth < maxProfileDepth {
			summaries := make([]map[string]any, 0, len(items))
			for _, item := range items {
				summaries = append(summaries, profileSummary(item, depth+1))
			}
			result["items"] = summaries
		}
		return result
	}
	digest, _ := profileDigest(value)
	return map[string]any{"type": fmt.Sprintf("%T", value), "sha256": digest}
}

func cumulativeGoCPU() float64 {
	samples := []runtimemetrics.Sample{{Name: "/cpu/classes/total:cpu-seconds"}}
	runtimemetrics.Read(samples)
	if samples[0].Value.Kind() != runtimemetrics.KindFloat64 {
		return 0
	}
	return samples[0].Value.Float64() * 1000
}

// StartProfile adds a nestable span to ctx. End must be called once; profile
// recording never changes the result of the measured memory operation.
func StartProfile(ctx context.Context, repoRoot string, options ProfileOptions) (context.Context, *ProfileSpan) {
	parent, _ := ctx.Value(profileContextKey{}).(profileContext)
	if options.ProfileID == "" {
		options.ProfileID = parent.profileID
	}
	if options.UserID == "" {
		options.UserID = parent.userID
	}
	if options.ChatID == "" {
		options.ChatID = parent.chatID
	}
	if options.RunID == "" {
		options.RunID = parent.runID
	}
	key := options.WorkspaceID + "|" + options.Backend
	profileCounts.Lock()
	count := profileCounts.byKey[key]
	count.active++
	count.peak = max(count.peak, count.active)
	profileCounts.byKey[key] = count
	profileCounts.Unlock()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	span := &ProfileSpan{repoRoot: repoRoot, operation: options.Operation,
		component: options.Component, workspaceID: options.WorkspaceID,
		profileID: options.ProfileID, userID: options.UserID, chatID: options.ChatID,
		runID: options.RunID, stageID: options.StageID, backend: options.Backend,
		spanID: uuid.NewString(), parentSpanID: parent.spanID, concurrencyKey: key,
		started: time.Now(), startedCPU: cumulativeGoCPU(), startedAlloc: stats.TotalAlloc,
		inputSummary: ProfileSummary(options.Input), outputSummary: map[string]any{"type": "unset"},
		stages: []map[string]any{}, status: "ok", maxInFlight: count.peak}
	bound := profileContext{spanID: span.spanID, profileID: span.profileID,
		userID: span.userID, chatID: span.chatID, runID: span.runID}
	return context.WithValue(ctx, profileContextKey{}, bound), span
}

func (span *ProfileSpan) SetOutput(value any) { span.outputSummary = ProfileSummary(value) }

func (span *ProfileSpan) AddStage(operation string, duration time.Duration, status string, details any) {
	if status == "" {
		status = "ok"
	}
	stage := map[string]any{"operation": operation,
		"duration_ms": float64(max(0, duration.Microseconds())) / 1000,
		"status":      status}
	if details != nil {
		stage["details"] = ProfileSummary(details)
	}
	span.stages = append(span.stages, stage)
}

func (span *ProfileSpan) SetResult(status, decision, outcome, reason string) {
	if status != "" {
		span.status = status
	}
	span.decision, span.outcome, span.reason = decision, outcome, reason
}

func (span *ProfileSpan) End(operationError error) {
	if span == nil || span.ended {
		return
	}
	span.ended = true
	duration := float64(max(0, time.Since(span.started).Microseconds())) / 1000
	profileCounts.Lock()
	count := profileCounts.byKey[span.concurrencyKey]
	span.maxInFlight = max(span.maxInFlight, count.peak)
	count.active--
	if count.active <= 0 {
		delete(profileCounts.byKey, span.concurrencyKey)
	} else {
		profileCounts.byKey[span.concurrencyKey] = count
	}
	profileCounts.Unlock()
	if operationError != nil {
		span.status = "failed"
	}
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	cpu := max(0, cumulativeGoCPU()-span.startedCPU)
	resources := map[string]any{"process_cpu_ms": cpu,
		"go_alloc_bytes": max(uint64(0), stats.TotalAlloc-span.startedAlloc)}
	errorName := ""
	if operationError != nil {
		errorName = fmt.Sprintf("%T", operationError)
	}
	_ = Record(span.repoRoot, Event{EventType: "memory_profile", WorkspaceID: span.workspaceID,
		ProfileID: span.profileID, UserID: span.userID, ChatID: span.chatID,
		RunID: span.runID, StageID: span.stageID, Backend: span.backend,
		Status: span.status, Error: errorName, Operation: span.operation,
		Component: span.component, Decision: span.decision, Outcome: span.outcome,
		Reason: span.reason, DurationMS: &duration, ParentSpanID: span.parentSpanID,
		Parallelism: map[string]any{"concurrency_key": span.concurrencyKey,
			"max_in_flight": span.maxInFlight}, Resources: resources,
		Data: map[string]any{"span_id": span.spanID, "input": span.inputSummary,
			"output": span.outputSummary, "stages": span.stages}})
}
