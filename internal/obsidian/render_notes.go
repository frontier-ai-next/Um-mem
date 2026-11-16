package obsidian

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func yamlScalar(value string) string {
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, ":#[]{}\n\r\"") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

func frontmatter(record memory.MemoryRecord, where string) string {
	lines := []string{"---",
		"id: " + yamlScalar(record.BlockID), "title: " + yamlScalar(record.Title),
		"workspace_id: " + yamlScalar(record.WorkspaceID), "area: " + yamlScalar(where),
		"kind: " + yamlScalar(record.Kind), "temperature: " + yamlScalar(temperature(record)),
		"scope: " + yamlScalar(record.Scope), "status: " + yamlScalar(record.Status),
		"retention: " + yamlScalar(record.Retention),
		"delivery_policy: " + yamlScalar(record.DeliveryPolicy),
		"authority: " + yamlScalar(record.Authority),
		"trust_level: " + yamlScalar(record.TrustLevel),
		"verification_status: " + yamlScalar(record.VerificationStatus),
		"promotion_decision: " + yamlScalar(record.PromotionDecision),
		"source_backend: " + yamlScalar(record.SourceBackend),
		"sensitivity: " + yamlScalar(record.Sensitivity),
		"content_mode: " + yamlScalar(record.ContentMode),
		"operational_risk: " + yamlScalar(record.OperationalRisk), "tags:"}
	if len(record.Tags) == 0 {
		lines = append(lines, "  []")
	} else {
		for _, tag := range record.Tags {
			lines = append(lines, "  - "+yamlScalar(tag))
		}
	}
	if record.RunID != "" {
		lines = append(lines, "run_id: "+yamlScalar(record.RunID))
	}
	if record.SubtaskID != "" {
		lines = append(lines, "subtask_id: "+yamlScalar(record.SubtaskID))
	}
	if len(record.EvidenceRefs) > 0 {
		lines = append(lines, "evidence_refs:")
		for _, ref := range record.EvidenceRefs {
			lines = append(lines, "  - type: "+yamlScalar(ref.RefType),
				"    id: "+yamlScalar(ref.RefID))
			if ref.ProducedBy != "" {
				lines = append(lines, "    produced_by: "+yamlScalar(ref.ProducedBy))
			}
			if ref.ProducerID != "" {
				lines = append(lines, "    producer_id: "+yamlScalar(ref.ProducerID))
			}
		}
	}
	return strings.Join(append(lines, "---"), "\n")
}

func wikiLink(target, alias string) string {
	target = strings.TrimSuffix(strings.ReplaceAll(target, "\\", "/"), ".md")
	if alias != "" && alias != target {
		return "[[" + target + "|" + alias + "]]"
	}
	return "[[" + target + "]]"
}

func noteText(lines []string) string {
	return strings.TrimRight(strings.Join(lines, "\n"), "\n\r\t ") + "\n"
}

func hiddenMetadataKey(key string) bool {
	text := strings.ToLower(key)
	if text == "native_score" || text == "raw_backend_score" || text == "retrieval_score" ||
		text == "store" || strings.HasSuffix(text, "_store") {
		return true
	}
	return strings.HasSuffix(text, "_write_policy")
}

func metadataValue(value any) string {
	switch value := value.(type) {
	case nil:
		return "None"
	case bool:
		if value {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(value)
	}
}

func edgeLabel(edgeType string) string {
	if edgeType == "" {
		return "Related"
	}
	words := strings.Fields(strings.ReplaceAll(edgeType, "_", " "))
	for i, word := range words {
		letters := []rune(strings.ToLower(word))
		if len(letters) > 0 {
			letters[0] = []rune(strings.ToUpper(string(letters[0])))[0]
		}
		words[i] = string(letters)
	}
	return strings.Join(words, " ")
}

func dedupeEdges(edges []memory.EdgeRecord) []memory.EdgeRecord {
	seen := map[[4]string]bool{}
	result := make([]memory.EdgeRecord, 0, len(edges))
	for _, edge := range edges {
		key := [4]string{edge.SrcID, edge.DstID, edge.EdgeType, edge.Reason}
		if !seen[key] {
			seen[key] = true
			result = append(result, edge)
		}
	}
	return result
}

func blockEdges(id string, edges []memory.EdgeRecord, noteNames map[string]string) (outgoing, incoming []memory.EdgeRecord) {
	outgoing, incoming = []memory.EdgeRecord{}, []memory.EdgeRecord{}
	for _, edge := range dedupeEdges(edges) {
		if edge.SrcID == id && noteNames[edge.DstID] != "" {
			outgoing = append(outgoing, edge)
		}
		if edge.DstID == id && noteNames[edge.SrcID] != "" {
			incoming = append(incoming, edge)
		}
	}
	return outgoing, incoming
}

func renderBlockNote(record memory.MemoryRecord, edges []memory.EdgeRecord,
	noteNames map[string]string) string {
	where := area(record)
	title := record.Title
	if title == "" {
		title = record.BlockID
	}
	backend := record.SourceBackend
	if backend == "" {
		backend = "unknown"
	}
	workspace := record.WorkspaceID
	if workspace == "" {
		workspace = "global/manager"
	}
	methods := strings.Join(record.VerificationMethods, ", ")
	if methods == "" {
		methods = "none"
	}
	lines := []string{frontmatter(record, where), "", "# " + title, "",
		fmt.Sprintf("> Area: **%s** · Temperature: `%s` · Status: `%s` · Trust: `%s`",
			where, temperature(record), record.Status, record.TrustLevel), "",
		"## Memory", "", strings.TrimSpace(record.Content), "", "## Runtime semantics", "",
		"- Workspace: `" + workspace + "`", "- Kind: `" + record.Kind + "`",
		"- Scope: `" + record.Scope + "`", "- Delivery policy: `" + record.DeliveryPolicy + "`",
		"- Authority: `" + record.Authority + "`", "- Backend block: `" + backend + "`",
		"- Verification: `" + record.VerificationStatus + "` via `" + methods + "`"}
	if len(record.EvidenceRefs) > 0 {
		lines = append(lines, "", "## Evidence", "")
		for _, ref := range record.EvidenceRefs {
			refType := ref.RefType
			if refType == "" {
				refType = "evidence"
			}
			value := refType
			if ref.RefID != "" {
				value += ":" + ref.RefID
			}
			lines = append(lines, "- "+value)
		}
	}
	outgoing, incoming := blockEdges(record.BlockID, edges, noteNames)
	if len(outgoing)+len(incoming) > 0 {
		lines = append(lines, "", "## Related memory", "")
		for _, edge := range outgoing {
			line := "- " + edgeLabel(edge.EdgeType) + ": [[" + noteNames[edge.DstID] + "]]"
			if edge.Reason != "" {
				line += " — " + edge.Reason
			}
			lines = append(lines, line)
		}
		for _, edge := range incoming {
			line := "- " + edgeLabel(edge.EdgeType) + " by: [[" + noteNames[edge.SrcID] + "]]"
			if edge.Reason != "" {
				line += " — " + edge.Reason
			}
			lines = append(lines, line)
		}
	}
	keys := []string{}
	for key, value := range record.Metadata {
		if value != nil && value != "" && !hiddenMetadataKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		lines = append(lines, "", "## Details", "")
		for _, key := range keys {
			lines = append(lines, "- `"+key+"`: "+metadataValue(record.Metadata[key]))
		}
	}
	return noteText(lines)
}

func renderEvidenceNote(record memory.MemoryRecord, sessionHub string) string {
	category := evidenceCategory(record)
	title := record.Title
	if title == "" {
		title = record.BlockID
	}
	backend := record.SourceBackend
	if backend == "" {
		backend = "unknown"
	}
	content := strings.TrimSpace(record.Content)
	if content == "" {
		content = "_(empty)_"
	}
	lines := []string{frontmatter(record, "Evidence/"+category), "", "# " + title, "",
		fmt.Sprintf("> Evidence category: **%s** · Temperature: `%s` · Status: `%s` · Delivery: `%s` · Backend: `%s`",
			category, temperature(record), record.Status, record.DeliveryPolicy, backend), "",
		"This note is source/candidate/session evidence. It can help recall and audit, but it is not a directive or stable project fact until promoted into core memory.",
		"", "## Content", "", content, ""}
	if sessionHub != "" {
		lines = append(lines, "## Navigation", "", "- Session hub: "+wikiLink(sessionHub, ""), "")
	}
	keys := []string{}
	for key := range record.Metadata {
		if key != "raw" && key != "payload" && !hiddenMetadataKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		lines = append(lines, "## Metadata", "")
		for _, key := range keys {
			lines = append(lines, "- **"+key+"**: `"+metadataValue(record.Metadata[key])+"`")
		}
		lines = append(lines, "")
	}
	return noteText(lines)
}
