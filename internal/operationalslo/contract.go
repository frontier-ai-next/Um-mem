package operationalslo

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
)

var Backends = []string{"mempalace", "hindsight", "mnemosyne", "zvec"}
var CorpusTiers = []string{"1k", "10k", "100k"}
var ConcurrencyLevels = []int{1, 8, 32}
var QueryModes = []string{"cold_exact", "cold_hybrid", "cold_temporal", "cold_reconstructive",
	"warm_exact", "warm_hybrid", "warm_temporal", "warm_reconstructive", "write", "correct", "erase", "rebuild"}

var baseP95 = map[string]int{"cold_exact": 2000, "cold_hybrid": 3000,
	"cold_temporal": 3000, "cold_reconstructive": 8000, "warm_exact": 250,
	"warm_hybrid": 750, "warm_temporal": 750, "warm_reconstructive": 3000,
	"write": 1000, "correct": 1500, "erase": 3000, "rebuild": 30000}
var tierScale = map[string]float64{"1k": 1, "10k": 2, "100k": 5}
var concurrencyScale = map[int]float64{1: 1, 8: 1.75, 32: 3.5}

type Threshold struct {
	Backend                string  `json:"backend"`
	CorpusTier             string  `json:"corpus_tier"`
	Concurrency            int     `json:"concurrency"`
	QueryMode              string  `json:"query_mode"`
	P50MS                  int     `json:"p50_ms"`
	P95MS                  int     `json:"p95_ms"`
	P99MS                  int     `json:"p99_ms"`
	MinThroughputPerSecond float64 `json:"min_throughput_per_second"`
	MaxQueueLagMS          int     `json:"max_queue_lag_ms"`
	MaxProjectionLagMS     int     `json:"max_projection_lag_ms"`
	RTOMS                  int     `json:"rto_ms"`
	MaxMemoryTurnFraction  float64 `json:"max_memory_turn_fraction"`
}

func Matrix() []Threshold {
	rows := make([]Threshold, 0, len(Backends)*len(CorpusTiers)*len(ConcurrencyLevels)*len(QueryModes))
	for _, backend := range Backends {
		backendScale := 1.0
		if backend != "mempalace" {
			backendScale = 1.25
		}
		for _, tier := range CorpusTiers {
			for _, concurrency := range ConcurrencyLevels {
				scale := backendScale * tierScale[tier] * concurrencyScale[concurrency]
				for _, mode := range QueryModes {
					p95 := max(1, int(math.RoundToEven(float64(baseP95[mode])*scale)))
					fraction := 0.25
					if strings.Contains(mode, "reconstructive") {
						fraction = 0.75
					}
					rtoScale := 2
					if mode == "rebuild" {
						rtoScale = 4
					}
					rows = append(rows, Threshold{Backend: backend, CorpusTier: tier,
						Concurrency: concurrency, QueryMode: mode,
						P50MS: max(1, int(math.RoundToEven(float64(p95)*0.55))),
						P95MS: p95, P99MS: max(p95, int(math.RoundToEven(float64(p95)*1.7))),
						MinThroughputPerSecond: max(0.01, math.RoundToEven(float64(concurrency)*1000/float64(p95)*10000)/10000),
						MaxQueueLagMS:          max(1000, p95*2), MaxProjectionLagMS: max(2000, p95*3),
						RTOMS: max(5000, p95*rtoScale), MaxMemoryTurnFraction: fraction})
				}
			}
		}
	}
	return rows
}

func pythonFloat(value float64) jsoncompatNumber {
	formatted := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(formatted, ".") {
		formatted += ".0"
	}
	return jsoncompatNumber(formatted)
}

// jsoncompatNumber retains Python's float-vs-integer JSON distinction in hashes.
type jsoncompatNumber string

func (value jsoncompatNumber) MarshalJSON() ([]byte, error) { return []byte(value), nil }

var contractRevisionOnce sync.Once
var contractRevision string
var contractRevisionErr error

func ContractRevision() (string, error) {
	contractRevisionOnce.Do(func() {
		contractRevision, contractRevisionErr = computeContractRevision()
	})
	return contractRevision, contractRevisionErr
}

func computeContractRevision() (string, error) {
	rows := Matrix()
	payload := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		payload = append(payload, map[string]any{
			"backend": row.Backend, "corpus_tier": row.CorpusTier,
			"concurrency": row.Concurrency, "query_mode": row.QueryMode,
			"p50_ms": row.P50MS, "p95_ms": row.P95MS, "p99_ms": row.P99MS,
			"min_throughput_per_second": pythonFloat(row.MinThroughputPerSecond),
			"max_queue_lag_ms":          row.MaxQueueLagMS,
			"max_projection_lag_ms":     row.MaxProjectionLagMS,
			"rto_ms":                    row.RTOMS,
			"max_memory_turn_fraction":  pythonFloat(row.MaxMemoryTurnFraction),
		})
	}
	encoded, err := jsoncompat.MarshalPython(payload, true)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("service-slo:sha256:%x", sha256.Sum256(encoded)), nil
}
