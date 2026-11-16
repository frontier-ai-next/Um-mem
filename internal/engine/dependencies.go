package engine

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// ValidateDependencies returns a bounded receipt for exact derived_from links.
// It does not promote the record or judge the quality of its evidence.
func (engine *Engine) ValidateDependencies(ctx context.Context, recordID string,
	options store.DependencyOptions) (memory.DependencyValidation, error) {
	return engine.index.ValidateDependencies(ctx, recordID, options)
}
