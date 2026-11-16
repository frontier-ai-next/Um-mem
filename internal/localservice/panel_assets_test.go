package localservice

import (
	"bytes"
	"testing"
)

func TestPanelAssetsAreEmbeddedAndRestricted(t *testing.T) {
	for _, name := range []string{"index.html", "style.css", "app.js"} {
		content, contentType, err := panelAsset(name)
		if err != nil || len(content) == 0 || contentType == "" {
			t.Fatalf("panel asset %s: %s, %v", name, contentType, err)
		}
	}
	if content, _, err := panelAsset("../schema.sql"); err == nil || len(content) != 0 {
		t.Fatal("panel asset lookup accepted a filesystem path")
	}
	html, _, err := panelAsset("index.html")
	if err != nil || !bytes.Contains(html, []byte("/panel/assets/app.js")) {
		t.Fatal("embedded panel shell lost its browser client")
	}
}
