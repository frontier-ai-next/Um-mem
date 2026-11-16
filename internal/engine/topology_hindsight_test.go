package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestSplitBlockStagesAndRecallsHindsightTarget(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	documents := make(map[string]map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		parts := strings.Split(request.URL.Path, "/")
		if len(parts) < 5 || parts[1] != "v1" || parts[2] != "default" || parts[3] != "banks" {
			t.Errorf("unexpected Hindsight path: %s", request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bankID := parts[4]
		mu.Lock()
		defer mu.Unlock()
		switch {
		case request.Method == http.MethodPut && len(parts) == 5:
			w.Write([]byte(`{}`))
		case request.Method == http.MethodGet && len(parts) == 7 && parts[5] == "documents":
			document := documents[bankID][parts[6]]
			if document == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(document)
		case request.Method == http.MethodPost && len(parts) == 6 && parts[5] == "memories":
			var body struct {
				Items []struct {
					Content    string         `json:"content"`
					DocumentID string         `json:"document_id"`
					Metadata   map[string]any `json:"metadata"`
					Tags       []string       `json:"tags"`
				} `json:"items"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Items) != 1 {
				t.Errorf("invalid Hindsight retain: %+v, %v", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			item := body.Items[0]
			if documents[bankID] == nil {
				documents[bankID] = make(map[string]map[string]any)
			}
			documents[bankID][item.DocumentID] = map[string]any{"id": item.DocumentID,
				"bank_id": bankID, "original_text": item.Content,
				"document_metadata": item.Metadata, "tags": item.Tags}
			w.Write([]byte(`{"success":true}`))
		case request.Method == http.MethodPost && len(parts) == 7 &&
			parts[5] == "memories" && parts[6] == "recall":
			var body struct {
				Tags []string `json:"tags"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body.Tags) != 1 {
				t.Errorf("invalid Hindsight recall: %+v, %v", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			results := make([]any, 0)
			for _, document := range documents[bankID] {
				tags := document["tags"].([]string)
				if len(tags) == 0 || tags[0] != body.Tags[0] {
					continue
				}
				results = append(results, map[string]any{"text": document["original_text"],
					"tags": tags, "metadata": document["document_metadata"]})
			}
			json.NewEncoder(w).Encode(map[string]any{"results": results})
		default:
			t.Errorf("unexpected Hindsight request: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "hindsight", HindsightBaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	blocks, err := selected.topology.ListBlocks(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Hindsight source",
		Synopsis: "Source facts", ParentID: blocks[0].Block.BlockID,
		Reason: "prepare split", Scope: "workspace"})
	if err != nil {
		t.Fatal(err)
	}
	sourceID := created["block_id"].(string)
	written, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "The source is durable.",
		RequestedBlockID: sourceID, SessionID: "session-1", RunID: "session-1"})
	if err != nil || !written.Saved {
		t.Fatalf("Hindsight source was not saved: %+v, %v", written, err)
	}
	receipt, err := selected.SplitBlock(ctx, SplitBlockRequest{BlockID: sourceID,
		ExpectedRevision: 1, Reason: "relocate source fact", OperationID: "hindsight-split",
		Targets: []BlockEvolutionTarget{{Name: "Hindsight target", Synopsis: "Target facts",
			RecordIDs: []string{written.RecordID}}}})
	if err != nil || receipt["generation_status"] != "published" {
		t.Fatalf("Hindsight split was not published: %+v, %v", receipt, err)
	}
	targetID := receipt["targets"].([]map[string]any)[0]["block_id"].(string)
	mu.Lock()
	bankCount := len(documents)
	var targetDocument map[string]any
	for _, bank := range documents {
		for _, document := range bank {
			metadata := document["document_metadata"].(map[string]any)
			if metadata["primary_block_id"] == targetID {
				targetDocument = document
			}
		}
	}
	mu.Unlock()
	if bankCount != 2 || targetDocument == nil ||
		targetDocument["id"] != "UM-MEM:block:"+written.RecordID {
		t.Fatalf("Hindsight did not stage a separate target bank: banks=%d target=%+v",
			bankCount, targetDocument)
	}
	plan := memory.NewRecallPlan("durable")
	plan.WorkspaceID, plan.ProfileID, plan.RunID = "alpha", "team", "session-1"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.LogicalBlockIDs = []string{targetID}
	plan.IncludeCandidates = true
	recalled, err := selected.Recall(ctx, plan)
	if err != nil || len(recalled) != 1 || recalled[0].BlockID != written.RecordID ||
		recalled[0].BlockMembership.PrimaryBlockID != targetID {
		t.Fatalf("Hindsight target native recall differs: %+v, %v", recalled, err)
	}
}
