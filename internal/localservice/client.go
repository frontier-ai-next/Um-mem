package localservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/paths"
)

// Client uses a published loopback listener and a role-specific bootstrap
// credential. Constructing it never launches a listener or creates secrets.
type Client struct {
	identity     Identity
	role         string
	capabilities []string
	secret       string
	endpoint     string
	http         *http.Client
	sequence     atomic.Uint64
}

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxMessageBytes {
		return nil, errors.New("local service runtime file exceeds protocol limit")
	}
	return data, nil
}

func NewClient(identity Identity, role string, timeout time.Duration) (*Client, error) {
	root, err := normalizeRoot(identity.RepoRoot)
	if err != nil {
		return nil, err
	}
	identity.RepoRoot = root
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" || identity.ProfileID == "" || identity.Backend == "" {
		return nil, errors.New("local service client identity is invalid")
	}
	identity.WorkspaceID = workspace
	paths, err := Paths(identity)
	if err != nil {
		return nil, err
	}
	registryRaw, err := readBoundedFile(paths.Registry)
	if err != nil {
		return nil, err
	}
	var registry registryFile
	if err := json.Unmarshal(registryRaw, &registry); err != nil ||
		registry.SchemaVersion != SchemaVersion || registry.APIVersion != APIVersion ||
		registry.ProtocolVersion != ProtocolVersion || registry.Host != "127.0.0.1" ||
		registry.Port < 1 || registry.Port > 65535 || registry.PID <= 0 ||
		registry.WorkspaceID != identity.WorkspaceID || registry.ProfileID != identity.ProfileID ||
		registry.Backend != identity.Backend || registry.Transport != "loopback_http" {
		return nil, errors.New("local service registry identity is invalid")
	}
	bootstrapRaw, err := readBoundedFile(paths.Bootstrap)
	if err != nil {
		return nil, err
	}
	var bootstrap bootstrapFile
	if err := json.Unmarshal(bootstrapRaw, &bootstrap); err != nil {
		return nil, errors.New("local service bootstrap is invalid")
	}
	roles, err := roleNames()
	if err != nil || !validBootstrap(bootstrap, identity, roles) {
		return nil, errors.New("local service bootstrap identity is invalid")
	}
	secret := bootstrap.Secrets[role]
	if secret == "" {
		return nil, errors.New("local service client role is invalid")
	}
	var capabilities map[string][]string
	if err := json.Unmarshal(roleCapabilitiesJSON, &capabilities); err != nil {
		return nil, err
	}
	allowed, ok := capabilities[role]
	if !ok {
		return nil, errors.New("local service client role is invalid")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
		DisableKeepAlives: false, MaxIdleConnsPerHost: 2}
	client := &http.Client{Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("local service redirect refused") }}
	return &Client{identity: identity, role: role, capabilities: append([]string(nil), allowed...),
		secret: secret, endpoint: net.JoinHostPort(registry.Host, strconv.Itoa(registry.Port)), http: client}, nil
}

func (client *Client) Close() {
	if transport, ok := client.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

func (client *Client) requestID() string { return strconv.FormatUint(client.sequence.Add(1), 10) }

func (client *Client) frame() map[string]any {
	return map[string]any{"protocol_version": ProtocolVersion, "schema_version": SchemaVersion,
		"api_version": APIVersion, "request_id": client.requestID()}
}

func (client *Client) post(ctx context.Context, path string, payload map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxMessageBytes {
		return nil, errors.New("local service request exceeds protocol limit")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+client.endpoint+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("local service returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxMessageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxMessageBytes {
		return nil, errors.New("local service response exceeds protocol limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("local service returned invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("local service returned trailing JSON")
	}
	return result, nil
}

func (client *Client) Health(ctx context.Context) (map[string]any, error) {
	result, err := client.post(ctx, "/health", map[string]any{})
	if err != nil {
		return nil, err
	}
	if result["workspace_id"] != client.identity.WorkspaceID ||
		result["profile_id"] != client.identity.ProfileID ||
		result["selected_backend"] != client.identity.Backend ||
		result["transport"] != "loopback_http" {
		return nil, errors.New("local service health identity is invalid")
	}
	return result, nil
}

// Dashboard opens or stops the local browser capability through the operator
// bootstrap boundary. Its URL is checked against the selected listener before
// a CLI may hand it to the user's browser.
func (client *Client) Dashboard(ctx context.Context, stop bool) (map[string]any, error) {
	if client.role != "operator" {
		return nil, errors.New("dashboard control requires the operator role")
	}
	if !stop {
		health, err := client.Health(ctx)
		if err != nil || health["ok"] != true || health["readiness"] != "ready" {
			return health, err
		}
	}
	payload := client.frame()
	payload["caller_role"], payload["bootstrap_secret"] = "operator", client.secret
	payload["action"] = "start"
	if stop {
		payload["action"] = "stop"
	}
	result, err := client.post(ctx, "/panel/connect", payload)
	if err != nil || result["ok"] != true || stop {
		return result, err
	}
	raw, ok := result["url"].(string)
	parsed, parseErr := url.Parse(raw)
	if !ok || parseErr != nil || parsed.Scheme != "http" || parsed.Host != client.endpoint ||
		parsed.Path != "/panel/" || parsed.RawQuery != "" || parsed.Fragment == "" ||
		strings.Contains(parsed.Fragment, "/") {
		return nil, errors.New("dashboard URL escaped the selected loopback listener")
	}
	return result, nil
}

func sessionID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "default"
	}
	if len(value) > 256 {
		return "", errors.New("local service session identity is invalid")
	}
	return value, nil
}

func (client *Client) Lease(ctx context.Context, rawSessionID string, lineage Lineage) (string, map[string]any, error) {
	session, err := sessionID(rawSessionID)
	if err != nil {
		return "", nil, err
	}
	health, err := client.Health(ctx)
	if err != nil {
		return "", nil, err
	}
	if health["ok"] != true || health["liveness"] != "live" || health["readiness"] != "ready" ||
		health["native_health"] != "healthy" {
		return "", health, errors.New("local service listener is not ready")
	}
	if lineage.WorkspaceID != "" && lineage.WorkspaceID != client.identity.WorkspaceID ||
		lineage.SessionID != "" && lineage.SessionID != session {
		return "", nil, errors.New("local service scope lineage is invalid")
	}
	lineage.WorkspaceID, lineage.SessionID = client.identity.WorkspaceID, session
	payload := client.frame()
	payload["caller_role"], payload["session_id"] = client.role, session
	payload["scope_lineage"] = map[string]string{"workspace_id": lineage.WorkspaceID,
		"project_id": lineage.ProjectID, "session_id": lineage.SessionID,
		"task_id": lineage.TaskID, "subtask_id": lineage.SubtaskID, "user_id": lineage.UserID}
	payload["capabilities"], payload["bootstrap_secret"] = client.capabilities, client.secret
	response, err := client.post(ctx, "/lease", payload)
	if err != nil {
		return "", nil, err
	}
	token, ok := response["lease"].(string)
	if response["ok"] != true || !ok || token == "" {
		return "", response, errors.New("local service lease rejected")
	}
	return token, response, nil
}

func (client *Client) SubmitHostState(ctx context.Context, rawSessionID string, event map[string]any) (map[string]any, error) {
	if client.role != "host_state" {
		return nil, errors.New("local service host-state role is required")
	}
	session, err := sessionID(rawSessionID)
	if err != nil {
		return nil, err
	}
	lease, _, err := client.Lease(ctx, session, Lineage{})
	if err != nil {
		return nil, err
	}
	payload := client.frame()
	payload["lease"], payload["caller_role"], payload["session_id"] = lease, client.role, session
	payload["event"] = event
	return client.post(ctx, "/host-state", payload)
}

func (client *Client) Call(ctx context.Context, rawSessionID string, lineage Lineage,
	tool string, arguments map[string]any, correlationID string) (map[string]any, error) {
	if !containsCapability(client.capabilities, tool) || tool == "host_state" {
		return nil, errors.New("local service caller role is not allowed for tool")
	}
	session, err := sessionID(rawSessionID)
	if err != nil {
		return nil, err
	}
	lease, _, err := client.Lease(ctx, session, lineage)
	if err != nil {
		return nil, err
	}
	bound := cloneArguments(arguments)
	bound["repo_root"], bound["workspace_id"], bound["backend"] = client.identity.RepoRoot,
		client.identity.WorkspaceID, client.identity.Backend
	payload := client.frame()
	payload["lease"], payload["caller_role"], payload["session_id"] = lease, client.role, session
	payload["tool"], payload["arguments"] = tool, bound
	if correlationID != "" {
		payload["mutation_correlation_id"] = correlationID
	}
	return client.post(ctx, "/call", payload)
}

func containsCapability(capabilities []string, tool string) bool {
	for _, item := range capabilities {
		if item == tool {
			return true
		}
	}
	return false
}
