package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

const ManagerWorkspaceID = "um-mem-manager"

// RecallManager queries the dedicated manager workspace only for an explicitly
// bound user. Returned records are forced to advisory delivery.
func (engine *Engine) RecallManager(ctx context.Context, plan memory.RecallPlan,
	userID string) ([]memory.MemoryRecord, error) {
	if userID == "" || userID != strings.TrimSpace(userID) ||
		engine.config.WorkspaceID == ManagerWorkspaceID {
		return nil, errors.New("manager recall requires a bound user and a non-manager workspace")
	}
	config := engine.config
	config.WorkspaceID = ManagerWorkspaceID
	manager, err := Open(config)
	if err != nil {
		return nil, err
	}
	defer manager.Close()
	managerPlan := memory.NewRecallPlan(plan.Query)
	managerPlan.WorkspaceID, managerPlan.ProfileID = ManagerWorkspaceID, engine.config.ProfileID
	managerPlan.RunID, managerPlan.Tags = plan.RunID, append([]string{}, plan.Tags...)
	managerPlan.RecordIDs = append([]string{}, plan.RecordIDs...)
	managerPlan.ScopeChain = []string{"user_global:" + userID}
	managerPlan.SearchAll, managerPlan.SourceHorizon = true, plan.SourceHorizon
	managerPlan.IncludeCandidates = plan.IncludeCandidates
	managerPlan.TemporalMode, managerPlan.AsOf, managerPlan.KnownAsOf =
		plan.TemporalMode, plan.AsOf, plan.KnownAsOf
	managerPlan.RevisionID = plan.RevisionID
	managerPlan.RetrievalMode, managerPlan.MaxTokens, managerPlan.Limit =
		plan.RetrievalMode, plan.MaxTokens, plan.Limit
	managerPlan.Reason = "explicit_manager_recall"
	if err := managerPlan.Validate(); err != nil {
		return nil, err
	}
	hits, err := manager.Recall(ctx, managerPlan)
	if err != nil {
		return nil, err
	}
	blocks := manager.current.Load().byID
	result := make([]memory.MemoryRecord, 0, len(hits))
	for _, record := range hits {
		if record.WorkspaceID != ManagerWorkspaceID || record.ProfileID != engine.config.ProfileID ||
			record.Scope != "user_global" || record.UserID != userID || record.BlockMembership == nil {
			continue
		}
		block, ok := blocks[record.BlockMembership.PrimaryBlockID]
		if !ok || block.Scope != "user_global" || block.IsolationPolicy["cross_workspace"] != true {
			continue
		}
		record.Authority = "advisory"
		metadata := make(map[string]any, len(record.Metadata)+2)
		for key, value := range record.Metadata {
			metadata[key] = value
		}
		metadata["authority"], metadata["memory_level"] = "advisory", "manager"
		record.Metadata = metadata
		result = append(result, record)
	}
	return result, nil
}
