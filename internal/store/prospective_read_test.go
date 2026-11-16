package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProspectiveObligationsReadPythonFixtureAndRejectStaleIndex(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "python_prospective.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		NodeJSON        string `json:"node_json"`
		PayloadJSON     string `json:"payload_json"`
		PayloadSHA256   string `json:"payload_sha256"`
		PayloadID       string `json:"payload_id"`
		ReceiptID       string `json:"receipt_id"`
		EventID         string `json:"event_id"`
		Version         int    `json:"version"`
		ExpectedVersion int    `json:"expected_version"`
		ObligationID    string `json:"obligation_id"`
		Scope           string `json:"scope"`
		Status          string `json:"status"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var node struct {
		NodeID      string         `json:"node_id"`
		WorkspaceID string         `json:"workspace_id"`
		NodeType    string         `json:"node_type"`
		Title       string         `json:"title"`
		Tags        []string       `json:"tags"`
		UpdatedAt   string         `json:"updated_at"`
		Metadata    map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(fixture.NodeJSON), &node); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO canonical_payloads(payload_id,payload_sha256,payload_kind,payload_json,created_at)
	 VALUES(?,?,?,?,?)`, fixture.PayloadID, fixture.PayloadSHA256, "prospective_obligation",
		fixture.PayloadJSON, "2026-10-24T12:00:00+00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO prospective_obligations(obligation_id,scope,status,version,payload_id,
	 payload_sha256,receipt_id,updated_at) VALUES(?,?,?,?,?,?,?,?)`, fixture.ObligationID,
		fixture.Scope, fixture.Status, fixture.Version, fixture.PayloadID, fixture.PayloadSHA256,
		fixture.ReceiptID, "2026-10-24T12:00:00+00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO prospective_obligation_events(obligation_id,event_id,
	 idempotency_key,expected_version,from_status,to_status,payload_id,payload_sha256,receipt_id,
	 created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, fixture.ObligationID, fixture.EventID,
		fixture.EventID, fixture.ExpectedVersion, "pending", fixture.Status, fixture.PayloadID,
		fixture.PayloadSHA256, fixture.ReceiptID, "2026-10-24T12:00:00+00:00"); err != nil {
		t.Fatal(err)
	}
	tags, _ := json.Marshal(node.Tags)
	if _, err := db.ExecContext(ctx, `INSERT INTO index_nodes(node_id,workspace_id,node_type,title,tags_json,
	 payload_json,updated_at) VALUES(?,?,?,?,?,?,?)`, node.NodeID, node.WorkspaceID, node.NodeType,
		node.Title, string(tags), fixture.NodeJSON, node.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	obligations, err := index.ProspectiveObligations(ctx, 50)
	if err != nil || len(obligations) != 1 || obligations[0].ID != "obl-1" ||
		len(obligations[0].Trigger) != 1 || obligations[0].Trigger[0].Value != "codex_event:PreToolUse" {
		t.Fatalf("Python prospective fixture was not read: %+v, %v", obligations, err)
	}
	node.Metadata["version"] = 2
	stale, _ := json.Marshal(node)
	if _, err := db.ExecContext(ctx, `UPDATE index_nodes SET payload_json=? WHERE node_id=?`, string(stale), node.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := index.ProspectiveObligations(ctx, 50); err == nil {
		t.Fatal("stale prospective index version was accepted")
	}
}
