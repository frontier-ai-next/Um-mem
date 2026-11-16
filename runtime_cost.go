package ummem

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
	"github.com/frontier-ai-next/Um-mem/internal/operationalslo"
)

// RuntimeCostObservation contains only counters reported by the model host.
// Unknown measurements remain nil; no token savings are inferred.
type RuntimeCostObservation struct {
	ObservationID     string `json:"observation_id"`
	InputTokens       *int64 `json:"input_tokens"`
	TotalTokens       *int64 `json:"total_tokens"`
	CachedInputTokens *int64 `json:"cached_input_tokens"`
}

func (client *Client) ObserveRuntimeCost(ctx context.Context,
	request RuntimeCostObservation) (map[string]any, error) {
	if client == nil || client.Engine == nil {
		return nil, errors.New("runtime cost requires an open client")
	}
	if strings.TrimSpace(request.ObservationID) == "" {
		return nil, errors.New("runtime cost requires a stable observation identity")
	}
	workspace, profile := client.Scope()
	identity := localservice.Identity{RepoRoot: client.RepoRoot(), WorkspaceID: workspace,
		ProfileID: profile, Backend: client.BackendID()}
	path, err := localservice.SLOPath(identity)
	if err != nil {
		return nil, err
	}
	ledger, err := operationalslo.OpenLedger(path, identity.Backend)
	if err != nil {
		return nil, err
	}
	defer ledger.Close()
	if err := ledger.RecordCost(ctx, operationalslo.CostSample{
		Category: "model", Tokens: request.TotalTokens,
		ObservationID: request.ObservationID,
	}); err != nil {
		return nil, err
	}
	if err := ledger.RecordPrefixCache(ctx, operationalslo.PrefixCacheSample{
		Source: "request", EligibleTokens: request.InputTokens,
		ReusedTokens: request.CachedInputTokens, InputTokens: request.InputTokens,
		ObservationID: request.ObservationID,
	}); err != nil {
		return nil, err
	}
	cost, err := ledger.HeadlineCostReport(ctx)
	if err != nil {
		return nil, err
	}
	prefix, err := ledger.PrefixCacheReport(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"observation_id": request.ObservationID,
		"cost": cost, "prefix_cache": prefix, "basis": "provider_reported",
		"memory_segment_cache": "unavailable"}, nil
}
