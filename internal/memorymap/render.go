package memorymap

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The existing browser presentation is retained verbatim as static assets.
//
//go:embed assets/page.html
var pageHTML string

//go:embed assets/style.css
var pageCSS string

//go:embed assets/script.js
var pageJS string

type BlockNode struct {
	BlockID     string      `json:"block_id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Synopsis    string      `json:"synopsis"`
	Tags        []string    `json:"tags"`
	Records     int         `json:"records"`
	Writable    bool        `json:"writable"`
	IndexOnly   bool        `json:"index_only"`
	Meta        bool        `json:"meta"`
	NodeKind    string      `json:"node_kind"`
	PathNames   []string    `json:"path_names"`
	Children    []BlockNode `json:"children"`
}

type RecordNode struct {
	RecordID   string   `json:"record_id"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	Kind       string   `json:"kind"`
	Tags       []string `json:"tags"`
	Status     string   `json:"status"`
	RecordedAt string   `json:"recorded_at"`
	Block      string   `json:"block"`
	Steps      []string `json:"steps"`
	Verify     string   `json:"verify"`
	Freshness  string   `json:"freshness"`
}

type SemanticEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
	Draw   bool    `json:"draw"`
}

type Data struct {
	WorkspaceID    string         `json:"workspace_id"`
	ProfileID      string         `json:"profile_id"`
	Backend        string         `json:"backend"`
	Tree           []BlockNode    `json:"tree"`
	Records        []RecordNode   `json:"records"`
	RecordCount    int            `json:"record_count"`
	BlockCount     int            `json:"block_count"`
	MetaBlockCount int            `json:"meta_block_count"`
	IndexedRecords int            `json:"indexed_records"`
	SemanticModel  string         `json:"semantic_model"`
	Kinds          map[string]int `json:"kinds"`
	Edges          []SemanticEdge `json:"edges"`
	GeneratedAt    string         `json:"generated_at"`
}

func ageLabel(raw string, now time.Time) string {
	if raw == "" {
		return "unknown age"
	}
	moment, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		moment, err = time.ParseInLocation("2006-01-02T15:04:05.999999999", raw, time.UTC)
		if err != nil {
			return "unknown age"
		}
	}
	seconds := int(now.Sub(moment).Seconds())
	if seconds < 0 {
		return "just now"
	}
	if seconds < 3600 {
		return fmt.Sprintf("%d min ago", max(1, seconds/60))
	}
	if seconds < 86400 {
		return fmt.Sprintf("%d h ago", seconds/3600)
	}
	return fmt.Sprintf("%d d ago", seconds/86400)
}

func graphData(data Data, now time.Time) map[string]any {
	nodes := []map[string]any{}
	links := []map[string]any{}
	var walk func([]BlockNode, string, int)
	walk = func(items []BlockNode, parent string, depth int) {
		for _, node := range items {
			if node.BlockID == "" {
				continue
			}
			nodes = append(nodes, map[string]any{"id": node.BlockID, "type": "block",
				"label": node.Name, "description": node.Description, "synopsis": node.Synopsis,
				"tags": node.Tags, "writable": node.Writable, "index_only": node.IndexOnly,
				"meta": node.Meta, "node_kind": node.NodeKind, "records": node.Records,
				"parent": parent, "depth": depth, "path_names": node.PathNames})
			if parent != "" {
				links = append(links, map[string]any{"source": parent, "target": node.BlockID, "kind": "tree"})
			}
			walk(node.Children, node.BlockID, depth+1)
		}
	}
	walk(data.Tree, "", 0)
	for _, record := range data.Records {
		if record.RecordID == "" {
			continue
		}
		label := record.Title
		if label == "" {
			label = "(untitled)"
		}
		haystack := strings.ToLower(strings.Join([]string{record.Title, record.Content,
			strings.Join(record.Tags, " "), record.Kind}, " "))
		nodes = append(nodes, map[string]any{"id": record.RecordID, "type": "record",
			"label": label, "kind": record.Kind, "text": record.Content, "tags": record.Tags,
			"status": record.Status, "age": ageLabel(record.RecordedAt, now),
			"steps": record.Steps, "verify": record.Verify, "freshness": record.Freshness,
			"parent": record.Block, "hay": haystack})
		if record.Block != "" {
			links = append(links, map[string]any{"source": record.Block,
				"target": record.RecordID, "kind": "membership"})
		}
	}
	identifiers := map[string]bool{}
	for _, node := range nodes {
		identifiers[node["id"].(string)] = true
	}
	for _, edge := range data.Edges {
		if !identifiers[edge.Source] || !identifiers[edge.Target] {
			continue
		}
		links = append(links, map[string]any{"source": edge.Source, "target": edge.Target,
			"kind": "semantic", "weight": edge.Weight, "draw": edge.Draw})
	}
	return map[string]any{"nodes": nodes, "links": links}
}

func Render(data Data, now time.Time) (string, error) {
	graph := graphData(data, now)
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(graph); err != nil {
		return "", err
	}
	encoded := strings.ReplaceAll(strings.TrimSuffix(payload.String(), "\n"), "</", `<\/`)
	fullModel := data.SemanticModel
	if fullModel == "" {
		fullModel = "off"
	}
	shortModel := fullModel
	if last := strings.LastIndexByte(shortModel, '/'); last >= 0 {
		shortModel = shortModel[last+1:]
	}
	drawn := 0
	for _, edge := range data.Edges {
		if edge.Draw {
			drawn++
		}
	}
	stats := fmt.Sprintf(`<div class="stat"><b>%d</b>records</div><div class="stat"><b>%d</b>blocks</div><div class="stat"><b>%d</b>links</div><div class="stat" title="%s"><b>%s</b>model</div>`,
		data.RecordCount, max(0, data.BlockCount-data.MetaBlockCount), drawn,
		html.EscapeString(fullModel), html.EscapeString(shortModel))
	subtitleParts := []string{}
	for _, part := range []string{data.WorkspaceID, data.Backend} {
		if part != "" {
			subtitleParts = append(subtitleParts, part)
		}
	}
	if data.RecordCount > 0 {
		subtitleParts = append(subtitleParts, fmt.Sprintf("%d/%d indexed", data.IndexedRecords, data.RecordCount))
	}
	if data.MetaBlockCount > 0 {
		subtitleParts = append(subtitleParts, fmt.Sprintf("%d meta", data.MetaBlockCount))
	}
	return strings.NewReplacer("__TITLE__", "UM-MEM memory graph · "+html.EscapeString(data.WorkspaceID),
		"__SUBTITLE__", html.EscapeString(strings.Join(subtitleParts, " · ")),
		"__STATS__", stats, "__STYLE__", pageCSS,
		"__DATA__", encoded, "__SCRIPT__", pageJS).Replace(pageHTML), nil
}

func Summary(data Data) (string, error) {
	drawn := 0
	for _, edge := range data.Edges {
		if edge.Draw {
			drawn++
		}
	}
	payload := struct {
		WorkspaceID   string         `json:"workspace_id"`
		Records       int            `json:"records"`
		Blocks        int            `json:"blocks"`
		MetaBlocks    int            `json:"meta_blocks"`
		Indexed       int            `json:"indexed"`
		Model         string         `json:"model"`
		Kinds         map[string]int `json:"kinds"`
		SemanticLinks int            `json:"semantic_links"`
	}{data.WorkspaceID, data.RecordCount, data.BlockCount, data.MetaBlockCount,
		data.IndexedRecords, data.SemanticModel, data.Kinds, drawn}
	var compact bytes.Buffer
	encoder := json.NewEncoder(&compact)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		return "", err
	}
	var spaced strings.Builder
	quoted, escaped := false, false
	for _, character := range strings.TrimSuffix(compact.String(), "\n") {
		spaced.WriteRune(character)
		if quoted {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				quoted = false
			}
			continue
		}
		if character == '"' {
			quoted = true
		} else if character == ',' || character == ':' {
			spaced.WriteByte(' ')
		}
	}
	return spaced.String(), nil
}

func Write(outputPath, page string) (string, error) {
	path, err := filepath.Abs(outputPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".memory-map-*.html")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(page); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
