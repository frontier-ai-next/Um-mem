package engine

import (
	"context"
	"errors"
	"sort"

	"github.com/frontier-ai-next/Um-mem/internal/prospective"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func (engine *Engine) CreateProspectiveObligation(ctx context.Context, value memory.ProspectiveObligation,
	eventID, idempotencyKey string) (store.ProspectiveReceipt, error) {
	return engine.writer.CreateProspectiveObligation(ctx, value, eventID, idempotencyKey)
}

func (engine *Engine) GetProspectiveObligation(ctx context.Context, id string) (*memory.ProspectiveObligation, error) {
	return engine.index.GetProspectiveObligation(ctx, id)
}

// TransitionProspectiveObligation applies an evidence-backed transition that
// does not fire a trigger. Firing requires ApplyProspectiveLifecycleEvent.
func (engine *Engine) TransitionProspectiveObligation(ctx context.Context, id string,
	event memory.ProspectiveLifecycleEvent) (store.ProspectiveReceipt, error) {
	if event.EventType == "fire" {
		return store.ProspectiveReceipt{}, errors.New("firing requires trigger revalidation")
	}
	return engine.writer.TransitionProspectiveObligation(ctx, id, event)
}

func prospectiveForEvaluation(value memory.ProspectiveObligation) prospective.Obligation {
	convert := func(values []memory.ProspectivePredicate) []prospective.Predicate {
		result := make([]prospective.Predicate, 0, len(values))
		for _, item := range values {
			result = append(result, prospective.Predicate{
				Type: item.PredicateType, Field: item.Field, Operator: item.Operator, Value: item.Value})
		}
		return result
	}
	return prospective.Obligation{ID: value.ObligationID, Scope: value.Scope, ScopeID: value.ScopeID,
		Status: value.Status, Trigger: convert(value.TriggerPredicates), Suppress: convert(value.SuppressPredicates),
		Cancel: convert(value.CancelPredicates), SemanticCues: value.SemanticTriggerCues,
		EarliestAt: value.EarliestAt, DeadlineAt: value.DeadlineAt, ExpiresAt: value.ExpiresAt}
}

// ApplyProspectiveLifecycleEvent rechecks a new fire against current typed
// host state and its authenticated origin before attempting the versioned CAS.
func (engine *Engine) ApplyProspectiveLifecycleEvent(ctx context.Context, id string,
	event memory.ProspectiveLifecycleEvent, trigger *prospective.FireContext) (store.ProspectiveReceipt, error) {
	if event.EventType == "fire" {
		current, err := engine.index.GetProspectiveObligation(ctx, id)
		if err != nil {
			return store.ProspectiveReceipt{}, err
		}
		if current != nil && current.Status == "armed" {
			if trigger == nil {
				return store.ProspectiveReceipt{}, errors.New("new prospective fire requires trigger revalidation")
			}
			if _, err := prospective.RevalidateFire(prospectiveForEvaluation(*current), *trigger); err != nil {
				return store.ProspectiveReceipt{}, err
			}
		}
	}
	return engine.writer.TransitionProspectiveObligation(ctx, id, event)
}

// ProspectiveTriggerCandidates returns bounded advisory trigger decisions.
func (engine *Engine) ProspectiveTriggerCandidates(ctx context.Context, trigger prospective.Context,
	limit int) ([]prospective.Decision, error) {
	values, err := engine.index.ProspectiveObligations(ctx, limit)
	if err != nil {
		return nil, err
	}
	decisions := make([]prospective.Decision, 0, len(values))
	for _, value := range values {
		decision, err := prospective.Evaluate(value, trigger)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

func (engine *Engine) ProspectiveIntervention(ctx context.Context, id string) (string, error) {
	value, err := engine.index.GetProspectiveObligation(ctx, id)
	if err != nil {
		return "", err
	}
	if value == nil {
		return "", errors.New("prospective obligation is missing")
	}
	if value.Status != "fired" {
		return "", errors.New("status_fired_required")
	}
	if value.RequiredIntervention == "" {
		return "", errors.New("fired obligation has no intervention payload")
	}
	return value.RequiredIntervention, nil
}

func (engine *Engine) ProspectiveTelemetryReport(ctx context.Context, limit int) (memory.ProspectiveTelemetryReport, error) {
	values, err := engine.index.ProspectiveTelemetryObligations(ctx, limit)
	if err != nil {
		return memory.ProspectiveTelemetryReport{}, err
	}
	byID := make(map[string]memory.ProspectiveObligation, len(values))
	for _, value := range values {
		if _, exists := byID[value.ObligationID]; exists {
			return memory.ProspectiveTelemetryReport{}, errors.New("prospective telemetry obligation ids must be unique")
		}
		byID[value.ObligationID] = value
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	report := memory.ProspectiveTelemetryReport{Entries: make([]memory.ProspectiveTelemetryEntry, 0, len(ids)),
		Channel: "prospective_obligation_telemetry"}
	for _, id := range ids {
		value := byID[id]
		report.Entries = append(report.Entries, memory.ProspectiveTelemetryEntry{ObligationID: id, Status: value.Status,
			FalseActivationCount:       value.Telemetry.FalsePositiveCount,
			MissedTriggerCount:         value.Telemetry.MissedTriggerCount,
			OverloadedTriggerCount:     value.Telemetry.OverloadedTriggerCount,
			AttentionInterruptionCount: value.Telemetry.AttentionInterruptionCount,
			DuplicateFireCount:         value.Telemetry.DuplicateFireCount,
			LateTriggerCount:           value.Telemetry.LateTriggerCount})
	}
	return report, nil
}
