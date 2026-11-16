package hindsight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type skippedHindsightProjector struct{}

func (skippedHindsightProjector) BackendID() string                       { return "hindsight" }
func (skippedHindsightProjector) RequiresNative(memory.MemoryRecord) bool { return false }
func (skippedHindsightProjector) Project(context.Context, memory.MemoryRecord, string, int64) (store.ProjectionResult, error) {
	return store.ProjectionResult{Verified: true, Skipped: true}, nil
}

func preparedRecord(t *testing.T) (*store.Writer, *store.Index, memory.MemoryRecord, store.WriteReceipt) {
	t.Helper()
	fixture, err := os.ReadFile("../../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := store.CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := store.OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	index, err := store.OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { index.Close() })
	receipt, err := writer.PrepareRootWrite(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	return writer, index, record, receipt
}

func TestProjectionRecoversAfterNativeWriteBeforeJournalCommit(t *testing.T) {
	writer, index, record, pending := preparedRecord(t)
	bankID, err := partitionID(record)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	var retainCalls, bankCalls, getCalls int
	var tamperHit bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("native request lost authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !strings.Contains(r.URL.Path, bankID) {
			t.Errorf("request escaped expected native partition: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/documents/"):
			getCalls++
			if document == nil || getCalls == 2 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(document)
		case r.Method == http.MethodPut:
			bankCalls++
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memories"):
			retainCalls++
			var request struct {
				Async bool `json:"async"`
				Items []struct {
					Content    string         `json:"content"`
					DocumentID string         `json:"document_id"`
					Metadata   map[string]any `json:"metadata"`
					Tags       []string       `json:"tags"`
				} `json:"items"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request.Async || len(request.Items) != 1 || request.Items[0].Content != record.Content ||
				request.Items[0].Tags[0] != "um_mem:block:leaf" ||
				request.Items[0].Metadata["commit_revision"] != "1" {
				t.Errorf("native retain contract differs: %+v", request)
			}
			item := request.Items[0]
			document = map[string]any{
				"id": item.DocumentID, "bank_id": bankID,
				"original_text": item.Content, "document_metadata": item.Metadata,
			}
			w.Write([]byte(`{"success":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/memories/recall"):
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if request["tags_match"] != "all_strict" {
				t.Errorf("native recall lost strict block tag: %v", request)
			}
			metadata := document["document_metadata"].(map[string]any)
			content := record.Content
			if tamperHit {
				content = "forged content"
			}
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{
				"text": content, "tags": []string{"um_mem:block:leaf"},
				"metadata": metadata,
			}}})
		default:
			t.Errorf("unexpected native request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	projector := Projector{BaseURL: server.URL, APIKey: "token", Client: server.Client()}
	ctx := context.Background()
	if _, err := writer.CompleteProjection(ctx, pending.CommitRevision, projector); err == nil {
		t.Fatal("unreadable native write became committed")
	}
	if visible, err := index.ReadStoredRecord(ctx, record.BlockID); err != nil || visible != nil {
		t.Fatalf("pending record became visible: %v, %v", visible, err)
	}
	pendingPlan := memory.NewRecallPlan("Bound")
	pendingPlan.WorkspaceID, pendingPlan.ProfileID = "alpha", "team"
	pendingPlan.LogicalBlockIDs = []string{"leaf"}
	pendingPlan.ScopeChain = []string{"workspace:alpha"}
	pendingPlan.IncludeCandidates = true
	if _, err := projector.Recall(ctx, index, pendingPlan); err == nil {
		t.Fatal("native recall ignored pending projection")
	}
	finished, err := writer.CompleteProjection(ctx, pending.CommitRevision, projector)
	if err != nil || finished.ProjectionPending || finished.Replayed {
		t.Fatalf("retry did not finish native projection: %+v, %v", finished, err)
	}
	if retainCalls != 1 || bankCalls != 1 || getCalls != 3 {
		t.Fatalf("recovery repeated native retain: retain=%d bank=%d get=%d", retainCalls, bankCalls, getCalls)
	}
	visible, err := index.ReadStoredRecord(ctx, record.BlockID)
	if err != nil || visible == nil || visible.Content != record.Content {
		t.Fatalf("committed native record not visible: %v, %v", visible, err)
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = "alpha", "team"
	plan.RecordIDs = []string{record.BlockID}
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	exact, err := index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != record.BlockID {
		t.Fatalf("exact recall failed after native commit: %v, %v", exact, err)
	}
	plan.IncludeCandidates = false
	exact, err = index.ReadExactRecords(ctx, plan)
	if err != nil || len(exact) != 0 {
		t.Fatalf("candidate escaped exact recall policy: %v, %v", exact, err)
	}
	plan.IncludeCandidates = true
	plan.WorkspaceID = "other"
	if _, err := index.ReadExactRecords(ctx, plan); err == nil {
		t.Fatal("cross-workspace exact recall was accepted")
	}
	plan.WorkspaceID = "alpha"
	plan.RecordIDs = nil
	plan.LogicalBlockIDs = []string{"leaf"}
	plan.QueryType = ""
	plan.Query = "Bound"
	plan.NormalizedQuery = ""
	native, err := projector.Recall(ctx, index, plan)
	if err != nil || len(native) != 1 || native[0].BlockID != record.BlockID {
		t.Fatalf("native recall did not return committed record: %v, %v", native, err)
	}
	tamperHit = true
	if _, err := projector.Recall(ctx, index, plan); err == nil {
		t.Fatal("native recall accepted forged source text")
	}
	plan.Facets = map[string][]string{"primary_block_id": {"leaf"}}
	exact, err = projector.Recall(ctx, index, plan)
	if err != nil || len(exact) != 1 || exact[0].BlockID != record.BlockID {
		t.Fatalf("exact facet did not route through canonical index: %v, %v", exact, err)
	}
	plan.Facets = nil
	plan.QueryType = ""
	plan.TemporalMode = "historical"
	plan.AsOf = "2024-01-15T00:00:00Z"
	historical, err := projector.Recall(ctx, index, plan)
	if err != nil || len(historical) != 1 || historical[0].BlockID != record.BlockID {
		t.Fatalf("historical Hindsight recall did not use canonical text index: %v, %v", historical, err)
	}
	replayed, err := writer.CompleteProjection(ctx, pending.CommitRevision, projector)
	if err != nil || !replayed.Replayed || retainCalls != 1 {
		t.Fatalf("committed journal replay was not idempotent: %+v, %v", replayed, err)
	}
	tamperHit = false
	source := record
	source.BlockID = "dependency_source"
	source.RevisionID = "rev_dependency_source"
	source.CanonicalKey = "alpha:dependency-source"
	source.Content = "Source fact"
	sourceMembership := *source.BlockMembership
	sourceMembership.RecordID = source.BlockID
	sourceMembership.RecordRevisionID = source.RevisionID
	sourceMembership.PrimaryBlockID = "source_leaf"
	sourceMembership.MembershipRevisionID = sourceMembership.RevisionHash()
	source.BlockMembership = &sourceMembership
	sourcePending, err := writer.PrepareRootWrite(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, sourcePending.CommitRevision, skippedHindsightProjector{}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AddEdge(ctx, memory.EdgeRecord{SrcID: record.BlockID, DstID: source.BlockID,
		EdgeType: "derived_from", CreatedAt: "2024-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	plan.TemporalMode = "current"
	plan.AsOf = ""
	plan.QueryType = ""
	plan.NormalizedQuery = ""
	if valid, err := projector.Recall(ctx, index, plan); err != nil || len(valid) != 1 {
		t.Fatalf("native recall lost valid derivation: %+v, %v", valid, err)
	}
	corrected := source
	corrected.BlockID = "dependency_source_corrected"
	corrected.RevisionID = "rev_dependency_source_2"
	corrected.RevisionOf = source.BlockID
	corrected.SupersedesIDs = []string{source.BlockID}
	corrected.ValidFrom = "2024-02-01T00:00:00Z"
	corrected.RecordedAt = corrected.ValidFrom
	corrected.UpdatedAt = corrected.ValidFrom
	correctedMembership := *corrected.BlockMembership
	correctedMembership.RecordID = corrected.BlockID
	correctedMembership.RecordRevisionID = corrected.RevisionID
	correctedMembership.ValidFrom = corrected.ValidFrom
	correctedMembership.MembershipRevisionID = correctedMembership.RevisionHash()
	corrected.BlockMembership = &correctedMembership
	correctionPending, err := writer.PrepareSuccessorWrite(ctx, corrected, "correction", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CompleteProjection(ctx, correctionPending.CommitRevision, skippedHindsightProjector{}); err != nil {
		t.Fatal(err)
	}
	invalid, err := projector.Recall(ctx, index, plan)
	if err != nil || len(invalid) != 0 {
		t.Fatalf("native hit escaped corrected source dependency: %+v, %v", invalid, err)
	}
}

func TestProjectionRejectsMismatchedNativeDocument(t *testing.T) {
	writer, index, record, pending := preparedRecord(t)
	bankID, err := partitionID(record)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("mismatched native document caused %s", r.Method)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "UM-MEM:block:" + record.BlockID,
			"bank_id": bankID, "original_text": record.Content,
			"document_metadata": map[string]string{
				"block_id": record.BlockID, "workspace_id": "other",
			},
		})
	}))
	defer server.Close()
	_, err = writer.CompleteProjection(context.Background(), pending.CommitRevision,
		Projector{BaseURL: server.URL, Client: server.Client()})
	if err == nil {
		t.Fatal("mismatched native document was accepted")
	}
	visible, err := index.ReadStoredRecord(context.Background(), record.BlockID)
	if err != nil || visible != nil {
		t.Fatalf("mismatched native document became visible: %v, %v", visible, err)
	}
}

func TestPartitionIdentityUsesPythonASCIIEncoding(t *testing.T) {
	record := memory.MemoryRecord{
		WorkspaceID: "альфа&<",
		BlockMembership: &memory.RecordBlockMembership{
			PrimaryBlockID: "leaf", CatalogRevisionID: "cat:v1",
			BackendPartitionID: "static:demo",
		},
	}
	material, err := jsoncompat.MarshalPython([]string{record.WorkspaceID, record.BlockMembership.BackendPartitionID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(material), `["\u0430\u043b\u044c\u0444\u0430&<","static:demo"]`; got != want {
		t.Fatalf("bank identity serialization differs from Python: %q", got)
	}
	first, err := partitionID(record)
	if err != nil || !strings.HasPrefix(first, "um-mem-bank-") || len(first) != len("um-mem-bank-")+64 {
		t.Fatalf("invalid native partition: %q, %v", first, err)
	}
}
