package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestObserveRuntimeCostCLIUsesProviderCountersAndStableIdentity(t *testing.T) {
	root := t.TempDir()
	requestPath := filepath.Join(root, "runtime-cost.json")
	write := func(total int64) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{
			"observation_id": "host-turn-1", "input_tokens": 120,
			"cached_input_tokens": 25, "total_tokens": total,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(requestPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(160)
	args := []string{"observe-runtime-cost", "--repo-root", root, "--workspace", "alpha",
		"--backend", "mnemosyne", "--request-file", requestPath}
	for attempt := 0; attempt < 2; attempt++ {
		var output bytes.Buffer
		if err := run(context.Background(), args, &output); err != nil {
			t.Fatal(err)
		}
		var report struct {
			ObservationID      string `json:"observation_id"`
			Basis              string `json:"basis"`
			MemorySegmentCache string `json:"memory_segment_cache"`
			Cost               struct {
				Categories map[string]struct {
					Tokens *int64 `json:"tokens"`
				} `json:"categories"`
			} `json:"cost"`
			PrefixCache struct {
				Sources map[string]struct {
					SampleCount int `json:"sample_count"`
				} `json:"sources"`
			} `json:"prefix_cache"`
		}
		if err := json.Unmarshal(output.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if report.ObservationID != "host-turn-1" || report.Basis != "provider_reported" ||
			report.MemorySegmentCache != "unavailable" || report.Cost.Categories["model"].Tokens == nil ||
			*report.Cost.Categories["model"].Tokens != 160 ||
			report.PrefixCache.Sources["request"].SampleCount != 1 {
			t.Fatalf("runtime cost report changed on attempt %d: %+v", attempt, report)
		}
	}
	write(161)
	var output bytes.Buffer
	if err := run(context.Background(), args, &output); err == nil {
		t.Fatal("a changed provider total reused the same observation identity")
	}
}
