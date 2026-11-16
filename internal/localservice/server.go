// Package localservice implements the authenticated loopback transport used by
// UM-MEM host integrations. Registration and process lifecycle are separate;
// readiness reflects the running Go listener's dependencies and native state.
package localservice

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/operationalslo"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/scheduler"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/servicepolicy"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/servicequeue"
	"golang.org/x/text/unicode/norm"
)

const (
	ProtocolVersion = 1
	SchemaVersion   = 2
	APIVersion      = 2
	MaxMessageBytes = 1_048_576
	LeaseTTL        = 120 * time.Second
)

var mutationTools = map[string]bool{
	"observe": true, "remember": true, "amend": true, "promote": true,
	"create_block": true, "convert_block_to_index": true, "amend_block": true,
	"move_block": true, "archive_block": true, "revive_block": true,
	"delete_block": true, "split_block": true, "merge_blocks": true,
	"resume_topology_evolution": true, "abort_topology_evolution": true,
	"add_block_edge": true, "remove_block_edge": true, "add_edge": true,
	"set_active_mask": true, "amend_active_mask": true,
	"transition_active_mask": true, "observe_maintenance": true,
	"consolidate_observations": true, "commit_working_checkpoint": true,
	"forget": true, "rebuild_indexes": true,
}

//go:embed role_capabilities.json
var roleCapabilitiesJSON []byte

type Identity struct {
	RepoRoot    string
	WorkspaceID string
	ProfileID   string
	Backend     string
}

type Lineage struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	SessionID   string `json:"session_id"`
	TaskID      string `json:"task_id"`
	SubtaskID   string `json:"subtask_id"`
	UserID      string `json:"user_id"`
}

type Caller struct {
	Identity             Identity
	Role                 string
	Lineage              Lineage
	PolicyBundleRevision string
	CatalogRevision      string
}

type Dispatcher func(context.Context, Caller, string, map[string]any) map[string]any

type lease struct {
	role                 string
	sessionID            string
	lineage              Lineage
	expires              time.Time
	policyBundleRevision string
	catalogRevision      string
}

type Server struct {
	identity        Identity
	secrets         map[string]string
	capabilities    map[string][]string
	dispatch        Dispatcher
	policy          *servicepolicy.Store
	scheduler       *scheduler.Store
	workQueue       *servicequeue.Store
	workQueueErr    bool
	backgroundReady bool
	panel           *Panel
	selected        *engine.Engine
	listenerPort    int
	activeRequests  int
	lastWork        time.Time
	dispatchSlots   chan struct{}
	waitingCalls    int
	successfulCalls int64
	failedCalls     int64
	mu              sync.Mutex
	leases          map[string]lease
	now             func() time.Time
	shutdown        chan struct{}
	draining        bool
}

func (server *Server) ConfigurePanel(selected *engine.Engine) error {
	if selected == nil {
		return errors.New("local service panel requires an engine")
	}
	workspace, profile := selected.Scope()
	root, err := normalizeRoot(selected.RepoRoot())
	if err != nil || root != server.identity.RepoRoot || workspace != server.identity.WorkspaceID ||
		profile != server.identity.ProfileID || selected.BackendID() != server.identity.Backend {
		return errors.New("local service panel engine identity does not match listener")
	}
	panel, err := NewPanel()
	if err != nil {
		return err
	}
	panel.selected = selected
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.listenerPort != 0 || len(server.leases) != 0 {
		return errors.New("local service panel cannot be configured after listener startup")
	}
	server.panel = panel
	server.selected = selected
	return nil
}

// ConfigurePolicy attaches the persistent session-pin store before the
// listener starts. A listener without a valid policy snapshot stays not ready.
func (server *Server) ConfigurePolicy(policy *servicepolicy.Store) error {
	if policy == nil {
		return errors.New("local service policy store is required")
	}
	identity := policy.Identity()
	policyRoot, err := normalizeRoot(identity.RepoRoot)
	if err != nil || policyRoot != server.identity.RepoRoot ||
		identity.WorkspaceID != server.identity.WorkspaceID ||
		identity.ProfileID != server.identity.ProfileID ||
		identity.Backend != server.identity.Backend {
		return errors.New("local service policy identity does not match listener")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.leases) != 0 {
		return errors.New("local service policy cannot change after leases are issued")
	}
	server.policy = policy
	return nil
}

func (server *Server) ConfigureScheduler(queue *scheduler.Store) error {
	if queue == nil || queue.WorkspaceID() != server.identity.WorkspaceID {
		return errors.New("local service scheduler workspace is invalid")
	}
	_, digest, err := SchedulerPath(server.identity)
	if err != nil || queue.IdentitySHA256() != digest {
		return errors.New("local service scheduler identity does not match listener")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.leases) != 0 {
		return errors.New("local service scheduler cannot change after leases are issued")
	}
	server.scheduler = queue
	return nil
}

func (server *Server) ConfigureWorkQueue(queue *servicequeue.Store) error {
	if queue == nil {
		return errors.New("local service work queue is required")
	}
	want, err := WorkQueuePath(server.identity)
	if err != nil || queue.Path() != want {
		return errors.New("local service work queue identity does not match listener")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.leases) != 0 {
		return errors.New("local service work queue cannot change after leases are issued")
	}
	server.workQueue = queue
	return nil
}

func normalizeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("repo root is required")
	}
	path, err := filepath.Abs(norm.NFC.String(strings.TrimSpace(root)))
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	path = norm.NFC.String(filepath.Clean(path))
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path, nil
}

// New requires separately provisioned, role-specific bootstrap credentials.
// Missing role credentials cannot issue leases. It does not create a registry
// file or replace an existing listener.
func New(identity Identity, secrets map[string]string, dispatch Dispatcher) (*Server, error) {
	root, err := normalizeRoot(identity.RepoRoot)
	if err != nil {
		return nil, err
	}
	identity.RepoRoot = root
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" {
		return nil, errors.New("local service workspace identity is invalid")
	}
	identity.WorkspaceID = workspace
	if identity.ProfileID == "." || identity.ProfileID == ".." ||
		strings.ContainsAny(identity.ProfileID, "/\\\x00") ||
		identity.ProfileID != strings.TrimSpace(identity.ProfileID) {
		return nil, errors.New("local service profile identity is invalid")
	}
	if identity.WorkspaceID == "" || identity.ProfileID == "" || identity.Backend == "" || dispatch == nil {
		return nil, errors.New("local service requires fixed identity and dispatcher")
	}
	var capabilities map[string][]string
	if err := json.Unmarshal(roleCapabilitiesJSON, &capabilities); err != nil {
		return nil, err
	}
	for role, secret := range secrets {
		if _, ok := capabilities[role]; !ok || secret == "" {
			return nil, errors.New("local service has an invalid role credential")
		}
	}
	credentials := make(map[string]string, len(secrets))
	for role, secret := range secrets {
		credentials[role] = secret
	}
	return &Server{identity: identity, secrets: credentials, capabilities: capabilities,
		dispatch: dispatch, leases: make(map[string]lease), now: time.Now,
		lastWork: time.Now(), dispatchSlots: make(chan struct{}, 4),
		shutdown: make(chan struct{})}, nil
}

func failure(message string) map[string]any { return map[string]any{"ok": false, "error": message} }

func integer(value any, expected int) bool {
	number, ok := value.(json.Number)
	return ok && number.String() == strconv.Itoa(expected)
}

func frameError(request map[string]any) string {
	if !integer(request["protocol_version"], ProtocolVersion) {
		return "local service protocol version mismatch"
	}
	if !integer(request["schema_version"], SchemaVersion) {
		return "local service schema version mismatch"
	}
	if !integer(request["api_version"], APIVersion) {
		return "local service API version mismatch"
	}
	if id, ok := request["request_id"].(string); !ok || id == "" {
		return "local service request_id is required"
	}
	return ""
}

func (server *Server) issueLease(request map[string]any) map[string]any {
	if err := frameError(request); err != "" {
		return failure(err)
	}
	server.mu.Lock()
	draining := server.draining
	server.mu.Unlock()
	if draining {
		return failure("local service is draining")
	}
	role, ok := request["caller_role"].(string)
	capabilities, allowed := server.capabilities[role]
	if !ok || !allowed {
		return failure("local service caller role is not allowed")
	}
	sessionID, ok := request["session_id"].(string)
	if !ok || strings.TrimSpace(sessionID) == "" || len(sessionID) > 256 {
		return failure("local service session identity rejected")
	}
	provided, ok := request["capabilities"].([]any)
	if !ok || len(provided) != len(capabilities) {
		return failure("local service capabilities mismatch")
	}
	for i, capability := range capabilities {
		if provided[i] != capability {
			return failure("local service capabilities mismatch")
		}
	}
	secret, ok := request["bootstrap_secret"].(string)
	known := server.secrets[role]
	if !ok || known == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(known)) != 1 {
		return failure("local service bootstrap authentication rejected")
	}
	lineage := Lineage{WorkspaceID: server.identity.WorkspaceID, SessionID: strings.TrimSpace(sessionID)}
	if raw, exists := request["scope_lineage"]; exists && raw != nil {
		fields, ok := raw.(map[string]any)
		if !ok {
			return failure("local service scope lineage rejected")
		}
		values := map[string]string{}
		for key, value := range fields {
			if !slices.Contains([]string{"workspace_id", "project_id", "session_id", "task_id", "subtask_id", "user_id"}, key) {
				return failure("local service scope lineage rejected")
			}
			text, ok := value.(string)
			if !ok {
				return failure("local service scope lineage rejected")
			}
			values[key] = text
		}
		if values["workspace_id"] != server.identity.WorkspaceID || values["session_id"] != strings.TrimSpace(sessionID) {
			return failure("local service scope lineage rejected")
		}
		lineage.ProjectID, lineage.TaskID = values["project_id"], values["task_id"]
		lineage.SubtaskID, lineage.UserID = values["subtask_id"], values["user_id"]
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return failure("local service lease generation failed")
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	server.mu.Lock()
	policy := server.policy
	server.mu.Unlock()
	policyRevision, catalogRevision := "", ""
	if policy != nil {
		bundle, err := policy.PinSession(strings.TrimSpace(sessionID))
		if err != nil {
			return failure("local service policy bundle unavailable")
		}
		if _, err := policy.SnapshotFor(bundle.RevisionID); err != nil {
			return failure("local service policy bundle unavailable")
		}
		policyRevision, catalogRevision = bundle.RevisionID, bundle.CatalogRevisionID
	}
	server.mu.Lock()
	if server.draining {
		server.mu.Unlock()
		return failure("local service is draining")
	}
	server.leases[token] = lease{role: role, sessionID: strings.TrimSpace(sessionID), lineage: lineage,
		expires: server.now().Add(LeaseTTL), policyBundleRevision: policyRevision,
		catalogRevision: catalogRevision}
	for key, entry := range server.leases {
		if !entry.expires.After(server.now()) {
			delete(server.leases, key)
		}
	}
	server.mu.Unlock()
	response := map[string]any{"ok": true, "lease": token, "expires_in_seconds": 120,
		"capabilities": capabilities, "workspace_id": server.identity.WorkspaceID,
		"profile_id": server.identity.ProfileID, "selected_backend": server.identity.Backend}
	if policy != nil {
		response["policy_bundle_revision"] = policyRevision
		response["catalog_revision"] = catalogRevision
	}
	return response
}

func (server *Server) authorizedIdentity(arguments map[string]any) bool {
	for _, field := range []string{"repo_root", "workspace_id", "backend"} {
		if value, exists := arguments[field]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	if value, _ := arguments["repo_root"].(string); value != "" {
		root, err := normalizeRoot(value)
		if err != nil || root != server.identity.RepoRoot {
			return false
		}
	}
	if value, _ := arguments["workspace_id"].(string); value != "" && value != server.identity.WorkspaceID {
		return false
	}
	if value, _ := arguments["backend"].(string); value != "" && value != server.identity.Backend {
		return false
	}
	return true
}

func (server *Server) call(ctx context.Context, request map[string]any) map[string]any {
	if err := frameError(request); err != "" {
		return failure(err)
	}
	server.mu.Lock()
	draining := server.draining
	selected, listenerPort := server.selected, server.listenerPort
	server.mu.Unlock()
	if draining {
		return failure("local service is draining")
	}
	if deadline, exists := request["deadline_ns"]; exists {
		number, ok := deadline.(json.Number)
		if !ok {
			return failure("local service deadline is invalid")
		}
		positive, err := number.Int64()
		if err != nil || positive <= 0 {
			return failure("local service deadline is invalid")
		}
	}
	token, ok := request["lease"].(string)
	if !ok || token == "" {
		return failure("local service lease rejected")
	}
	server.mu.Lock()
	entry, exists := server.leases[token]
	if exists && !entry.expires.After(server.now()) {
		delete(server.leases, token)
		exists = false
	}
	server.mu.Unlock()
	if !exists {
		return failure("local service lease rejected")
	}
	if request["caller_role"] != entry.role {
		return failure("local service caller role mismatch")
	}
	if request["session_id"] != entry.sessionID {
		return failure("local service session identity rejected")
	}
	tool, ok := request["tool"].(string)
	if !ok || !slices.Contains(server.capabilities[entry.role], tool) || tool == "host_state" {
		return failure("local service caller role is not allowed for tool")
	}
	arguments, ok := request["arguments"].(map[string]any)
	if !ok || !server.authorizedIdentity(arguments) {
		return failure("local service request identity is not authorized")
	}
	if value, exists := arguments["session_id"]; exists && value != nil && value != "" {
		return failure("local service session claim is not authorized")
	}
	if _, exists := arguments["profile_id"]; exists {
		return failure("local service request identity is not authorized")
	}
	if tool == "health" {
		return server.health()
	}
	if selected != nil && listenerPort != 0 {
		health := server.health()
		if health["readiness"] != "ready" {
			if health["degraded_state"] == "native_memory_unavailable" {
				return map[string]any{"ok": false, "error": "local service native memory unavailable",
					"reason_code": "native_backend_unavailable", "retryable": true}
			}
			return failure("local service listener is not ready")
		}
		if mutationTools[tool] && health["state"] == "ready_read_only" {
			return failure("local service emergency read-only")
		}
	}
	if tool == "remember" || tool == "observe" {
		correlation, exists := request["mutation_correlation_id"].(string)
		if _, provided := request["mutation_correlation_id"]; provided &&
			(!exists || correlation == "" || len(correlation) > 256) {
			return failure("local service mutation correlation rejected")
		}
		if correlation == "" {
			correlation = request["request_id"].(string)
		}
		if len(correlation) > 256 {
			return failure("local service mutation correlation rejected")
		}
		if supplied, present := arguments["operation_id"]; present && supplied != nil && supplied != "" {
			value, ok := supplied.(string)
			if !ok || value != correlation {
				return failure("local service mutation correlation rejected")
			}
		}
		arguments = cloneArguments(arguments)
		arguments["operation_id"] = correlation
	}
	result := server.dispatch(ctx, Caller{Identity: server.identity, Role: entry.role,
		Lineage: entry.lineage, PolicyBundleRevision: entry.policyBundleRevision,
		CatalogRevision: entry.catalogRevision}, tool, arguments)
	if result != nil && result["ok"] == true &&
		(tool == "observe" || tool == "remember" || tool == "amend") {
		server.enqueueIndexRefresh(ctx, result)
	}
	server.mu.Lock()
	panel := server.panel
	server.mu.Unlock()
	if panel != nil && result != nil {
		panel.Observe(tool, arguments, result, entry.lineage)
	}
	return result
}

func (server *Server) enqueueIndexRefresh(ctx context.Context, result map[string]any) {
	memoryID, _ := result["memory_id"].(string)
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return
	}
	server.mu.Lock()
	queue := server.workQueue
	server.mu.Unlock()
	var receipt servicequeue.AdmissionReceipt
	var err error
	if queue == nil {
		err = errors.New("local service background queue unavailable")
	} else {
		receipt, err = queue.Submit(ctx, "indexing", memoryID, memoryID, 3)
	}
	if err != nil {
		server.mu.Lock()
		server.workQueueErr = true
		server.mu.Unlock()
		result["background_work"] = map[string]any{"lane": "indexing",
			"status": "admission_failed", "no_silent_drop": false}
		return
	}
	if receipt.Status == "backpressured" {
		result["background_work"] = map[string]any{"lane": receipt.Lane,
			"work_id": receipt.WorkID, "admission_id": receipt.AdmissionID,
			"status": receipt.Status, "reason": receipt.Reason, "no_silent_drop": true}
	}
}

func cloneArguments(arguments map[string]any) map[string]any {
	copy := make(map[string]any, len(arguments))
	for key, value := range arguments {
		copy[key] = value
	}
	return copy
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		http.Error(writer, "local service requires loopback transport", http.StatusForbidden)
		return
	}
	server.mu.Lock()
	server.activeRequests++
	server.lastWork = server.now()
	server.mu.Unlock()
	defer func() {
		server.mu.Lock()
		server.activeRequests--
		server.mu.Unlock()
	}()
	if request.URL.Path == "/panel" || strings.HasPrefix(request.URL.Path, "/panel/") {
		server.mu.Lock()
		panel, port := server.panel, server.listenerPort
		server.mu.Unlock()
		if panel != nil && port != 0 {
			panel.Handle(writer, request, server, port)
		} else {
			http.NotFound(writer, request)
		}
		return
	}
	if request.Method != http.MethodPost {
		http.NotFound(writer, request)
		return
	}
	if request.ContentLength < 0 || request.ContentLength > MaxMessageBytes {
		server.respond(writer, failure("local service request exceeds protocol limit"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxMessageBytes+1))
	if err != nil || len(body) > MaxMessageBytes {
		server.respond(writer, failure("local service request exceeds protocol limit"))
		return
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		server.respond(writer, failure("local service request is invalid"))
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		server.respond(writer, failure("local service request is invalid"))
		return
	}
	var response map[string]any
	switch request.URL.Path {
	case "/health":
		response = server.health()
	case "/lease":
		response = server.issueLease(payload)
	case "/call":
		response = server.dispatchBounded(request.Context(), func() map[string]any {
			return server.call(request.Context(), payload)
		})
	case "/host-state":
		response = server.dispatchBounded(request.Context(), func() map[string]any {
			return server.submitHostState(request.Context(), payload)
		})
	case "/shutdown":
		response = server.shutdownRequest(payload)
	default:
		response = failure("local service endpoint is unknown")
	}
	server.respond(writer, response)
}

// dispatchBounded keeps no more than four calls running and thirty-two waiting.
// A caller rejected by the queue receives a visible failure receipt.
func (server *Server) dispatchBounded(ctx context.Context, run func() map[string]any) map[string]any {
	server.mu.Lock()
	if server.waitingCalls >= 32 {
		server.failedCalls++
		server.mu.Unlock()
		return failure("local service dispatch queue is full")
	}
	server.waitingCalls++
	server.mu.Unlock()
	select {
	case server.dispatchSlots <- struct{}{}:
		server.mu.Lock()
		server.waitingCalls--
		server.mu.Unlock()
		defer func() { <-server.dispatchSlots }()
	case <-ctx.Done():
		server.mu.Lock()
		server.waitingCalls--
		server.failedCalls++
		server.mu.Unlock()
		return failure("local service request was cancelled")
	}
	result := run()
	server.mu.Lock()
	if result["ok"] == true {
		server.successfulCalls++
	} else {
		server.failedCalls++
	}
	server.mu.Unlock()
	return result
}

// IsIdle fails closed if either durable queue cannot be inspected. A long
// event stream counts as an active request and keeps the panel owner alive.
func (server *Server) IsIdle(ctx context.Context, timeout time.Duration) bool {
	server.mu.Lock()
	idle := !server.draining && server.activeRequests == 0 &&
		server.now().Sub(server.lastWork) >= timeout
	queue, scheduled, panel := server.workQueue, server.scheduler, server.panel
	server.mu.Unlock()
	if !idle || queue == nil || scheduled == nil {
		return false
	}
	if panel != nil {
		panel.expireIfIdle()
	}
	pending, err := scheduled.HasPending(ctx)
	if err != nil || pending {
		return false
	}
	health, err := queue.Health(ctx, server.now())
	if err != nil {
		return false
	}
	for _, lane := range health.Lanes {
		if lane.Pending != 0 || lane.Running != 0 {
			return false
		}
	}
	return true
}

func (server *Server) BeginShutdown() {
	server.mu.Lock()
	if !server.draining {
		server.draining = true
		server.backgroundReady = false
		close(server.shutdown)
	}
	server.mu.Unlock()
}

// SetBackgroundReady records whether the listener owns running queue workers
// and a scheduler loop. Startup calls it only after both have started.
func (server *Server) SetBackgroundReady(ready bool) {
	server.mu.Lock()
	server.backgroundReady = ready && !server.draining
	server.mu.Unlock()
}

func (server *Server) PublishPanelEvent(event ChangeEvent) {
	server.mu.Lock()
	panel := server.panel
	server.mu.Unlock()
	if panel != nil {
		panel.Publish(event)
	}
}

func (server *Server) health() map[string]any {
	server.mu.Lock()
	draining := server.draining
	queue := server.workQueue
	queueFailed := server.workQueueErr
	backgroundReady, listenerPort := server.backgroundReady, server.listenerPort
	policy, scheduled, selected := server.policy, server.scheduler, server.selected
	depth, successful, failed := server.waitingCalls, server.successfulCalls, server.failedCalls
	server.mu.Unlock()
	background := map[string]any{"available": false, "emergency_read_only": true,
		"no_silent_drop": false, "lanes": []servicequeue.LaneHealth{}}
	if queue != nil && !queueFailed {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		status, err := queue.Health(ctx, time.Now())
		cancel()
		if err == nil {
			background = map[string]any{"available": true,
				"emergency_read_only": status.EmergencyReadOnly,
				"no_silent_drop":      status.NoSilentDrop, "lanes": status.Lanes}
		}
		if err != nil {
			server.mu.Lock()
			server.workQueueErr = true
			server.mu.Unlock()
		}
	}
	result := map[string]any{"ok": false, "state": "not_ready", "liveness": "live",
		"readiness": "not_ready", "transport": "loopback_http", "listener_pid": os.Getpid(),
		"workspace_id": server.identity.WorkspaceID, "profile_id": server.identity.ProfileID,
		"backend": server.identity.Backend, "selected_backend": server.identity.Backend,
		"native_health": "not_probed", "native_ok": false, "strict_ok": false,
		"effective_mode": "unavailable", "policy_bundle_revision": nil,
		"catalog_revision": nil, "block_revision": nil, "projection_revision": nil,
		"service_schema_revision": "local-service-schema:2",
		"slo_contract_revision":   nil,
		"queue_capacity":          32, "queue_depth": depth,
		"successful_calls": successful, "failed_calls": failed,
		"dispatcher_alive": true,
		"scheduler_health": "unavailable", "background_queue": background,
		"degraded_state": "go_port_partial", "error": "Go local service dependencies are not ready"}
	if revision, err := operationalslo.ContractRevision(); err == nil {
		result["slo_contract_revision"] = revision
	}
	if background["available"] != true {
		result["background_queue_error"] = "local service background queue unavailable"
	}
	if policy != nil {
		bundle, err := policy.Active()
		if err == nil {
			var snapshot servicepolicy.Snapshot
			snapshot, err = policy.SnapshotFor(bundle.RevisionID)
			if err == nil {
				var blockRevision, projectionRevision string
				blockRevision, err = servicepolicy.BlockRevision(snapshot)
				if err == nil {
					projectionRevision, err = servicepolicy.ProjectionRevision(snapshot)
				}
				if err == nil {
					result["policy_bundle_revision"] = bundle.RevisionID
					result["catalog_revision"] = bundle.CatalogRevisionID
					result["block_revision"] = blockRevision
					result["projection_revision"] = projectionRevision
				}
			}
		}
		if err != nil {
			result["degraded_state"] = "policy_bundle_unavailable"
			result["error"] = "local service policy bundle unavailable"
		}
	}
	if scheduled != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := scheduled.HasPending(ctx)
		cancel()
		if err == nil {
			result["scheduler_health"] = "healthy"
		}
	}
	if selected != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		native, err := selected.Health(ctx)
		cancel()
		if err == nil {
			for _, key := range []string{"schema_revision", "index_revision", "canonical_snapshot_sha256"} {
				if value, exists := native[key]; exists {
					result[key] = value
				}
			}
			if native["native_ok"] == true && native["strict_ok"] == true {
				result["native_health"] = "healthy"
				result["native_ok"] = true
				result["strict_ok"] = true
				result["effective_mode"] = "selected_native"
			} else {
				result["native_health"] = "unavailable"
			}
		} else {
			result["native_health"] = "unavailable"
		}
	}
	if draining {
		result["state"] = "draining"
		result["degraded_state"] = "listener_draining"
		result["error"] = "local service listener is draining"
		return result
	}
	if listenerPort == 0 || !backgroundReady || selected == nil {
		return result
	}
	if result["policy_bundle_revision"] == nil {
		result["degraded_state"] = "policy_bundle_unavailable"
		result["error"] = "local service policy bundle unavailable"
		return result
	}
	if result["native_ok"] != true || result["strict_ok"] != true {
		result["degraded_state"] = "native_memory_unavailable"
		result["error"] = "local service native memory unavailable"
		return result
	}
	if result["scheduler_health"] != "healthy" {
		result["degraded_state"] = "scheduler_unavailable"
		result["error"] = "local service scheduler unavailable"
		return result
	}
	result["ok"] = true
	result["readiness"] = "ready"
	result["error"] = nil
	if background["available"] != true || background["emergency_read_only"] == true {
		result["state"] = "ready_read_only"
		result["degraded_state"] = "background_queue_unavailable"
		if background["available"] == true {
			result["degraded_state"] = "emergency_read_only"
		}
		result["error"] = "local service mutations are disabled until background queue recovery"
		return result
	}
	result["state"] = "ready"
	result["degraded_state"] = nil
	return result
}

func (server *Server) shutdownRequest(request map[string]any) map[string]any {
	if err := frameError(request); err != "" {
		return failure(err)
	}
	if request["caller_role"] != "operator" {
		return failure("local service shutdown requires operator role")
	}
	secret, ok := request["bootstrap_secret"].(string)
	known := server.secrets["operator"]
	if !ok || known == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(known)) != 1 {
		return failure("local service bootstrap authentication rejected")
	}
	server.BeginShutdown()
	return map[string]any{"ok": true, "state": "shutting_down"}
}

func (server *Server) respond(writer http.ResponseWriter, payload map[string]any) {
	if payload == nil {
		payload = failure("local service response rejected")
	}
	serialized, err := json.Marshal(payload)
	if err != nil || len(serialized) > MaxMessageBytes {
		serialized = []byte(`{"ok":false,"error":"local service response exceeds protocol limit"}`)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", strconv.Itoa(len(serialized)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(serialized)
}
