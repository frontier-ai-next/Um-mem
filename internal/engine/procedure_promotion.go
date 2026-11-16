package engine

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// ProcedurePromotionGate evaluates the selected workspace's durable outcome
// credits against independent verifier events before a procedure can be
// promoted. The publication path must also check the procedure's source
// episodes and current canonical head.
func (engine *Engine) ProcedurePromotionGate(ctx context.Context,
	memoryID, taskSignature string) (memory.ProcedurePromotionGate, error) {
	credits, err := engine.index.OutcomeCreditsForMemory(ctx, memoryID)
	if err != nil {
		return memory.ProcedurePromotionGate{}, err
	}
	resolver, err := ledger.OpenResolver(engine.config.RepoRoot, engine.config.WorkspaceID)
	if err != nil {
		return memory.ProcedurePromotionGate{}, err
	}
	return resolver.VerifiedProcedurePromotionGate(memoryID, engine.config.ProfileID,
		taskSignature, credits)
}
