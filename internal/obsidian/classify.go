package obsidian

import (
	"sort"
	"strings"
	"unicode"

	"github.com/frontier-ai-next/Um-mem/memory"
)

var coreAreas = []string{"Project knowledge", "User profile", "Agent profile memory",
	"Skills", "Tools and MCP", "Project documents", "Other memory"}

var evidenceCategories = []string{"Sessions", "Turns", "User intents", "Candidate ideas",
	"Outcomes", "Questions", "Archive"}

var areaOrder = map[string]int{"User profile": 10, "Agent profile memory": 20,
	"Project knowledge": 30, "Session summaries": 40, "Skills": 50,
	"Tools and MCP": 60, "Project documents": 70, "Candidate ideas": 80,
	"Archive": 90, "Other memory": 100}

func safeSegment(value, fallback string) string {
	value = strings.TrimSpace(value)
	var output strings.Builder
	previousSpace := false
	for _, char := range value {
		switch {
		case unicode.IsLetter(char) || unicode.IsNumber(char) || char == '-' || char == '_':
			output.WriteRune(char)
			previousSpace = false
		case unicode.IsSpace(char):
			if !previousSpace {
				output.WriteByte(' ')
				previousSpace = true
			}
		default:
			if !previousSpace {
				output.WriteByte(' ')
				previousSpace = true
			}
		}
	}
	clean := strings.Trim(output.String(), " ._-")
	for strings.Contains(clean, "  ") {
		clean = strings.ReplaceAll(clean, "  ", " ")
	}
	if clean == "" {
		return fallback
	}
	return clean
}

func safeID(value string) string {
	return strings.ReplaceAll(safeSegment(value, "memory"), " ", "-")
}

func noteName(record memory.MemoryRecord) string {
	id := safeID(record.BlockID)
	title := record.Title
	if title == "" {
		title = record.BlockID
	}
	title = safeSegment(title, id)
	return title + " - " + id
}

func tagSet(record memory.MemoryRecord) map[string]bool {
	tags := make(map[string]bool, len(record.Tags))
	for _, tag := range record.Tags {
		if text := strings.ToLower(strings.TrimSpace(tag)); text != "" {
			tags[text] = true
		}
	}
	return tags
}

func temperature(record memory.MemoryRecord) string {
	value := strings.ToLower(strings.TrimSpace(record.Temperature))
	if value == "" {
		return "warm"
	}
	return value
}

func area(record memory.MemoryRecord) string {
	tags := tagSet(record)
	id := strings.ToLower(record.BlockID)
	kind := strings.ToLower(record.Kind)
	delivery := strings.ToLower(record.DeliveryPolicy)
	status := strings.ToLower(record.Status)
	switch {
	case strings.HasPrefix(id, "user_profile_") || tags["target:user"]:
		return "User profile"
	case strings.HasPrefix(id, "profile_memory_") || tags["target:memory"]:
		return "Agent profile memory"
	case strings.Contains(kind, "session") || strings.HasPrefix(id, "session_"):
		return "Session summaries"
	case strings.Contains(kind, "skill") || tags["skill-index"] || tags["skill"]:
		return "Skills"
	case strings.Contains(kind, "mcp") || tags["mcp-inventory"] || strings.HasPrefix(id, "mcp_inventory"):
		return "Tools and MCP"
	case strings.Contains(kind, "doc") || tags["project-doc"] || tags["project-doc-pointer"]:
		return "Project documents"
	case status == "candidate" || delivery == "reflection_only" ||
		kind == "idea" || kind == "hypothesis" || kind == "research_lead":
		return "Candidate ideas"
	case record.Retention == "archive" || delivery == "hidden" || temperature(record) == "cold":
		return "Archive"
	case map[string]bool{"lesson": true, "durable": true, "preference": true,
		"fact": true, "project_fact": true, "pattern": true, "architecture": true,
		"decision": true}[kind]:
		return "Project knowledge"
	default:
		return "Other memory"
	}
}

func evidenceArchive(record memory.MemoryRecord) bool {
	where := area(record)
	if where == "Session summaries" || where == "Candidate ideas" || where == "Archive" {
		return true
	}
	return strings.Contains(strings.ToLower(record.Kind), "session") ||
		strings.ToLower(record.Status) == "candidate" || record.Retention == "archive" ||
		strings.ToLower(record.DeliveryPolicy) == "hidden" ||
		strings.ToLower(record.DeliveryPolicy) == "reflection_only" || temperature(record) == "cold"
}

func evidenceCategory(record memory.MemoryRecord) string {
	kind := strings.ToLower(record.Kind)
	title := strings.ToLower(record.Title)
	id := strings.ToLower(record.BlockID)
	switch {
	case strings.Contains(kind, "session_summary") || strings.HasPrefix(id, "session_summary") ||
		strings.HasPrefix(id, "session_"):
		return "Sessions"
	case kind == "session_turn" || strings.HasPrefix(title, "conversation turn"):
		return "Turns"
	case kind == "user_intent":
		return "User intents"
	case kind == "assistant_outcome":
		return "Outcomes"
	case kind == "open_question":
		return "Questions"
	case record.Retention == "archive" || record.DeliveryPolicy == "hidden":
		return "Archive"
	default:
		return "Candidate ideas"
	}
}

func inScope(record memory.MemoryRecord, scopeChain map[string]bool) bool {
	if map[string]bool{"retracted": true, "quarantined": true,
		"superseded": true, "deprecated": true}[strings.ToLower(record.Status)] ||
		strings.ToLower(record.Kind) == "source_index" || record.IsProtected() {
		return false
	}
	for _, tag := range record.Tags {
		if strings.ToLower(tag) == "source-node" {
			return false
		}
	}
	key, err := record.ScopeKey()
	return err == nil && scopeChain[key]
}

func preference(record memory.MemoryRecord) [4]string {
	status := "2"
	if record.Status == "accepted" {
		status = "0"
	} else if record.Status == "candidate" {
		status = "1"
	}
	verification := "1"
	if record.VerificationStatus == "verified" {
		verification = "0"
	}
	backend := "3"
	switch record.SourceBackend {
	case "mempalace":
		backend = "0"
	case "hindsight":
		backend = "1"
	case "mnemosyne":
		backend = "2"
	}
	updated := record.UpdatedAt
	if updated == "" {
		updated = record.CreatedAt
	}
	return [4]string{status, verification, backend, updated}
}

func selectRecords(records []memory.MemoryRecord, scopeChain []string) (core, evidence []memory.MemoryRecord) {
	allowed := make(map[string]bool, len(scopeChain))
	for _, key := range scopeChain {
		allowed[key] = true
	}
	selected := map[string]memory.MemoryRecord{}
	order := []string{}
	for _, record := range records {
		if !inScope(record, allowed) {
			continue
		}
		id := record.BlockID
		if id == "" {
			id = record.Content
		}
		current, exists := selected[id]
		if !exists {
			order = append(order, id)
		}
		if !exists || comparePreference(preference(record), preference(current)) < 0 {
			selected[id] = record
		}
	}
	core, evidence = []memory.MemoryRecord{}, []memory.MemoryRecord{}
	for _, id := range order {
		record := selected[id]
		if evidenceArchive(record) {
			evidence = append(evidence, record)
		} else {
			core = append(core, record)
		}
	}
	return core, evidence
}

func comparePreference(left, right [4]string) int {
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func sortedCoreAreas(groups map[string][]memory.MemoryRecord) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, area := range coreAreas {
		result = append(result, area)
		seen[area] = true
	}
	for area := range groups {
		if !seen[area] {
			result = append(result, area)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := areaOrder[result[i]], areaOrder[result[j]]
		if left == 0 {
			left = 999
		}
		if right == 0 {
			right = 999
		}
		if left != right {
			return left < right
		}
		return result[i] < result[j]
	})
	return result
}

func sortedEvidenceCategories(groups map[string][]memory.MemoryRecord) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, category := range evidenceCategories {
		result = append(result, category)
		seen[category] = true
	}
	for category := range groups {
		if !seen[category] {
			result = append(result, category)
		}
	}
	sort.Strings(result)
	return result
}
