package obsidian

import (
	"fmt"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func groupBy(records []memory.MemoryRecord, classify func(memory.MemoryRecord) string) map[string][]memory.MemoryRecord {
	groups := map[string][]memory.MemoryRecord{}
	for _, record := range records {
		key := classify(record)
		groups[key] = append(groups[key], record)
	}
	return groups
}

func sortedNotes(records []memory.MemoryRecord, names map[string]string) []memory.MemoryRecord {
	result := append([]memory.MemoryRecord(nil), records...)
	rank := func(value string, options ...string) int {
		for i, option := range options {
			if value == option {
				return i
			}
		}
		return len(options)
	}
	sort.SliceStable(result, func(i, j int) bool {
		a, b := result[i], result[j]
		left := [3]int{rank(a.DeliveryPolicy, "always_on", "working_set", "on_demand"),
			rank(temperature(a), "hot", "warm", "cold"), rank(a.Status, "accepted", "candidate")}
		right := [3]int{rank(b.DeliveryPolicy, "always_on", "working_set", "on_demand"),
			rank(temperature(b), "hot", "warm", "cold"), rank(b.Status, "accepted", "candidate")}
		for n := range left {
			if left[n] != right[n] {
				return left[n] < right[n]
			}
		}
		return strings.ToLower(names[a.BlockID]) < strings.ToLower(names[b.BlockID])
	})
	return result
}

func countSummary(records []memory.MemoryRecord, key func(memory.MemoryRecord) string) string {
	counts := map[string]int{}
	for _, record := range records {
		counts[key(record)]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func renderIndex(title string, core, evidence []memory.MemoryRecord) string {
	backend := func(r memory.MemoryRecord) string {
		if r.SourceBackend == "" {
			return "unknown"
		}
		return r.SourceBackend
	}
	return noteText([]string{"# " + title + " memory", "",
		"This is the workspace entry point. It links only to hub notes and the canvas so the Obsidian graph remains hierarchical.",
		"", "## Navigation", "", "- " + wikiLink("Core/Core Index", "Core memory"),
		"- " + wikiLink("Evidence/Evidence Index", "Evidence / session recall"),
		"- " + wikiLink("Memory Overview", "Readable overview"),
		"- " + wikiLink("Memory Map.canvas", "Colored memory canvas"), "", "## Snapshot", "",
		fmt.Sprintf("- Core memory blocks: **%d**", len(core)),
		fmt.Sprintf("- Evidence/archive blocks kept in backend: **%d**", len(evidence)),
		"- Temperatures: " + countSummary(core, temperature),
		"- Statuses: " + countSummary(core, func(r memory.MemoryRecord) string { return r.Status }),
		"- Backends: " + countSummary(core, backend), "", "## Authority rule", "",
		"Evidence/session notes are recall and audit material only. Stable project facts should appear under Core memory after promotion/upsert.", ""})
}

func compactText(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) > limit {
		runes := []rune(value)
		value = strings.TrimSpace(string(runes[:limit-3])) + "..."
	}
	return value
}

func renderOverview(title string, core, evidence []memory.MemoryRecord, names map[string]string) string {
	lines := []string{"# " + title + " memory overview", "",
		"A compact human-readable snapshot of the current UM-MEM workspace memory.", ""}
	groups := groupBy(core, area)
	for _, category := range sortedCoreAreas(groups) {
		if len(groups[category]) == 0 {
			continue
		}
		lines = append(lines, "## "+category, "")
		items := sortedNotes(groups[category], names)
		if len(items) > 30 {
			items = items[:30]
		}
		for _, item := range items {
			lines = append(lines, "- **"+names[item.BlockID]+"** — "+compactText(item.Content, 240))
		}
		lines = append(lines, "")
	}
	if len(evidence) > 0 {
		lines = append(lines, "## Evidence archive", "",
			fmt.Sprintf("%d source-backed session/candidate/archive records are kept in the backend for recall and audit. They are summarized in [[Evidence Archive]] instead of being listed as core memory.", len(evidence)), "")
	}
	return noteText(lines)
}

func renderCoreIndex(title string, core []memory.MemoryRecord, hubs map[string]string) string {
	groups := groupBy(core, area)
	lines := []string{"# " + title + " core memory index", "",
		"Core memory is the compact, stable project/profile memory surface. It is separated from raw session evidence to keep the graph readable and authority boundaries clear.", ""}
	if len(core) == 0 {
		lines = append(lines, "No promoted/core memory blocks for this workspace yet. The canonical categories are still listed below so the vault has a stable visual shape.", "")
	}
	for _, category := range sortedCoreAreas(groups) {
		lines = append(lines, fmt.Sprintf("- %s (%d)", wikiLink(hubs[category], category), len(groups[category])))
	}
	return noteText(lines)
}

func renderCoreHub(category string, records []memory.MemoryRecord, names, targets map[string]string) string {
	lines := []string{"# " + category, "",
		"Core area hub for **" + category + "**. Leaf notes are linked below; project indexes link here instead of linking to every leaf directly.", ""}
	if len(records) == 0 {
		return noteText(append(lines, "No blocks in this category yet."))
	}
	for _, record := range sortedNotes(records, names) {
		backend := record.SourceBackend
		if backend == "" {
			backend = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- %s — `%s` / `%s` / `%s`", wikiLink(targets[record.BlockID], names[record.BlockID]), temperature(record), record.Status, backend))
	}
	return noteText(lines)
}

func renderEvidenceIndex(title string, evidence []memory.MemoryRecord, hubs map[string]string) string {
	groups := groupBy(evidence, evidenceCategory)
	lines := []string{"# " + title + " evidence index", "",
		"Evidence is source/candidate/session material used for recall and audit. It is non-directive until promoted into core memory.", "",
		fmt.Sprintf("Total evidence blocks: **%d**", len(evidence)), ""}
	if len(evidence) == 0 {
		lines = append(lines, "No evidence/archive records for this workspace yet. The canonical evidence categories are still listed below so the vault has a stable visual shape.", "")
	}
	for _, category := range sortedEvidenceCategories(groups) {
		lines = append(lines, fmt.Sprintf("- %s (%d)", wikiLink(hubs[category], category), len(groups[category])))
	}
	return noteText(lines)
}

func renderEvidenceHub(category string, records []memory.MemoryRecord, names, targets, sessions map[string]string) string {
	bySession := map[string][]memory.MemoryRecord{}
	direct := []memory.MemoryRecord{}
	for _, record := range records {
		if key := sessionKey(record); key != "" {
			bySession[key] = append(bySession[key], record)
		} else {
			direct = append(direct, record)
		}
	}
	lines := []string{"# " + category, "",
		"Category hub. It links to session hubs first so the graph stays hierarchical instead of becoming a leaf dandelion.", ""}
	keys := make([]string, 0, len(bySession))
	for key := range bySession {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("- %s (%d)", wikiLink(sessions[key], sessionHubName(key)), len(bySession[key])))
	}
	if len(records) == 0 {
		lines = append(lines, "No evidence in this category yet.")
	}
	if len(direct) > 0 {
		lines = append(lines, "", "## Direct evidence", "")
		for _, record := range sortedNotes(direct, names) {
			lines = append(lines, "- "+wikiLink(targets[record.BlockID], names[record.BlockID]))
		}
	}
	return noteText(lines)
}

func renderSessionHub(key string, records []memory.MemoryRecord, names, targets map[string]string) string {
	lines := []string{"# " + sessionHubName(key), "",
		"Session hub. Open this first for a compact view of one agent-memory episode; leaf evidence hangs from this node, not from the project index.", ""}
	groups := groupBy(records, evidenceCategory)
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, category := range keys {
		lines = append(lines, "## "+category, "")
		for _, record := range sortedNotes(groups[category], names) {
			lines = append(lines, "- "+wikiLink(targets[record.BlockID], names[record.BlockID])+" — "+compactText(record.Content, 160))
		}
		lines = append(lines, "")
	}
	return noteText(lines)
}

func renderEvidenceArchive(title string, count int) string {
	return noteText([]string{"# " + title + " evidence archive", "",
		"This note intentionally links only to the evidence index, not to every evidence leaf. Use the hub hierarchy to avoid an Obsidian graph dandelion.", "",
		"- " + wikiLink("Evidence/Evidence Index", "Evidence Index"), fmt.Sprintf("- Total evidence blocks: **%d**", count), ""})
}

func renderProjectsIndex(projects []string) string {
	lines := []string{"# UM-MEM workspaces", "", "Open a workspace index first, then drill into Core or Evidence.", ""}
	if len(projects) == 0 {
		lines = append(lines, "No exported workspaces yet.")
	} else {
		sort.Slice(projects, func(i, j int) bool { return strings.ToLower(projects[i]) < strings.ToLower(projects[j]) })
		for _, project := range projects {
			lines = append(lines, "- "+wikiLink("UM-MEM/Projects/"+project+"/Index", project))
		}
	}
	return noteText(lines)
}
