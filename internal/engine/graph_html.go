package engine

import (
	"context"
	"fmt"
	"html"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// RenderGraphHTML writes a standalone web view of the store: blocks as labelled
// clusters, every record as a node, and semantic edges as curves — including the
// bridges between blocks, which the tree view cannot show. Self-contained on
// purpose: no build step, no server, opens from disk.
func RenderGraphHTML(title string, records []memory.MemoryRecord, edges []memory.EdgeRecord, withdrawn map[string]bool) string {
	type node struct {
		id     string
		title  string
		block  string
		kind   string
		x, y   float64
		degree int
	}
	nodes := make([]*node, 0, len(records))
	byID := map[string]*node{}
	blockNames := make([]string, 0)
	seenBlock := map[string]bool{}
	for _, record := range records {
		if record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID] {
			continue
		}
		block := ""
		if record.BlockMembership != nil {
			block = record.BlockMembership.PrimaryBlockID
		}
		item := &node{id: record.BlockID, title: record.Title, block: block, kind: record.Kind}
		nodes = append(nodes, item)
		byID[record.BlockID] = item
		if !seenBlock[block] {
			seenBlock[block] = true
			blockNames = append(blockNames, block)
		}
	}
	sort.Strings(blockNames)
	blockIndex := map[string]int{}
	for index, name := range blockNames {
		blockIndex[name] = index
	}

	type link struct {
		a, b  *node
		kind  string
		cross bool
	}
	links := make([]link, 0, len(edges))
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		from, okFrom := byID[edge.SrcID]
		to, okTo := byID[edge.DstID]
		if !okFrom || !okTo || from == to {
			continue
		}
		from.degree++
		to.degree++
		links = append(links, link{a: from, b: to, kind: edge.EdgeType, cross: from.block != to.block})
	}

	// Seed each block cluster on a circle, then relax with a tiny force pass; the
	// result is stable enough to compare two runs by eye.
	random := rand.New(rand.NewSource(7))
	blocks := float64(len(blockNames))
	for index, item := range nodes {
		angle := 2 * math.Pi * float64(blockIndex[item.block]) / math.Max(blocks, 1)
		radius := 260.0
		item.x = 560 + radius*math.Cos(angle) + random.Float64()*40
		item.y = 420 + radius*math.Sin(angle) + random.Float64()*40
		_ = index
	}
	for round := 0; round < 220; round++ {
		for _, current := range links {
			dx := current.b.x - current.a.x
			dy := current.b.y - current.a.y
			distance := math.Hypot(dx, dy) + 1
			target := 130.0
			if current.cross {
				target = 260
			}
			force := (distance - target) * 0.02
			ux, uy := dx/distance, dy/distance
			current.a.x += ux * force
			current.a.y += uy * force
			current.b.x -= ux * force
			current.b.y -= uy * force
		}
		for i := 0; i < len(nodes); i++ {
			for j := i + 1; j < len(nodes); j++ {
				dx := nodes[j].x - nodes[i].x
				dy := nodes[j].y - nodes[i].y
				distance := math.Hypot(dx, dy) + 1
				if distance > 90 {
					continue
				}
				push := (90 - distance) * 0.05
				ux, uy := dx/distance, dy/distance
				nodes[i].x -= ux * push
				nodes[i].y -= uy * push
				nodes[j].x += ux * push
				nodes[j].y += uy * push
			}
		}
	}

	palette := []string{"#7c9cff", "#ffb37c", "#88d498", "#f78fb3", "#c3a6ff", "#6fd3e0", "#e8d174", "#ff8f8f"}
	var svg strings.Builder
	svg.WriteString(fmt.Sprintf(`<svg viewBox="0 0 1120 840" width="100%%" height="100%%" role="img">`))
	for _, current := range links {
		stroke := "#c9d2e6"
		width := "1"
		dash := ""
		if current.cross {
			stroke = "#8c6cff"
			width = "1.6"
			dash = ` stroke-dasharray="5 4"`
		}
		svg.WriteString(fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%s"%s><title>%s</title></line>`,
			current.a.x, current.a.y, current.b.x, current.b.y, stroke, width, dash, html.EscapeString(current.kind)))
	}
	for _, item := range nodes {
		color := palette[blockIndex[item.block]%len(palette)]
		radius := 6.0
		if item.degree > 3 {
			radius = 9
		}
		svg.WriteString(fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s" stroke="#1b2436" stroke-width="0.8"><title>%s · %s</title></circle>`,
			item.x, item.y, radius, color, html.EscapeString(item.title), html.EscapeString(item.block)))
	}
	svg.WriteString(`</svg>`)

	var legend strings.Builder
	for index, name := range blockNames {
		count := 0
		for _, item := range nodes {
			if item.block == name {
				count++
			}
		}
		legend.WriteString(fmt.Sprintf(`<li><span class="dot" style="background:%s"></span>%s <em>%d</em></li>`,
			palette[index%len(palette)], html.EscapeString(name), count))
	}
	cross := 0
	for _, current := range links {
		if current.cross {
			cross++
		}
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><title>%s</title>
<style>
:root{color-scheme:light dark;--bg:#0f1420;--fg:#e6ebf5;--muted:#9aa7bd;--card:#182033}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 system-ui,Segoe UI,sans-serif}
header{padding:18px 24px;border-bottom:1px solid #26314a}
h1{margin:0 0 6px;font-size:18px}
p{margin:0;color:var(--muted)}
.wrap{display:grid;grid-template-columns:1fr 280px;gap:0;height:calc(100vh - 84px)}
main{overflow:hidden}
ul{list-style:none;margin:0;padding:16px 18px;display:grid;gap:8px}
li{display:flex;align-items:center;gap:8px;color:var(--muted)}
.dot{width:10px;height:10px;border-radius:50%%;display:inline-block;flex:0 0 auto}
em{margin-left:auto;color:var(--fg);font-style:normal}
aside{border-left:1px solid #26314a;background:var(--card)}
.small{padding:0 18px 16px;color:var(--muted);font-size:12px}
</style></head><body>
<header>
<h1>%s</h1>
<p>%d записей · %d связей · %d мостов между блоками · сплошная линия — внутри блока, пунктир — между блоками</p>
</header>
<div class="wrap"><main>%s</main>
<aside><ul>%s</ul><p class="small">Наведите на точку, чтобы увидеть заголовок и блок. Цвет — блок; размер — число связей.</p></aside></div>
</body></html>`, html.EscapeString(title), html.EscapeString(title), len(nodes), len(links), cross, svg.String(), legend.String())
}

// GraphHTML writes the standalone web view next to the store and reports what it
// contains, so "does the graph look like a web" has a file to open.
func (engine *Engine) GraphHTML(ctx context.Context, path string) (map[string]any, error) {
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	withdrawn := retractedTargets(records, edges)
	title := "UM-MEM � " + engine.config.WorkspaceID
	document := RenderGraphHTML(title, records, edges, withdrawn)
	if err := os.WriteFile(path, []byte(document), 0o644); err != nil {
		return nil, err
	}
	cross := 0
	byID := map[string]string{}
	for _, record := range records {
		if record.BlockMembership != nil {
			byID[record.BlockID] = record.BlockMembership.PrimaryBlockID
		}
	}
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		if from, ok := byID[edge.SrcID]; ok {
			if to, ok2 := byID[edge.DstID]; ok2 && from != to {
				cross++
			}
		}
	}
	return map[string]any{"ok": true, "path": path, "bytes": len(document), "cross_block_edges": cross}, nil
}
