package obsidian

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

var sessionPattern = regexp.MustCompile(`\d{8}_\d{6}_[A-Za-z0-9]+`)

func sessionKey(record memory.MemoryRecord) string {
	id := record.BlockID
	for _, prefix := range []string{"session_summary_", "session_"} {
		if strings.HasPrefix(id, prefix) {
			return strings.TrimPrefix(id, prefix)
		}
	}
	if strings.HasPrefix(id, "turn_") {
		parts := strings.Split(id, "_")
		if len(parts) >= 4 {
			return strings.Join(parts[1:4], "_")
		}
	}
	values := []string{record.RunID}
	for _, key := range []string{"session_id", "source_session_id"} {
		if value, ok := record.Metadata[key].(string); ok {
			values = append(values, value)
		}
	}
	for _, value := range values {
		if match := sessionPattern.FindString(value); match != "" {
			return match
		}
	}
	return ""
}

func sessionHubName(key string) string {
	return "Session " + safeSegment(strings.ReplaceAll(key, "_", " "), "unknown")
}
func coreHubName(category string) string { return safeSegment(category, "memory") + " Index" }
func evidenceHubName(category string) string {
	return "Evidence " + safeSegment(strings.ReplaceAll(category, "/", " "), "memory") + " Index"
}

type Result struct {
	VaultDir           string   `json:"vault_dir"`
	IndexPath          string   `json:"index_path"`
	MemoryOverviewPath string   `json:"memory_overview_path"`
	BlocksExported     int      `json:"blocks_exported"`
	FilesWritten       []string `json:"files_written"`
}

type canvasTextNode struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Text   string `json:"text"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Color  string `json:"color"`
}
type canvasLink struct {
	ID       string `json:"id"`
	FromNode string `json:"fromNode"`
	FromSide string `json:"fromSide"`
	ToNode   string `json:"toNode"`
	ToSide   string `json:"toSide"`
	Color    string `json:"color"`
}

func textNode(id, text string, x, y, width, height int, color string) canvasTextNode {
	return canvasTextNode{ID: id, Type: "text", Text: text, X: x, Y: y,
		Width: width, Height: height, Color: color}
}
func canvasEdge(id, from, to, color string) canvasLink {
	return canvasLink{ID: id, FromNode: from, FromSide: "bottom",
		ToNode: to, ToSide: "top", Color: color}
}

func renderCanvas(title string, coreCount int, coreGroups, evidenceGroups map[string][]memory.MemoryRecord) (string, error) {
	nodes := []canvasTextNode{textNode("project", title+"\nUmMem workspace", 0, 0, 260, 120, "1"),
		textNode("core", fmt.Sprintf("Core memory\n%d blocks", coreCount), -340, 190, 260, 120, "4"),
		textNode("evidence", fmt.Sprintf("Evidence / recall audit\n%d blocks", countGroups(evidenceGroups)), 340, 190, 300, 120, "2")}
	edges := []canvasLink{canvasEdge("project-core", "project", "core", "4"),
		canvasEdge("project-evidence", "project", "evidence", "2")}
	for i, category := range sortedCoreAreas(coreGroups) {
		id := fmt.Sprintf("core-%d", i)
		nodes = append(nodes, textNode(id, fmt.Sprintf("%s\n%d", category, len(coreGroups[category])), -620+i*260, 390, 230, 100, "4"))
		edges = append(edges, canvasEdge(id+"-edge", "core", id, "4"))
	}
	for i, category := range sortedEvidenceCategories(evidenceGroups) {
		id := fmt.Sprintf("evidence-%d", i)
		color := fmt.Sprint(i%5 + 1)
		nodes = append(nodes, textNode(id, fmt.Sprintf("%s\n%d", category, len(evidenceGroups[category])), 120+i*260, 390, 230, 100, color))
		edges = append(edges, canvasEdge(id+"-edge", "evidence", id, color))
	}
	data, err := json.MarshalIndent(struct {
		Nodes []canvasTextNode `json:"nodes"`
		Edges []canvasLink     `json:"edges"`
	}{Nodes: nodes, Edges: edges}, "", "  ")
	return string(data) + "\n", err
}

func countGroups(groups map[string][]memory.MemoryRecord) int {
	n := 0
	for _, records := range groups {
		n += len(records)
	}
	return n
}

// Render creates the complete generated project tree in memory before any file is changed.
// All returned paths are relative to one workspace's project directory.
func Render(records []memory.MemoryRecord, edges []memory.EdgeRecord, workspaceID, projectTitle string,
	scopeChain []string) (map[string]string, int, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, 0, fmt.Errorf("workspace ID is required")
	}
	if len(scopeChain) == 0 {
		scopeChain = []string{"workspace:" + workspaceID}
	}
	if projectTitle == "" {
		projectTitle = workspaceID
	}
	core, evidence := selectRecords(records, scopeChain)
	names := map[string]string{}
	for _, record := range append(append([]memory.MemoryRecord{}, core...), evidence...) {
		names[record.BlockID] = noteName(record)
	}
	coreGroups := groupBy(core, area)
	evidenceGroups := groupBy(evidence, evidenceCategory)
	coreTargets, evidenceTargets := map[string]string{}, map[string]string{}
	coreHubs, evidenceHubs, sessionHubs := map[string]string{}, map[string]string{}, map[string]string{}
	sessions := map[string][]memory.MemoryRecord{}
	for _, category := range sortedCoreAreas(coreGroups) {
		coreHubs[category] = filepath.ToSlash(filepath.Join("Core", safeSegment(category, "memory"), coreHubName(category)))
	}
	for _, category := range sortedEvidenceCategories(evidenceGroups) {
		evidenceHubs[category] = filepath.ToSlash(filepath.Join("Evidence", safeSegment(category, "memory"), evidenceHubName(category)))
	}
	for _, record := range core {
		coreTargets[record.BlockID] = filepath.ToSlash(filepath.Join("Core", safeSegment(area(record), "memory"), names[record.BlockID]))
	}
	for _, record := range evidence {
		evidenceTargets[record.BlockID] = filepath.ToSlash(filepath.Join("Evidence", safeSegment(evidenceCategory(record), "memory"), names[record.BlockID]))
		if key := sessionKey(record); key != "" {
			sessions[key] = append(sessions[key], record)
			sessionHubs[key] = filepath.ToSlash(filepath.Join("Evidence", "Sessions", sessionHubName(key)))
		}
	}
	files := map[string]string{}
	addNote := func(target, value string) error {
		path := target + ".md"
		if _, exists := files[path]; exists {
			return fmt.Errorf("Obsidian note path collision: %s", path)
		}
		files[path] = value
		return nil
	}
	for _, record := range core {
		if err := addNote(coreTargets[record.BlockID], renderBlockNote(record, edges, names)); err != nil {
			return nil, 0, err
		}
	}
	for _, category := range sortedCoreAreas(coreGroups) {
		if err := addNote(coreHubs[category], renderCoreHub(category, coreGroups[category], names, coreTargets)); err != nil {
			return nil, 0, err
		}
	}
	for _, record := range evidence {
		if err := addNote(evidenceTargets[record.BlockID], renderEvidenceNote(record, sessionHubs[sessionKey(record)])); err != nil {
			return nil, 0, err
		}
	}
	for _, category := range sortedEvidenceCategories(evidenceGroups) {
		if err := addNote(evidenceHubs[category], renderEvidenceHub(category, evidenceGroups[category], names, evidenceTargets, sessionHubs)); err != nil {
			return nil, 0, err
		}
	}
	keys := make([]string, 0, len(sessions))
	for key := range sessions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := addNote(sessionHubs[key], renderSessionHub(key, sessions[key], names, evidenceTargets)); err != nil {
			return nil, 0, err
		}
	}
	files["Core/Core Index.md"] = renderCoreIndex(projectTitle, core, coreHubs)
	files["Evidence/Evidence Index.md"] = renderEvidenceIndex(projectTitle, evidence, evidenceHubs)
	files["Index.md"] = renderIndex(projectTitle, core, evidence)
	files["Memory Overview.md"] = renderOverview(projectTitle, core, evidence, names)
	files["Evidence Archive.md"] = renderEvidenceArchive(projectTitle, len(evidence))
	canvas, err := renderCanvas(projectTitle, len(core), coreGroups, evidenceGroups)
	if err != nil {
		return nil, 0, err
	}
	files["Memory Map.canvas"] = canvas
	return files, len(core), nil
}

func safeChild(root, child string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(root, child))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside Obsidian workspace: %s", child)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink in Obsidian workspace path: %s", current)
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
	}
	return path, nil
}

// Write refreshes one workspace's generated Core and Evidence trees while
// retaining other workspaces and hand-written files at the project root.
func Write(vaultDir, workspaceID, projectTitle string, scopeChain []string,
	records []memory.MemoryRecord, edges []memory.EdgeRecord) (Result, error) {
	files, count, err := Render(records, edges, workspaceID, projectTitle, scopeChain)
	if err != nil {
		return Result{}, err
	}
	vault, err := filepath.Abs(vaultDir)
	if err != nil {
		return Result{}, err
	}
	if vaultDir == "" {
		return Result{}, fmt.Errorf("vault directory is required")
	}
	project, err := safeChild(vault, filepath.Join("UM-MEM", "Projects", safeID(workspaceID)))
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(project, 0755); err != nil {
		return Result{}, err
	}
	stage, err := os.MkdirTemp(project, ".obsidian-stage-")
	if err != nil {
		return Result{}, err
	}
	verifiedStage, err := safeChild(project, filepath.Base(stage))
	if err != nil || verifiedStage != stage {
		return Result{}, fmt.Errorf("staging directory is outside the Obsidian workspace")
	}
	cleanupStage := true
	defer func() {
		if cleanupStage {
			_ = os.RemoveAll(stage)
		}
	}()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		path, err := safeChild(stage, filepath.FromSlash(rel))
		if err != nil {
			return Result{}, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(path, []byte(files[rel]), 0644); err != nil {
			return Result{}, err
		}
	}
	projectsRoot, err := safeChild(vault, filepath.Join("UM-MEM", "Projects"))
	if err != nil {
		return Result{}, err
	}
	entries, err := os.ReadDir(projectsRoot)
	if err != nil {
		return Result{}, err
	}
	projects := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		index, err := safeChild(projectsRoot, filepath.Join(entry.Name(), "Index.md"))
		if err != nil {
			return Result{}, err
		}
		if entry.Name() == safeID(workspaceID) {
			projects = append(projects, entry.Name())
		} else if _, err := os.Stat(index); err == nil {
			projects = append(projects, entry.Name())
		}
	}
	projectsIndex, err := safeChild(projectsRoot, "Index.md")
	if err != nil {
		return Result{}, err
	}
	projectsStage := filepath.Join(stage, ".projects-index.md")
	if err := os.WriteFile(projectsStage, []byte(renderProjectsIndex(projects)), 0644); err != nil {
		return Result{}, err
	}
	type swap struct {
		source, target, backup string
		hadPrevious            bool
	}
	swaps := []swap{}
	for i, rel := range []string{"Core", "Evidence", "Index.md", "Memory Overview.md",
		"Evidence Archive.md", "Memory Map.canvas"} {
		target, err := safeChild(project, rel)
		if err != nil {
			return Result{}, err
		}
		source, err := safeChild(stage, rel)
		if err != nil {
			return Result{}, err
		}
		backup, err := safeChild(stage, fmt.Sprintf(".backup-%d", i))
		if err != nil {
			return Result{}, err
		}
		swaps = append(swaps, swap{source: source, target: target, backup: backup})
	}
	projectsBackup, err := safeChild(stage, ".backup-projects-index")
	if err != nil {
		return Result{}, err
	}
	swaps = append(swaps, swap{source: projectsStage, target: projectsIndex,
		backup: projectsBackup})
	completed := 0
	committed := false
	defer func() {
		if committed {
			return
		}
		for i := completed - 1; i >= 0; i-- {
			item := swaps[i]
			// Every target was checked as a child of this vault before mutation.
			if err := os.RemoveAll(item.target); err != nil {
				cleanupStage = false
			}
			if item.hadPrevious {
				if err := os.Rename(item.backup, item.target); err != nil {
					cleanupStage = false
				}
			}
		}
	}()
	for i := range swaps {
		item := &swaps[i]
		if _, err := os.Lstat(item.target); err == nil {
			item.hadPrevious = true
			if err := os.Rename(item.target, item.backup); err != nil {
				return Result{}, err
			}
		} else if !os.IsNotExist(err) {
			return Result{}, err
		}
		completed = i + 1
		if err := os.Rename(item.source, item.target); err != nil {
			return Result{}, err
		}
	}
	committed = true
	written := make([]string, 0, len(paths)+1)
	for _, rel := range paths {
		written = append(written, filepath.Join(project, filepath.FromSlash(rel)))
	}
	written = append(written, projectsIndex)
	return Result{VaultDir: vault, IndexPath: filepath.Join(project, "Index.md"),
		MemoryOverviewPath: filepath.Join(project, "Memory Overview.md"),
		BlocksExported:     count, FilesWritten: written}, nil
}
