package engine

import (
	"context"
	"errors"

	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
)

// PlanHostState reads the versioned prospective index and records only hashed
// advisory evidence in the scheduler decision. It does not fire an obligation,
// mutate memory, or authorize any host action.
func (engine *Engine) PlanHostState(ctx context.Context, event scheduler.HostStateEvent) (scheduler.AdvisoryDecisionPlan, error) {
	if event.WorkspaceID != engine.config.WorkspaceID {
		return scheduler.AdvisoryDecisionPlan{}, errors.New("host-state workspace mismatch")
	}
	obligations, err := engine.index.ProspectiveObligations(ctx, 50)
	if err != nil {
		return scheduler.AdvisoryDecisionPlan{}, err
	}
	return scheduler.PlanFromObligations(event, obligations)
}
