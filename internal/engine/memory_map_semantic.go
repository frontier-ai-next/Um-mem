package engine

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/memorymap"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
	_ "modernc.org/sqlite"
)

func semanticMapEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("UM_MEM_SEMANTIC"))) {
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return true
	}
}

func semanticMapModel() string {
	if configured := strings.TrimSpace(os.Getenv("UM_MEM_SEMANTIC_MODEL")); configured != "" {
		return configured
	}
	return "intfloat/multilingual-e5-large"
}

func semanticMapFloor(path, model string) float64 {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return 0.80
	}
	var calibration struct {
		Model string   `json:"model"`
		Floor *float64 `json:"floor"`
	}
	if json.Unmarshal(encoded, &calibration) != nil ||
		calibration.Model != "" && calibration.Model != model ||
		calibration.Floor == nil || math.IsNaN(*calibration.Floor) {
		return 0.80
	}
	return max(0.80, *calibration.Floor)
}

func cosineMapVectors(left, right []float64) float64 {
	if len(left) == 0 || len(left) != len(right) {
		return 0
	}
	dot, normLeft, normRight := 0.0, 0.0, 0.0
	for i, value := range left {
		dot += value * right[i]
		normLeft += value * value
		normRight += right[i] * right[i]
	}
	if normLeft == 0 || normRight == 0 {
		return 0
	}
	return dot / (math.Sqrt(normLeft) * math.Sqrt(normRight))
}

func decodeMapVector(blob []byte) ([]float64, error) {
	if len(blob)%4 != 0 {
		return nil, errors.New("semantic map vector has an invalid byte length")
	}
	vector := make([]float64, len(blob)/4)
	for i := range vector {
		bits := binary.LittleEndian.Uint32(blob[i*4 : i*4+4])
		vector[i] = float64(math.Float32frombits(bits))
	}
	return vector, nil
}

func (engine *Engine) memoryMapSemantic(ctx context.Context,
	records []memorymap.RecordNode) (string, int, []memorymap.SemanticEdge, error) {
	empty := []memorymap.SemanticEdge{}
	if !semanticMapEnabled() {
		return "", 0, empty, nil
	}
	root, err := paths.WorkspaceMemoryRoot(engine.config.RepoRoot, engine.config.WorkspaceID)
	if err != nil {
		return "", 0, empty, err
	}
	indexPath := filepath.Join(root, "semantic", "embeddings.sqlite")
	if _, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) {
		return "", 0, empty, nil
	} else if err != nil {
		return "", 0, empty, err
	}
	db, err := sql.Open("sqlite", indexPath)
	if err != nil {
		return "", 0, empty, err
	}
	defer db.Close()
	model := semanticMapModel()
	var indexed int
	err = db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT record_id) FROM semantic_vectors
		WHERE model=? AND profile=?`, model, engine.config.ProfileID).Scan(&indexed)
	if err != nil {
		return "", 0, empty, err
	}
	if len(records) < 2 {
		return model, indexed, empty, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT record_id, vector FROM semantic_vectors
		WHERE model=? AND profile=? AND variant='full'`, model, engine.config.ProfileID)
	if err != nil {
		return "", 0, empty, err
	}
	defer rows.Close()
	vectors := map[string][]float64{}
	for rows.Next() {
		var id string
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			return "", 0, empty, err
		}
		vector, err := decodeMapVector(blob)
		if err != nil {
			return "", 0, empty, err
		}
		vectors[id] = vector
	}
	if err := rows.Err(); err != nil {
		return "", 0, empty, err
	}
	threshold := semanticMapFloor(filepath.Join(root, "semantic", "calibration.json"), model)
	type pair struct {
		score  float64
		first  string
		second string
	}
	pairs := []pair{}
	for i, first := range records {
		left := vectors[first.RecordID]
		if len(left) == 0 {
			continue
		}
		for _, second := range records[i+1:] {
			right := vectors[second.RecordID]
			if len(right) == 0 {
				continue
			}
			if score := cosineMapVectors(left, right); score >= threshold {
				pairs = append(pairs, pair{score, first.RecordID, second.RecordID})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		if pairs[i].first != pairs[j].first {
			return pairs[i].first > pairs[j].first
		}
		return pairs[i].second > pairs[j].second
	})
	degree := map[string]int{}
	edges := make([]memorymap.SemanticEdge, 0, len(pairs))
	for _, pair := range pairs {
		draw := degree[pair.first] < 2 && degree[pair.second] < 2
		if draw {
			degree[pair.first]++
			degree[pair.second]++
		}
		edges = append(edges, memorymap.SemanticEdge{Source: pair.first, Target: pair.second,
			Kind: "semantic", Weight: math.RoundToEven(pair.score*10000) / 10000, Draw: draw})
	}
	return model, indexed, edges, nil
}
