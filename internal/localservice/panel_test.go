package localservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func panelRequest(t *testing.T, panel *Panel, server *Server, method, path string,
	body map[string]any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, "http://127.0.0.1:8181"+path, bytes.NewReader(encoded))
	if body != nil {
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	panel.Handle(response, request, server, 8181)
	return response
}

func TestPanelActivationAuthAndStaticBoundary(t *testing.T) {
	panel, err := NewPanel()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"},
		map[string]string{"operator": "operator-secret"},
		func(context.Context, Caller, string, map[string]any) map[string]any {
			return map[string]any{"ok": true}
		})
	if err != nil {
		t.Fatal(err)
	}
	if response := panelRequest(t, panel, server, http.MethodGet, "/panel/", nil, nil); response.Code != http.StatusGone {
		t.Fatalf("inactive panel served assets: %d", response.Code)
	}
	frame := map[string]any{"protocol_version": 1, "schema_version": 2,
		"api_version": 2, "request_id": "panel-connect", "caller_role": "operator",
		"bootstrap_secret": "wrong"}
	if response := panelRequest(t, panel, server, http.MethodPost, "/panel/connect", frame, nil); response.Code != http.StatusForbidden {
		t.Fatalf("wrong operator secret activated panel: %d", response.Code)
	}
	frame["bootstrap_secret"] = "operator-secret"
	response := panelRequest(t, panel, server, http.MethodPost, "/panel/connect", frame, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("operator activation failed: %d %s", response.Code, response.Body.String())
	}
	var connected map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &connected); err != nil {
		t.Fatal(err)
	}
	url, ok := connected["url"].(string)
	if !ok || !strings.HasPrefix(url, "http://127.0.0.1:8181/panel/#") {
		t.Fatalf("panel capability URL: %#v", connected)
	}
	token := strings.TrimPrefix(url, "http://127.0.0.1:8181/panel/#")
	asset := panelRequest(t, panel, server, http.MethodGet, "/panel/", nil, nil)
	if asset.Code != http.StatusOK || !strings.Contains(asset.Body.String(), "Карта знаний") ||
		strings.Contains(asset.Body.String(), token) ||
		asset.Header().Get("Content-Security-Policy") == "" ||
		asset.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("static panel shell crossed the secret boundary: %d", asset.Code)
	}
	for _, name := range []string{"app.js", "style.css", "refinement.css",
		"branch-records.mjs", "graph-layout.mjs"} {
		response := panelRequest(t, panel, server, http.MethodGet, "/panel/assets/"+name, nil, nil)
		if response.Code != http.StatusOK || response.Body.Len() == 0 ||
			response.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("panel asset %s unavailable: %d", name, response.Code)
		}
	}
	if unknown := panelRequest(t, panel, server, http.MethodGet, "/panel/assets/secret.txt", nil, nil); unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown asset was served: %d", unknown.Code)
	}
	if unauth := panelRequest(t, panel, server, http.MethodGet, "/panel/api/events", nil, nil); unauth.Code != http.StatusUnauthorized {
		t.Fatalf("event stream lacked cookie gate: %d", unauth.Code)
	}
	response = panelRequest(t, panel, server, http.MethodPost, "/panel/auth",
		map[string]any{"token": token}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("capability exchange failed: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "um_mem_panel_8181" ||
		!cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode ||
		cookies[0].Path != "/panel" {
		t.Fatalf("panel cookie contract: %#v", cookies)
	}
	if stopped := panelRequest(t, panel, server, http.MethodPost, "/panel/api/stop",
		map[string]any{}, cookies[0]); stopped.Code != http.StatusOK || panel.isActive() {
		t.Fatalf("authenticated panel stop failed: %d", stopped.Code)
	}
	if after := panelRequest(t, panel, server, http.MethodGet, "/panel/", nil, nil); after.Code != http.StatusGone {
		t.Fatalf("stopped panel still served assets: %d", after.Code)
	}
}

func TestPanelRejectsCrossOriginAndStreamsContentFreeEvents(t *testing.T) {
	panel, err := NewPanel()
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"},
		map[string]string{"operator": "secret"},
		func(context.Context, Caller, string, map[string]any) map[string]any {
			return map[string]any{"ok": true}
		})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panel.activate(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8181/panel/", nil)
	request.Header.Set("Origin", "https://example.invalid")
	response := httptest.NewRecorder()
	panel.Handle(response, request, server, 8181)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin panel request was accepted: %d", response.Code)
	}
	panel.sessions["session"] = time.Now().Add(time.Hour)
	cursor := panel.feed.Cursor()
	panel.Publish(ChangeEvent{Kind: "mutation", Operation: "remember", OK: true,
		RecordID: "record-1"})
	streamCtx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8181/panel/api/events?since="+cursor, nil)
	request = request.WithContext(streamCtx)
	request.AddCookie(&http.Cookie{Name: "um_mem_panel_8181", Value: "session"})
	response = httptest.NewRecorder()
	panel.Handle(response, request, server, 8181)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "event: change") ||
		!strings.Contains(response.Body.String(), "record-1") ||
		response.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-free event stream failed: %d %s", response.Code, response.Body.String())
	}
}
