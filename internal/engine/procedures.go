package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type ProcedureRequest struct {
	Query string
	Limit int
	RunID string
}

// ProcedureDescriptor is the Python-compatible environment stamp for public
// procedure writes and reads.
func (engine *Engine) ProcedureDescriptor() (map[string]any, error) {
	root, err := filepath.Abs(engine.config.RepoRoot)
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	model := strings.TrimSpace(os.Getenv("UM_MEM_SEMANTIC_MODEL"))
	if model == "" {
		model = "intfloat/multilingual-e5-large"
	}
	return map[string]any{"um_mem_version": "0.6.0", "semantic_model": model,
		"store": root, "workspace": engine.config.WorkspaceID}, nil
}

// Procedures renders safe current workspace procedures without limiting the
// search to a recent window. It does not grant authority.
func (engine *Engine) Procedures(ctx context.Context, request ProcedureRequest) ([]map[string]any, error) {
	if request.Limit < 1 || request.Limit > 128 {
		return nil, errors.New("procedure limit must be 1-128")
	}
	procedures, err := engine.index.CurrentKindRecords(ctx, engine.config.BackendID, "procedure")
	if err != nil {
		return nil, err
	}
	plan := memory.NewRecallPlan(request.Query)
	plan.WorkspaceID, plan.ProfileID = engine.config.WorkspaceID, engine.config.ProfileID
	plan.ScopeChain, plan.RunID, plan.IncludeCandidates =
		[]string{"workspace:" + engine.config.WorkspaceID}, request.RunID, true
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	candidates := make([]memory.MemoryRecord, 0)
	for _, record := range procedures {
		if !memory.EligibleForRecall(record, plan, now) {
			continue
		}
		dependency, err := engine.index.ValidateDependencies(ctx, record.BlockID, store.DependencyOptions{})
		if err != nil {
			return nil, err
		}
		if dependency.Status == "valid" {
			candidates = append(candidates, record)
		}
	}
	documents := make([]string, len(candidates))
	titles := make([]string, len(candidates))
	for index, record := range candidates {
		documents[index] = record.Title + "\n" + record.Content
		titles[index] = record.Title
	}
	ranked := memory.RankText(request.Query, documents, titles, request.Limit)
	descriptor, err := engine.ProcedureDescriptor()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(ranked))
	for _, row := range ranked {
		result = append(result, memory.ProcedureEntry(candidates[row.Index], descriptor, now))
	}
	return result, nil
}
