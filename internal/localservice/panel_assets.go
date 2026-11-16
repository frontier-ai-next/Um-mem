package localservice

import (
	"embed"
	"errors"
)

//go:embed panel_assets/index.html panel_assets/style.css panel_assets/refinement.css panel_assets/app.js panel_assets/graph-layout.mjs panel_assets/branch-records.mjs
var panelAssets embed.FS

func panelAsset(name string) ([]byte, string, error) {
	contentType := map[string]string{"index.html": "text/html", "style.css": "text/css",
		"refinement.css": "text/css", "app.js": "text/javascript",
		"graph-layout.mjs": "text/javascript", "branch-records.mjs": "text/javascript"}[name]
	if contentType == "" {
		return nil, "", errors.New("unknown panel asset")
	}
	content, err := panelAssets.ReadFile("panel_assets/" + name)
	return content, contentType, err
}
