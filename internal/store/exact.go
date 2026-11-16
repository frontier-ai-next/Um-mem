package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// ReadExactRecords resolves explicit record IDs and indexed terms against
// committed canonical payloads, then applies the recall safety policy.
func (index *Index) ReadExactRecords(ctx context.Context, plan memory.RecallPlan) ([]memory.MemoryRecord, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.WorkspaceID != index.workspaceID || plan.ProfileID != index.profileID {
		return nil, errors.New("exact recall scope does not match canonical index")
	}
	historyQuery := plan.TemporalMode == "historical" || plan.AsOf != "" || plan.KnownAsOf != ""
	knownBound, _, err := parseTemporalTime(plan.KnownAsOf)
	if err != nil {
		return nil, err
	}
	eventBound, _, err := parseTemporalTime(plan.AsOf)
	if err != nil {
		return nil, err
	}
	limit := plan.Limit
	if limit == 0 {
		limit = 10
	}
	now := time.Now().UTC()
	tx, err := index.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	selectedIDs, err := index.resolveExactIDsTx(ctx, tx, plan)
	if err != nil {
		return nil, err
	}
	var topology *catalog.MembershipSnapshot
	if index.topology != nil {
		topology, err = index.topology.OpenMembershipSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		defer topology.Close()
	}
	results := make([]memory.MemoryRecord, 0, min(limit, len(selectedIDs)))
	for _, recordID := range selectedIDs {
		record, err := index.readStoredRecordTx(ctx, tx, recordID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			continue
		}
		if historyQuery {
			if plan.RevisionID != "" && record.RevisionID != plan.RevisionID {
				continue
			}
			if plan.KnownAsOf != "" {
				committed, err := index.committedByTx(ctx, tx, record.BlockID, knownBound)
				if err != nil {
					return nil, err
				}
				if !committed {
					continue
				}
				if record.CanonicalKey != "" {
					headID, err := index.knownHeadTx(ctx, tx, record.CanonicalKey, knownBound)
					if err != nil {
						return nil, err
					}
					if headID != record.BlockID {
						continue
					}
				}
			} else if plan.AsOf != "" && plan.TemporalMode == "current" && record.CanonicalKey != "" {
				headID, err := index.eventHeadTx(ctx, tx, record.CanonicalKey, eventBound)
				if err != nil {
					return nil, err
				}
				if headID != record.BlockID {
					continue
				}
			}
			projected, matches, err := index.eventMatchesTx(ctx, tx, *record, plan)
			if err != nil {
				return nil, err
			}
			if !matches {
				continue
			}
			record = &projected
		} else {
			current, err := index.isCurrentRecordTx(ctx, tx, *record)
			if err != nil {
				return nil, err
			}
			if !current {
				continue
			}
		}
		if topology != nil {
			effective, err := topology.EffectiveRecord(ctx, *record)
			if err != nil {
				return nil, err
			}
			record = &effective
		}
		if !memory.EligibleForRecall(*record, plan, now) {
			continue
		}
		matchedTags := true
		for _, tag := range plan.Tags {
			found := false
			for _, actual := range record.Tags {
				if tag == actual {
					found = true
					break
				}
			}
			if !found {
				matchedTags = false
				break
			}
		}
		if !matchedTags {
			continue
		}
		if plan.KnownAsOf == "" && (!historyQuery || plan.AsOf != "") {
			dependencies, err := index.validateDependenciesTx(ctx, tx, record.BlockID, DependencyOptions{AsOf: plan.AsOf})
			if err != nil {
				return nil, err
			}
			if dependencies.Status != "valid" {
				continue
			}
		}
		results = append(results, *record)
		if len(results) == limit {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}
