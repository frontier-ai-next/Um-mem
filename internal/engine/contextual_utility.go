package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type ContextualUtilityRequest struct {
	MemoryID                   string
	TaskFamily                 string
	Stage                      string
	ProjectID                  string
	HostID                     string
	ProfileID                  string
	MinimumSupport             int
	MinimumIndependentOutcomes int
}

// ContextualUtilitySummary uses verified outcome credits only as shadow
// evaluation in one exact task context.
func (engine *Engine) ContextualUtilitySummary(ctx context.Context,
	request ContextualUtilityRequest) (memory.ContextualUtilitySummary, error) {
	request.MemoryID = strings.TrimSpace(request.MemoryID)
	request.TaskFamily = strings.TrimSpace(request.TaskFamily)
	request.Stage = strings.TrimSpace(request.Stage)
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.HostID = strings.TrimSpace(request.HostID)
	request.ProfileID = strings.TrimSpace(request.ProfileID)
	for _, value := range []string{request.MemoryID, request.TaskFamily, request.Stage,
		request.ProjectID, request.HostID, request.ProfileID} {
		if value == "" {
			return memory.ContextualUtilitySummary{}, errors.New("contextual utility requires exact nonempty dimensions")
		}
	}
	if request.MinimumSupport == 0 {
		request.MinimumSupport = 3
	}
	if request.MinimumIndependentOutcomes == 0 {
		request.MinimumIndependentOutcomes = 2
	}
	if request.MinimumSupport < 1 || request.MinimumIndependentOutcomes < 1 {
		return memory.ContextualUtilitySummary{}, errors.New("contextual utility thresholds must be positive")
	}
	credits, err := engine.index.OutcomeCreditsForMemory(ctx, request.MemoryID)
	if err != nil {
		return memory.ContextualUtilitySummary{}, err
	}
	identity := strings.Join([]string{engine.config.WorkspaceID, request.MemoryID,
		request.TaskFamily, request.Stage, request.ProjectID, request.HostID, request.ProfileID}, "|")
	digest := sha256.Sum256([]byte(identity))
	summary := memory.ContextualUtilitySummary{SummaryID: "contextual-utility:" + hex.EncodeToString(digest[:]),
		WorkspaceID: engine.config.WorkspaceID, MemoryID: request.MemoryID,
		TaskFamily: request.TaskFamily, Stage: request.Stage, ProjectID: request.ProjectID,
		HostID: request.HostID, ProfileID: request.ProfileID,
		MinimumSupport:             request.MinimumSupport,
		MinimumIndependentOutcomes: request.MinimumIndependentOutcomes,
		RankingMode:                "shadow_only"}
	domains := make(map[string]bool)
	for _, credit := range credits {
		if credit.TaskFamily != request.TaskFamily || credit.Stage != request.Stage ||
			credit.ProjectID != request.ProjectID || credit.HostID != request.HostID ||
			credit.ProfileID != request.ProfileID {
			continue
		}
		if summary.OldestCreditAt == "" || credit.CreatedAt < summary.OldestCreditAt {
			summary.OldestCreditAt = credit.CreatedAt
		}
		if credit.CreatedAt > summary.NewestCreditAt {
			summary.NewestCreditAt = credit.CreatedAt
		}
		switch credit.Outcome {
		case "helped":
			summary.HelpedCount++
			domains[credit.IndependenceDomain] = true
		case "hurt":
			summary.HurtCount++
			domains[credit.IndependenceDomain] = true
		case "neutral":
			summary.NeutralCount++
		}
	}
	summary.SupportCount = summary.HelpedCount + summary.HurtCount
	summary.OutcomeDiversity = len(domains)
	if summary.SupportCount > 0 {
		summary.ShadowUtility = math.Round(float64(summary.HelpedCount-summary.HurtCount)/
			float64(summary.SupportCount)*1_000_000) / 1_000_000
	}
	summary.ShadowEligible = summary.SupportCount >= summary.MinimumSupport &&
		summary.OutcomeDiversity >= summary.MinimumIndependentOutcomes
	return summary, nil
}
