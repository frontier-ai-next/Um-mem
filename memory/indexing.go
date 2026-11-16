package memory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

type ExactTerm struct {
	Type  string
	Value string
}

type ProvenanceLink struct {
	DerivedID    string
	RawSourceRef string
}

type IndexDocument struct {
	ExactTerms      []ExactTerm
	StructureNodes  []IndexNode
	ProvenanceLinks []ProvenanceLink
}

var exactMetadataFields = map[string][]string{
	"package":                 {"package", "packages", "package_id", "package_ids"},
	"component":               {"component", "components", "component_id", "component_ids"},
	"path":                    {"path", "paths", "source_path", "source_paths", "file_path", "touched_files"},
	"symbol":                  {"symbol", "symbols", "target_symbols", "touched_symbols"},
	"entity":                  {"entity", "entities"},
	"dependency":              {"dependency", "dependencies"},
	"configuration":           {"configuration", "configurations"},
	"config_key":              {"config_key", "config_keys"},
	"command":                 {"command", "commands"},
	"version":                 {"version", "versions"},
	"temporal_state":          {"temporal_state", "temporal_states"},
	"endpoint":                {"endpoint", "endpoints"},
	"artifact_hash":           {"artifact_hash", "artifact_hashes"},
	"checkpoint_source_range": {"checkpoint_source_range_ref", "checkpoint_source_range_refs"},
	"fold_source_range":       {"fold_source_range_ref", "fold_source_range_refs"},
	"prospective_trigger":     {"prospective_obligation_id", "prospective_obligation_ids"},
}

func exactValue(value any) string {
	switch item := value.(type) {
	case string:
		return norm.NFC.String(strings.TrimSpace(item))
	case json.Number:
		return item.String()
	case bool:
		if item {
			return "True"
		}
		return "False"
	case float64, int, int64:
		return fmt.Sprint(item)
	default:
		return ""
	}
}

func metadataValues(metadata map[string]any, keys ...string) []string {
	values := make([]string, 0)
	seen := make(map[string]bool)
	for _, key := range keys {
		raw, exists := metadata[key]
		if !exists {
			continue
		}
		items := []any{raw}
		switch typed := raw.(type) {
		case string:
			items = []any{typed}
		case []any:
			items = typed
		case []string:
			items = make([]any, len(typed))
			for index, value := range typed {
				items[index] = value
			}
		default:
			continue
		}
		for _, item := range items {
			value := exactValue(item)
			if value != "" && !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
	}
	return values
}

func CompileIndexDocument(record MemoryRecord) (IndexDocument, error) {
	if err := record.ValidateSafety(); err != nil {
		return IndexDocument{}, err
	}
	terms := make(map[ExactTerm]bool)
	links := make(map[ProvenanceLink]bool)
	add := func(kind string, values ...string) {
		for _, raw := range values {
			value := norm.NFC.String(strings.TrimSpace(raw))
			if value != "" {
				terms[ExactTerm{Type: kind, Value: value}] = true
			}
		}
	}
	valuesByType := make(map[string][]string, len(exactMetadataFields))
	for kind, keys := range exactMetadataFields {
		valuesByType[kind] = metadataValues(record.Metadata, keys...)
		add(kind, valuesByType[kind]...)
	}
	add("artifact_hash", record.ArtifactRevision)
	add("repository", record.RepositoryID)
	add("profile_id", record.ProfileID)
	scopeKey, err := record.ScopeKey()
	if err != nil {
		return IndexDocument{}, err
	}
	add("scope_key", scopeKey)
	if record.BlockMembership != nil {
		add("primary_block_id", record.BlockMembership.PrimaryBlockID)
	}
	for _, reference := range record.EvidenceRefs {
		add("evidence_ref", reference.RefID)
		if reference.Hash != nil {
			add("evidence_ref", *reference.Hash)
		}
	}
	if segments, ok := record.Metadata["source_segments"].([]any); ok {
		for _, item := range segments {
			segment, ok := item.(map[string]any)
			if !ok {
				continue
			}
			add("evidence_ref", exactValue(segment["source_id"]), exactValue(segment["sha256"]))
		}
	}
	for _, rawClaim := range record.ChildClaims {
		var claim struct {
			OriginSegments []struct {
				Origin struct {
					Channel  string `json:"channel"`
					OriginID string `json:"origin_id"`
				} `json:"origin"`
				IndependenceDomain string        `json:"independence_domain"`
				EvidenceRefs       []EvidenceRef `json:"evidence_refs"`
			} `json:"origin_segments"`
			DerivationReceipt *struct {
				DerivedID string `json:"derived_id"`
			} `json:"derivation_receipt"`
		}
		if err := json.Unmarshal(rawClaim, &claim); err != nil {
			return IndexDocument{}, fmt.Errorf("invalid child claim provenance: %w", err)
		}
		for _, segment := range claim.OriginSegments {
			add("origin_channel", segment.Origin.Channel)
			add("independence_domain", segment.IndependenceDomain)
			rawRefs := []string{segment.Origin.OriginID}
			for _, reference := range segment.EvidenceRefs {
				rawRefs = append(rawRefs, reference.RefID)
			}
			add("raw_source_ref", rawRefs...)
			if claim.DerivationReceipt != nil {
				for _, rawRef := range rawRefs {
					if rawRef != "" && claim.DerivationReceipt.DerivedID != "" {
						links[ProvenanceLink{DerivedID: claim.DerivationReceipt.DerivedID, RawSourceRef: rawRef}] = true
					}
				}
			}
		}
	}
	document := IndexDocument{
		ExactTerms:      make([]ExactTerm, 0, len(terms)),
		ProvenanceLinks: make([]ProvenanceLink, 0, len(links)),
	}
	document.StructureNodes = compileIndexNodes(record, valuesByType)
	for term := range terms {
		document.ExactTerms = append(document.ExactTerms, term)
	}
	for link := range links {
		document.ProvenanceLinks = append(document.ProvenanceLinks, link)
	}
	sort.Slice(document.ExactTerms, func(i, j int) bool {
		a, b := document.ExactTerms[i], document.ExactTerms[j]
		return a.Type < b.Type || a.Type == b.Type && a.Value < b.Value
	})
	sort.Slice(document.ProvenanceLinks, func(i, j int) bool {
		a, b := document.ProvenanceLinks[i], document.ProvenanceLinks[j]
		return a.DerivedID < b.DerivedID || a.DerivedID == b.DerivedID && a.RawSourceRef < b.RawSourceRef
	})
	return document, nil
}
