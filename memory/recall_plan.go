package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/text/cases"
)

// RecallPlan describes an explicit, bounded search over memory leaves. Record IDs
// and logical block IDs are separate namespaces.
type RecallPlan struct {
	Query             string              `json:"query"`
	NormalizedQuery   string              `json:"normalized_query"`
	QueryType         string              `json:"query_type"`
	RetrievalMode     string              `json:"retrieval_mode"`
	WorkspaceID       string              `json:"workspace_id"`
	ProfileID         string              `json:"profile_id"`
	RunID             string              `json:"run_id"`
	LogicalBlockIDs   []string            `json:"logical_block_ids"`
	SearchAll         bool                `json:"search_all"`
	RecordIDs         []string            `json:"record_ids"`
	Tags              []string            `json:"tags"`
	Paths             []string            `json:"paths"`
	Symbols           []string            `json:"symbols"`
	Entities          []string            `json:"entities"`
	ConfigKeys        []string            `json:"config_keys"`
	Endpoints         []string            `json:"endpoints"`
	ArtifactHashes    []string            `json:"artifact_hashes"`
	Facets            map[string][]string `json:"facets"`
	SourceHorizon     string              `json:"source_horizon"`
	IncludeCandidates bool                `json:"include_candidates"`
	ScopeChain        []string            `json:"scope_chain"`
	TemporalMode      string              `json:"temporal_mode"`
	AsOf              string              `json:"as_of"`
	KnownAsOf         string              `json:"known_as_of"`
	RevisionID        string              `json:"revision_id"`
	MaxTokens         int                 `json:"max_tokens"`
	Limit             int                 `json:"limit"`
	Reason            string              `json:"reason"`
	Metadata          map[string]any      `json:"metadata"`
}

func NewRecallPlan(query string) RecallPlan {
	return RecallPlan{
		Query: query, RetrievalMode: "semantic", SourceHorizon: "durable",
		TemporalMode: "current", MaxTokens: 2048, Limit: 10,
	}
}

func NormalizeRecallQuery(query string) string {
	return cases.Fold().String(strings.Join(strings.Fields(query), " "))
}

func ClassifyRecallQuery(plan RecallPlan) string {
	if hasValue(plan.RecordIDs) {
		return "exact_lookup"
	}
	if plan.TemporalMode == "historical" || plan.KnownAsOf != "" {
		return "temporal"
	}
	if plan.HasExactSelectors() {
		return "exact_lookup"
	}
	return "broad_recall"
}

// HasExactSelectors reports whether the caller supplied index-owned fields.
// QueryType is not sufficient: a historical plan may also have exact fields.
func (p RecallPlan) HasExactSelectors() bool {
	if hasValue(p.RecordIDs) || hasValue(p.Paths) || hasValue(p.Symbols) ||
		hasValue(p.Entities) || hasValue(p.ConfigKeys) || hasValue(p.Endpoints) ||
		hasValue(p.ArtifactHashes) {
		return true
	}
	for _, values := range p.Facets {
		if hasValue(values) {
			return true
		}
	}
	return false
}

func hasValue(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func uniqueTexts(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validISOTime(value string, requireZone bool) bool {
	if value == "" {
		return true
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return true
	}
	if requireZone {
		return false
	}
	for _, layout := range []string{"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// Validate applies the canonical recall-plan constraints and fills derived fields.
func (p *RecallPlan) Validate() error {
	if p == nil {
		return errors.New("RecallPlan is nil")
	}
	normalized := NormalizeRecallQuery(p.Query)
	if p.NormalizedQuery != "" && p.NormalizedQuery != normalized {
		return errors.New("RecallPlan normalized_query must match the canonical query")
	}
	p.NormalizedQuery = normalized
	p.LogicalBlockIDs = uniqueTexts(p.LogicalBlockIDs)
	p.RecordIDs = uniqueTexts(p.RecordIDs)
	p.Tags = uniqueTexts(p.Tags)
	p.Paths = uniqueTexts(p.Paths)
	p.Symbols = uniqueTexts(p.Symbols)
	p.Entities = uniqueTexts(p.Entities)
	p.ConfigKeys = uniqueTexts(p.ConfigKeys)
	p.Endpoints = uniqueTexts(p.Endpoints)
	p.ArtifactHashes = uniqueTexts(p.ArtifactHashes)
	for name, values := range p.Facets {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return errors.New("RecallPlan facet names must be exact nonempty terms")
		}
		p.Facets[name] = uniqueTexts(values)
	}
	p.ScopeChain = uniqueTexts(p.ScopeChain)
	if p.SourceHorizon != "working" && p.SourceHorizon != "durable" && p.SourceHorizon != "archive" {
		return errors.New("RecallPlan source_horizon must be working, durable, or archive")
	}
	if p.SearchAll && len(p.LogicalBlockIDs) > 0 {
		return errors.New("RecallPlan search_all cannot be combined with logical_block_ids")
	}
	// A caller that names neither logical blocks nor exact record ids is asking
	// for a recall over everything it can see. Requiring `search_all` to be set
	// explicitly made that obvious case fail on backends that cannot scan without
	// a target, with an error the caller could not act on.
	if !p.SearchAll && len(p.LogicalBlockIDs) == 0 && len(p.RecordIDs) == 0 {
		p.SearchAll = true
	}
	if p.TemporalMode != "current" && p.TemporalMode != "historical" {
		return errors.New("RecallPlan temporal_mode must be current or historical")
	}
	if p.RetrievalMode != "semantic" && p.RetrievalMode != "hybrid" {
		return errors.New("RecallPlan retrieval_mode must be semantic or hybrid")
	}
	if p.RetrievalMode == "hybrid" && (len(p.RecordIDs) > 0 || p.TemporalMode == "historical" || p.AsOf != "" || p.KnownAsOf != "" || p.RevisionID != "") {
		return errors.New("hybrid retrieval does not apply to exact record or historical reads")
	}
	if !validISOTime(strings.TrimSpace(p.AsOf), false) {
		return errors.New("RecallPlan as_of must be an ISO timestamp or empty")
	}
	if !validISOTime(strings.TrimSpace(p.KnownAsOf), true) {
		return errors.New("RecallPlan known_as_of requires an ISO timestamp with a timezone")
	}
	if p.TemporalMode == "current" && p.RevisionID != "" {
		return errors.New("RecallPlan revision_id requires historical temporal_mode")
	}
	if p.QueryType == "" {
		p.QueryType = ClassifyRecallQuery(*p)
	}
	switch p.QueryType {
	case "exact_lookup", "broad_recall", "synthesis", "temporal", "negative_evidence", "task_action":
	default:
		return errors.New("RecallPlan query_type must be a canonical closed value")
	}
	if p.MaxTokens < 0 || p.Limit < 0 {
		return fmt.Errorf("RecallPlan max_tokens and limit must be non-negative")
	}
	return nil
}
