package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// IndexNode is the canonical project hierarchy or an exact retrieval facet.
type IndexNode struct {
	NodeID      string         `json:"node_id"`
	Title       string         `json:"title"`
	NodeType    string         `json:"node_type"`
	WorkspaceID string         `json:"workspace_id"`
	Tags        []string       `json:"tags"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

func indexNodeID(workspaceID, kind, value, parentID string) string {
	material := workspaceID + "\x00" + kind + "\x00" + value + "\x00" + parentID
	digest := sha256.Sum256([]byte(material))
	return "project_structure:" + hex.EncodeToString(digest[:])
}

func compileIndexNodes(record MemoryRecord, valuesByType map[string][]string) []IndexNode {
	type level struct{ kind, value string }
	levels := make([]level, 0, 5)
	appendLevel := func(kind, value string) {
		value = norm.NFC.String(strings.TrimSpace(value))
		if value != "" {
			levels = append(levels, level{kind, value})
		}
	}
	first := func(values []string) string {
		if len(values) > 0 {
			return values[0]
		}
		return ""
	}
	appendLevel("repository", record.RepositoryID)
	appendLevel("package", first(metadataValues(record.Metadata, "package_id", "package")))
	appendLevel("component", first(metadataValues(record.Metadata, "component_id", "component")))
	appendLevel("file", first(valuesByType["path"]))
	appendLevel("symbol", first(valuesByType["symbol"]))
	createdAt, updatedAt := record.CreatedAt, record.UpdatedAt
	if createdAt == "" {
		createdAt = record.RecordedAt
	}
	if updatedAt == "" {
		updatedAt = record.RecordedAt
	}
	nodes := make([]IndexNode, 0)
	parentID := ""
	for _, item := range levels {
		id := indexNodeID(record.WorkspaceID, item.kind, item.value, parentID)
		nodes = append(nodes, IndexNode{
			NodeID: id, Title: item.kind + ": " + item.value,
			NodeType: "project_structure", WorkspaceID: record.WorkspaceID,
			Tags: []string{"project_structure", item.kind},
			Metadata: map[string]any{"structure_kind": item.kind, "structure_value": item.value,
				"parent_id": parentID, "related_record_ids": []string{record.BlockID}},
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		})
		parentID = id
	}
	for _, kind := range []string{"entity", "dependency", "configuration", "config_key", "command", "version", "temporal_state", "endpoint"} {
		values := append([]string(nil), valuesByType[kind]...)
		sort.Strings(values)
		for _, raw := range values {
			value := norm.NFC.String(strings.TrimSpace(raw))
			if value == "" {
				continue
			}
			nodes = append(nodes, IndexNode{
				NodeID: indexNodeID(record.WorkspaceID, "facet:"+kind, value, ""),
				Title:  kind + ": " + value, NodeType: "exact_facet", WorkspaceID: record.WorkspaceID,
				Tags: []string{"exact_facet", kind},
				Metadata: map[string]any{"term_type": kind, "term_value": value,
					"related_record_ids": []string{record.BlockID}},
				CreatedAt: createdAt, UpdatedAt: updatedAt,
			})
		}
	}
	return nodes
}
