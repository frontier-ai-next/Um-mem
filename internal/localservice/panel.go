package localservice

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
)

const panelSessionTTL = 12 * time.Hour

const panelCSP = "default-src 'self'; script-src 'self'; style-src 'self'; " +
	"connect-src 'self'; img-src 'self' data:; object-src 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'"

type Panel struct {
	mu          sync.Mutex
	active      bool
	accessToken string
	sessions    map[string]time.Time
	lastBrowser time.Time
	streams     int
	feed        *ChangeFeed
	streamSlots chan struct{}
	selected    *engine.Engine
}

func randomPanelToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func NewPanel() (*Panel, error) {
	feed, err := NewChangeFeed(256)
	if err != nil {
		return nil, err
	}
	token, err := randomPanelToken()
	if err != nil {
		return nil, err
	}
	return &Panel{accessToken: token, feed: feed,
		sessions: map[string]time.Time{}, streamSlots: make(chan struct{}, 16)}, nil
}

func (panel *Panel) activate() (string, error) {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	if !panel.active {
		feed, err := NewChangeFeed(256)
		if err != nil {
			return "", err
		}
		token, err := randomPanelToken()
		if err != nil {
			return "", err
		}
		panel.feed.Wake()
		panel.feed, panel.accessToken = feed, token
		panel.sessions = map[string]time.Time{}
		panel.active = true
	}
	panel.lastBrowser = time.Now()
	return panel.accessToken, nil
}

func (panel *Panel) deactivate(idleOnly bool) bool {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	if idleOnly && (!panel.active || panel.streams != 0 || time.Since(panel.lastBrowser) <= panel.idleTimeout()) {
		return false
	}
	panel.active = false
	panel.sessions = map[string]time.Time{}
	panel.accessToken = ""
	panel.feed.Wake()
	return true
}

// idleTimeout keeps the panel usable for a working day instead of expiring one
// minute after the last click, which read as "the panel does not open".
func (panel *Panel) idleTimeout() time.Duration {
	if raw := os.Getenv("UM_MEM_PANEL_IDLE"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 8 * time.Hour
}

func (panel *Panel) expireIfIdle() { panel.deactivate(true) }

func (panel *Panel) isActive() bool {
	panel.mu.Lock()
	defer panel.mu.Unlock()
	return panel.active
}

func (panel *Panel) session(request *http.Request, port int) string {
	cookie, err := request.Cookie("um_mem_panel_" + strconv.Itoa(port))
	if err != nil {
		return ""
	}
	panel.mu.Lock()
	defer panel.mu.Unlock()
	expiry, known := panel.sessions[cookie.Value]
	if !known || !expiry.After(time.Now()) {
		delete(panel.sessions, cookie.Value)
		return ""
	}
	return cookie.Value
}

func panelHeaders(writer http.ResponseWriter, contentType string) {
	writer.Header().Set("Content-Type", contentType+"; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writer.Header().Set("Content-Security-Policy", panelCSP)
}

func panelJSON(writer http.ResponseWriter, code int, payload map[string]any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		code, raw = http.StatusInternalServerError, []byte(`{"error":"Panel response unavailable"}`)
	}
	panelHeaders(writer, "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	writer.WriteHeader(code)
	_, _ = writer.Write(raw)
}

func panelBody(request *http.Request) (map[string]any, int, string) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, http.StatusUnsupportedMediaType, "JSON required"
	}
	if request.ContentLength < 1 || request.ContentLength > 524288 {
		return nil, http.StatusRequestEntityTooLarge, "Invalid request size"
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, 524289))
	if err != nil || len(raw) > 524288 {
		return nil, http.StatusRequestEntityTooLarge, "Invalid request size"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var body map[string]any
	if decoder.Decode(&body) != nil || body == nil {
		return nil, http.StatusBadRequest, "JSON object required"
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return nil, http.StatusBadRequest, "JSON object required"
	}
	return body, 0, ""
}

// Handle serves only the fixed panel namespace. The listener should route to
// it after its data API is complete; panel activation is off by default.
func (panel *Panel) Handle(writer http.ResponseWriter, request *http.Request, server *Server, port int) {
	origin := "http://127.0.0.1:" + strconv.Itoa(port)
	if port < 1 || request.Host != strings.TrimPrefix(origin, "http://") ||
		request.Header.Get("Origin") != "" && request.Header.Get("Origin") != origin ||
		request.Header.Get("Sec-Fetch-Site") == "cross-site" {
		panelJSON(writer, http.StatusForbidden, map[string]any{"error": "Same-origin loopback access required"})
		return
	}
	panel.expireIfIdle()
	path := request.URL.Path
	if path != "/panel/connect" && path != "/" && !panel.isActive() {
		panelJSON(writer, http.StatusGone, map[string]any{"error": "Panel is stopped. Run um-memory dashboard."})
		return
	}
	// Opening the bare port used to answer 401, which reads as "the panel is
	// broken". Send the browser to the panel with the current token instead.
	if request.Method == http.MethodGet && (path == "/" || path == "") {
		if !panel.isActive() {
			if token, err := panel.activate(); err == nil {
				http.Redirect(writer, request, "/panel/#"+token, http.StatusFound)
				return
			}
		}
		if panel.accessToken != "" {
			http.Redirect(writer, request, "/panel/#"+panel.accessToken, http.StatusFound)
			return
		}
	}
	if request.Method == http.MethodGet && (path == "/panel" || path == "/panel/") {
		panel.serveAsset(writer, "index.html")
		return
	}
	if request.Method == http.MethodGet && strings.HasPrefix(path, "/panel/assets/") {
		panel.serveAsset(writer, strings.TrimPrefix(path, "/panel/assets/"))
		return
	}
	var body map[string]any
	if request.Method == http.MethodPost {
		var code int
		var message string
		body, code, message = panelBody(request)
		if code != 0 {
			panelJSON(writer, code, map[string]any{"error": message})
			return
		}
	}
	if request.Method == http.MethodPost && path == "/panel/connect" {
		panel.connect(writer, body, server, origin)
		return
	}
	if request.Method == http.MethodPost && path == "/panel/auth" {
		panel.auth(writer, body, port)
		return
	}
	session := panel.session(request, port)
	if session == "" {
		panelJSON(writer, http.StatusUnauthorized,
			map[string]any{"error": "Open the panel with um-memory dashboard"})
		return
	}
	panel.mu.Lock()
	panel.lastBrowser = time.Now()
	panel.mu.Unlock()
	if request.Method == http.MethodPost && path == "/panel/api/stop" {
		panel.deactivate(false)
		panelJSON(writer, http.StatusOK, map[string]any{"ok": true, "state": "stopped"})
		return
	}
	if request.Method == http.MethodGet && path == "/panel/api/events" {
		cursor := request.Header.Get("Last-Event-ID")
		if cursor == "" {
			cursor = request.URL.Query().Get("since")
		}
		panel.events(writer, request, port, cursor)
		return
	}
	if panel.selected != nil {
		query := request.URL.Query()
		var payload map[string]any
		var err error
		switch {
		case request.Method == http.MethodGet && path == "/panel/api/snapshot":
			payload, err = panel.snapshot(request.Context(), server, query.Get("scope"), query.Get("scope_id"))
		case request.Method == http.MethodGet && path == "/panel/api/harness-context":
			payload = map[string]any{"ok": true, "turns": harnessContext(server.identity)}
		case request.Method == http.MethodGet && path == "/panel/api/records":
			payload, err = panel.records(request.Context(), server, query.Get("block"),
				query.Get("cursor"), query.Get("scope"), query.Get("scope_id"))
		case request.Method == http.MethodGet && path == "/panel/api/record":
			payload, err = panel.record(request.Context(), query.Get("id"))
		case request.Method == http.MethodPost && path == "/panel/api/call":
			payload, err = panel.call(request.Context(), server, session, body)
		default:
			panelJSON(writer, http.StatusNotFound, map[string]any{"error": "Unknown panel endpoint"})
			return
		}
		if err != nil {
			code := http.StatusServiceUnavailable
			message := "Memory service unavailable; retry"
			if panelBadInput(err) {
				code, message = http.StatusBadRequest, err.Error()
			}
			panelJSON(writer, code, map[string]any{"ok": false, "error": message})
			return
		}
		code := http.StatusOK
		if payload["reason_code"] == "canonical_revision_conflict" {
			code = http.StatusConflict
		}
		panelJSON(writer, code, payload)
		return
	}
	panelJSON(writer, http.StatusNotFound, map[string]any{"error": "Unknown panel endpoint"})
}

func (panel *Panel) serveAsset(writer http.ResponseWriter, name string) {
	content, contentType, err := panelAsset(name)
	if err != nil {
		panelJSON(writer, http.StatusNotFound, map[string]any{"error": "Unknown asset"})
		return
	}
	panelHeaders(writer, contentType)
	writer.Header().Set("Content-Length", strconv.Itoa(len(content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(content)
}

func (panel *Panel) connect(writer http.ResponseWriter, body map[string]any, server *Server, origin string) {
	secret, ok := body["bootstrap_secret"].(string)
	known := server.secrets["operator"]
	if frameError(body) != "" || body["caller_role"] != "operator" || !ok || known == "" ||
		subtle.ConstantTimeCompare([]byte(secret), []byte(known)) != 1 {
		panelJSON(writer, http.StatusForbidden, map[string]any{"error": "Operator authentication required"})
		return
	}
	if body["action"] == "stop" {
		panel.deactivate(false)
		panelJSON(writer, http.StatusOK, map[string]any{"ok": true, "state": "stopped"})
		return
	}
	token, err := panel.activate()
	if err != nil {
		panelJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "Panel unavailable"})
		return
	}
	panelJSON(writer, http.StatusOK, map[string]any{"ok": true, "url": origin + "/panel/#" + token})
}

func (panel *Panel) auth(writer http.ResponseWriter, body map[string]any, port int) {
	token, ok := body["token"].(string)
	session, err := randomPanelToken()
	if err != nil {
		panelJSON(writer, http.StatusServiceUnavailable, map[string]any{"error": "Panel unavailable"})
		return
	}
	panel.mu.Lock()
	if !panel.active || !ok || subtle.ConstantTimeCompare([]byte(token), []byte(panel.accessToken)) != 1 {
		panel.mu.Unlock()
		panelJSON(writer, http.StatusUnauthorized,
			map[string]any{"error": "Open the panel with um-memory dashboard"})
		return
	}
	for value, expiry := range panel.sessions {
		if !expiry.After(time.Now()) {
			delete(panel.sessions, value)
		}
	}
	if len(panel.sessions) >= 128 {
		panel.mu.Unlock()
		panelJSON(writer, http.StatusTooManyRequests, map[string]any{"error": "Too many panel sessions"})
		return
	}
	panel.sessions[session] = time.Now().Add(panelSessionTTL)
	panel.lastBrowser = time.Now()
	panel.mu.Unlock()
	http.SetCookie(writer, &http.Cookie{Name: "um_mem_panel_" + strconv.Itoa(port),
		Value: session, Path: "/panel", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		MaxAge: int(panelSessionTTL.Seconds())})
	panelJSON(writer, http.StatusOK, map[string]any{"ok": true})
}

func (panel *Panel) events(writer http.ResponseWriter, request *http.Request, port int, cursor string) {
	select {
	case panel.streamSlots <- struct{}{}:
	default:
		panelJSON(writer, http.StatusTooManyRequests, map[string]any{"error": "Too many live panels"})
		return
	}
	panel.mu.Lock()
	panel.streams++
	feed := panel.feed
	panel.mu.Unlock()
	defer func() {
		<-panel.streamSlots
		panel.mu.Lock()
		panel.streams--
		panel.lastBrowser = time.Now()
		panel.mu.Unlock()
	}()
	flusher, ok := writer.(http.Flusher)
	if !ok {
		panelJSON(writer, http.StatusInternalServerError, map[string]any{"error": "Event stream unavailable"})
		return
	}
	panelHeaders(writer, "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	for request.Context().Err() == nil {
		panel.mu.Lock()
		active := panel.active && panel.feed == feed
		panel.mu.Unlock()
		if !active {
			_, _ = io.WriteString(writer, "event: stopped\ndata: {}\n\n")
			flusher.Flush()
			return
		}
		reset, changes := feed.Since(cursor)
		if reset {
			cursor = feed.Cursor()
			_, _ = fmt.Fprintf(writer, "id: %s\nevent: reset\ndata: {}\n\n", cursor)
		}
		for _, change := range changes {
			cursor = change.ID
			payload, err := json.Marshal(change)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(writer, "id: %s\nevent: change\ndata: %s\n\n", cursor, payload)
		}
		if _, err := io.WriteString(writer, ": heartbeat\n\n"); err != nil {
			return
		}
		flusher.Flush()
		if err := feed.Wait(request.Context(), cursor, 15*time.Second); err != nil {
			return
		}
		if panel.session(request, port) == "" {
			return
		}
	}
}

func (panel *Panel) Publish(event ChangeEvent) {
	panel.mu.Lock()
	active, feed := panel.active, panel.feed
	panel.mu.Unlock()
	if active {
		feed.Publish(event)
	}
}

var panelReads = map[string]bool{"recall": true, "build_context": true,
	"build_granted_context": true, "resolve_active_mask": true}

var panelQuiet = map[string]bool{"health": true, "block_map": true, "expand_block": true,
	"navigation_envelope": true, "recent": true, "list_records": true,
	"read_exact_record": true, "read_exact_records": true, "get_active_mask": true,
	"operation_status": true, "projection_barrier": true, "memory_map": true,
	"procedure": true, "read_working_set": true, "export_graph": true,
	"export_obsidian": true}

func (panel *Panel) Observe(tool string, arguments, result map[string]any, lineage Lineage) {
	if panelQuiet[tool] || !panel.isActive() {
		return
	}
	items := panelItems(result["hits"])
	if len(items) == 0 {
		items = panelItems(result["items"])
	}
	recordIDs := make([]string, 0, min(len(items), 32))
	for _, mapping := range items[:min(len(items), 32)] {
		metadata, _ := mapping["metadata"].(map[string]any)
		id := stringValue(mapping["record_id"])
		if id == "" {
			id = stringValue(metadata["record_id"])
		}
		if id == "" {
			id = stringValue(mapping["source"])
		}
		if id != "" {
			recordIDs = append(recordIDs, id)
		}
	}
	kind := "mutation"
	if panelReads[tool] {
		kind = "retrieval"
	}
	id := stringValue(result["memory_id"])
	if id == "" {
		id = stringValue(result["block_id"])
	}
	if id == "" {
		id = stringValue(arguments["memory_id"])
	}
	rendered, _ := result["rendered_context"].(string)
	panel.Publish(ChangeEvent{Kind: kind, Operation: tool, OK: result["ok"] == true,
		SessionID: lineage.SessionID, TaskID: lineage.TaskID, RecordID: id,
		RecordIDs: recordIDs, Count: len(items), Rendered: rendered != "",
		CommitRevision:     stringValue(result["commit_revision"]),
		ProjectionRevision: stringValue(result["projection_revision"]),
		ReasonCode:         stringValue(result["reason_code"])})
}

func panelItems(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		result := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if mapping, ok := item.(map[string]any); ok {
				result = append(result, mapping)
			}
		}
		return result
	default:
		return nil
	}
}
