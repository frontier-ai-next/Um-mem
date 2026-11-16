package memory

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type MaterializedSourceRevision struct {
	SourceRecordID string `json:"source_record_id"`
	RevisionID     string `json:"revision_id"`
	SourceSHA256   string `json:"source_sha256"`
	ValidFrom      string `json:"valid_from"`
	ValidTo        string `json:"valid_to"`
}

func materializedSourceHash(record MemoryRecord) (string, error) {
	payload, err := jsoncompat.MarshalPython(map[string]any{
		"block_id": record.BlockID, "content": record.Content,
		"workspace_id": record.WorkspaceID, "scope": record.Scope,
		"authority": record.Authority, "valid_from": record.ValidFrom,
		"valid_to": record.ValidTo, "status": record.Status,
	}, false)
	if err != nil {
		return "", err
	}
	return consolidationHash(payload), nil
}

func MaterializedSourceRevisionFromRecord(record MemoryRecord) (MaterializedSourceRevision, error) {
	if strings.TrimSpace(record.BlockID) == "" || strings.TrimSpace(record.RevisionID) == "" {
		return MaterializedSourceRevision{}, errors.New("materialized source requires record and revision IDs")
	}
	hash, err := materializedSourceHash(record)
	if err != nil {
		return MaterializedSourceRevision{}, err
	}
	return MaterializedSourceRevision{SourceRecordID: record.BlockID, RevisionID: record.RevisionID,
		SourceSHA256: hash, ValidFrom: record.ValidFrom, ValidTo: record.ValidTo}, nil
}

type MaterializedViewRevision struct {
	ViewID              string                       `json:"view_id"`
	WorkspaceID         string                       `json:"workspace_id"`
	Content             string                       `json:"content"`
	Sources             []MaterializedSourceRevision `json:"sources"`
	EvidenceRefs        []EvidenceRef                `json:"evidence_refs"`
	Status              string                       `json:"status"`
	InvalidationReasons []string                     `json:"invalidation_reasons"`
}

func NewMaterializedViewRevision(viewID, workspaceID, content string,
	sources []MaterializedSourceRevision, evidenceRefs []EvidenceRef) (MaterializedViewRevision, error) {
	result := MaterializedViewRevision{}
	if strings.TrimSpace(viewID) == "" || strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(content) == "" {
		return result, errors.New("materialized view requires ID, workspace and content")
	}
	if len(sources) == 0 {
		return result, errors.New("materialized view requires sources")
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if strings.TrimSpace(source.SourceRecordID) == "" || strings.TrimSpace(source.RevisionID) == "" ||
			!validConsolidationHash(source.SourceSHA256) {
			return result, errors.New("materialized view requires valid source revisions")
		}
		if seen[source.SourceRecordID] {
			return result, errors.New("materialized view source IDs must be unique")
		}
		seen[source.SourceRecordID] = true
	}
	refs, err := uniqueEvidenceRefs(evidenceRefs, true)
	if err != nil {
		return result, err
	}
	return MaterializedViewRevision{ViewID: strings.TrimSpace(viewID), WorkspaceID: strings.TrimSpace(workspaceID),
		Content: strings.TrimSpace(content), Sources: slices.Clone(sources), EvidenceRefs: refs,
		Status: "active", InvalidationReasons: []string{}}, nil
}

func parseConsolidationInstant(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05", "2006-01-02"} {
		if instant, err := time.Parse(layout, value); err == nil {
			return instant.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid consolidation instant %q", value)
}

// Revalidate returns an updated view without changing the original. Any
// missing, changed, expired or retracted canonical source invalidates it.
func (view MaterializedViewRevision) Revalidate(sourceRecords []MemoryRecord,
	asOf string) (MaterializedViewRevision, error) {
	instant, err := parseConsolidationInstant(strings.TrimSpace(asOf))
	if err != nil {
		return MaterializedViewRevision{}, err
	}
	current := make(map[string]MemoryRecord, len(sourceRecords))
	for _, record := range sourceRecords {
		current[record.BlockID] = record
	}
	reasons := map[string]bool{}
	for _, source := range view.Sources {
		record, found := current[source.SourceRecordID]
		if !found {
			reasons["source_missing:"+source.SourceRecordID] = true
			continue
		}
		if record.WorkspaceID != view.WorkspaceID {
			reasons["source_workspace_changed:"+source.SourceRecordID] = true
		}
		if record.RevisionID != source.RevisionID {
			reasons["source_revision_changed:"+source.SourceRecordID] = true
		}
		hash, err := materializedSourceHash(record)
		if err != nil {
			return MaterializedViewRevision{}, err
		}
		if hash != source.SourceSHA256 {
			reasons["source_hash_changed:"+source.SourceRecordID] = true
		}
		switch record.Status {
		case "quarantined", "superseded", "deprecated", "retracted":
			reasons["source_lifecycle_invalid:"+source.SourceRecordID] = true
		}
		if record.ValidFrom != "" {
			start, err := parseConsolidationInstant(record.ValidFrom)
			if err != nil {
				return MaterializedViewRevision{}, err
			}
			if instant.Before(start) {
				reasons["source_outside_validity:"+source.SourceRecordID] = true
			}
		}
		if record.ValidTo != "" {
			end, err := parseConsolidationInstant(record.ValidTo)
			if err != nil {
				return MaterializedViewRevision{}, err
			}
			if !instant.Before(end) {
				reasons["source_outside_validity:"+source.SourceRecordID] = true
			}
		}
	}
	updated := view
	updated.Sources = slices.Clone(view.Sources)
	updated.EvidenceRefs = slices.Clone(view.EvidenceRefs)
	updated.InvalidationReasons = make([]string, 0, len(reasons))
	for reason := range reasons {
		updated.InvalidationReasons = append(updated.InvalidationReasons, reason)
	}
	slices.Sort(updated.InvalidationReasons)
	updated.Status = "active"
	if len(reasons) != 0 {
		updated.Status = "invalidated"
	}
	return updated, nil
}
