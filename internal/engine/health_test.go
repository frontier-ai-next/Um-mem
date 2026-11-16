package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
)

func TestHealthChecksSelectedNativeBackendAndProjectionJournal(t *testing.T) {
	var ready atomic.Bool
	native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			t.Errorf("unexpected Hindsight health request: %s %s", request.Method, request.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer native.Close()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "hindsight", HindsightBaseURL: native.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	unavailable, err := selected.Health(context.Background())
	if err != nil || unavailable["ok"] != false || unavailable["native_ok"] != false ||
		unavailable["effective_mode"] != "unavailable" {
		t.Fatalf("unavailable Hindsight health differs: %+v, %v", unavailable, err)
	}
	ready.Store(true)
	healthy, err := selected.Health(context.Background())
	if err != nil || healthy["ok"] != true || healthy["native_ok"] != true ||
		healthy["strict_ok"] != true || healthy["effective_mode"] != "native" ||
		healthy["workspace_id"] != "alpha" ||
		healthy["schema_revision"] != "um-mem-backend-index-schema:16" ||
		healthy["index_revision"] != healthy["canonical_snapshot_sha256"] {
		t.Fatalf("healthy Hindsight status differs: %+v, %v", healthy, err)
	}
	projection, ok := healthy["projection"].(store.ProjectionHealth)
	if !ok || projection.Pending != 0 || projection.Failed != 0 ||
		projection.Recovery != "reconcile" {
		t.Fatalf("health lost native projection journal state: %+v", healthy)
	}
}
