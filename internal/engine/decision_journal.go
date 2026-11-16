package engine

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func (engine *Engine) RecordMemoryDecision(ctx context.Context, event memory.MemoryDecisionEvent) (store.DecisionJournalReceipt, error) {
	return engine.writer.RecordMemoryDecision(ctx, event)
}

func (engine *Engine) RecordMemoryUsageEvent(ctx context.Context, event memory.MemoryUsageEvent) (store.DecisionJournalReceipt, error) {
	return engine.writer.RecordMemoryUsageEvent(ctx, event)
}

func (engine *Engine) MemoryDecisionTrace(ctx context.Context, decisionID string) (memory.MemoryDecisionTrace, error) {
	return engine.index.MemoryDecisionTrace(ctx, decisionID)
}

func (engine *Engine) RecordOutcomeCredit(ctx context.Context, event memory.OutcomeCreditEvent) (store.DecisionJournalReceipt, error) {
	return engine.writer.RecordOutcomeCredit(ctx, event)
}

func (engine *Engine) OutcomeCredits(ctx context.Context, decisionID string) ([]memory.OutcomeCreditEvent, error) {
	return engine.index.OutcomeCredits(ctx, decisionID)
}

func (engine *Engine) MemoryOperatorReport(ctx context.Context, decisionID string) (memory.MemoryOutcomeReport, error) {
	trace, err := engine.index.MemoryDecisionTrace(ctx, decisionID)
	if err != nil {
		return memory.MemoryOutcomeReport{}, err
	}
	credits, err := engine.index.OutcomeCredits(ctx, decisionID)
	if err != nil {
		return memory.MemoryOutcomeReport{}, err
	}
	return memory.BuildMemoryOutcomeReport("operator-report:"+trace.Decision.DecisionID, trace, credits)
}
