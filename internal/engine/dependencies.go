package engine

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// ValidateDependencies returns a bounded receipt for exact derived_from links.
// It does not promote the record or judge the quality of its evidence.
func (engine *Engine) ValidateDependencies(ctx context.Context, recordID string,
	options store.DependencyOptions) (memory.DependencyValidation, error) {
	return engine.index.ValidateDependencies(ctx, recordID, options)
}
