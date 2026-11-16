package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func TestServiceRunPublishesAndClearsRegistry(t *testing.T) {
	root := t.TempDir()
	identity := localservice.Identity{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}
	paths, err := localservice.Paths(identity)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--repo-root", root, "--workspace", "alpha",
			"--profile", "team", "--backend", "mnemosyne"})
	}()
	deadline := time.Now().Add(10 * time.Second)
	var port int
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(paths.Registry)
		if err == nil {
			var registry struct {
				Port int `json:"port"`
			}
			if json.Unmarshal(data, &registry) == nil && registry.Port > 0 {
				port = registry.Port
				break
			}
		}
		select {
		case err := <-done:
			t.Fatalf("service exited before registry publication: %v", err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if port == 0 {
		t.Fatal("service did not publish a listener registry")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	var health map[string]any
	readyDeadline := time.Now().Add(10 * time.Second)
	for {
		response, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/health", port),
			"application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if health["state"] == "ready" {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("live service did not become ready: %+v", health)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if health["ok"] != true || health["readiness"] != "ready" || health["liveness"] != "live" {
		t.Fatalf("live service misreported readiness: %+v", health)
	}
	if health["policy_bundle_revision"] == nil || health["catalog_revision"] == nil ||
		health["block_revision"] == nil || health["projection_revision"] == nil ||
		health["scheduler_health"] != "healthy" || health["native_health"] == "not_probed" {
		t.Fatalf("service omitted Go policy/native diagnostics: %+v", health)
	}
	queueHealth, ok := health["background_queue"].(map[string]any)
	if !ok || queueHealth["available"] != true || queueHealth["no_silent_drop"] != true {
		t.Fatalf("service did not report durable queue health: %+v", health)
	}
	workPath, err := localservice.WorkQueuePath(identity)
	if err != nil {
		t.Fatal(err)
	}
	work, err := servicequeue.Open(workPath, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	queued, err := work.Submit(context.Background(), "policy_shadow_qualification",
		"test-policy-bundle", "revision-1", 3)
	if err != nil || queued.Status != "queued" {
		t.Fatalf("service work admission failed: %+v, %v", queued, err)
	}
	workDeadline := time.Now().Add(5 * time.Second)
	for {
		item, err := work.Item(context.Background(), queued.WorkID)
		if err != nil {
			t.Fatal(err)
		}
		if item.State == "completed" {
			break
		}
		if item.State == "poisoned" || time.Now().After(workDeadline) {
			t.Fatalf("service did not process policy queue work: %+v", item)
		}
		time.Sleep(20 * time.Millisecond)
	}
	schedulerPath, identityDigest, err := localservice.SchedulerPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := scheduler.Open(schedulerPath, identityDigest, "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduled.Close()
	event := scheduler.HostStateEvent{EventID: "codex-host-state:service-test",
		EventType: "prospective_trigger", WorkspaceID: "alpha",
		RunID: "codex-session:service-test", StageID: "codex-turn:service-test",
		StateRevision: "bundle:test", OccurredAt: time.Now().Add(-time.Minute),
		SignalCodes:     []string{"schema:1", "codex_event:UserPromptSubmit"},
		CapabilityCodes: []string{"codex_hook", "typed_host_state"}}
	submission, err := scheduled.Submit(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range submission.Jobs {
		deadline := time.Now().Add(5 * time.Second)
		for {
			receipt, err := scheduled.ReceiptFor(context.Background(), job.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if receipt != nil {
				if receipt.Executed {
					t.Fatal("advisory scheduler claimed host execution")
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("service did not resolve scheduler job %s", job.JobID)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if _, err := os.Stat(paths.Bootstrap); err != nil {
		t.Fatalf("service did not persist role credentials: %v", err)
	}
	bootstrapData, err := os.ReadFile(paths.Bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	var bootstrap struct {
		Secrets map[string]string `json:"secrets"`
	}
	if err := json.Unmarshal(bootstrapData, &bootstrap); err != nil {
		t.Fatal(err)
	}
	post := func(path string, payload map[string]any) map[string]any {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/%s", port, path),
			"application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	frame := func(requestID string) map[string]any {
		return map[string]any{"protocol_version": 1, "schema_version": 2,
			"api_version": 2, "request_id": requestID}
	}
	leaseRequest := frame("lease-1")
	leaseRequest["caller_role"], leaseRequest["session_id"] = "host_hook", "session-1"
	leaseRequest["bootstrap_secret"] = bootstrap.Secrets["host_hook"]
	leaseRequest["capabilities"] = []string{"build_context", "health", "observe",
		"operation_status", "remember", "resolve_active_mask"}
	issued := post("lease", leaseRequest)
	token, ok := issued["lease"].(string)
	if issued["ok"] != true || !ok || token == "" {
		t.Fatalf("live service failed to issue host lease: %+v", issued)
	}
	call := frame("call-1")
	call["caller_role"], call["session_id"], call["lease"] = "host_hook", "session-1", token
	call["mutation_correlation_id"], call["tool"] = "service-write-1", "remember"
	call["arguments"] = map[string]any{"content": "Live listener candidate",
		"requested_block_id": "stable_project_facts", "operation_id": "service-write-1"}
	created := post("call", call)
	id, ok := created["memory_id"].(string)
	if created["ok"] != true || !ok || id == "" || created["projection_status"] != "committed" {
		t.Fatalf("live service could not commit candidate: %+v", created)
	}
	if _, exists := created["background_work"]; exists {
		t.Fatalf("accepted index refresh reported a failure: %+v", created)
	}
	indexWork, err := work.Submit(context.Background(), "indexing", id, id, 3)
	if err != nil || indexWork.Status != "replayed" {
		t.Fatalf("committed write did not enqueue index refresh: %+v, %v", indexWork, err)
	}
	indexDeadline := time.Now().Add(5 * time.Second)
	for {
		item, err := work.Item(context.Background(), indexWork.WorkID)
		if err != nil {
			t.Fatal(err)
		}
		if item.State == "completed" {
			break
		}
		if item.State == "poisoned" || time.Now().After(indexDeadline) {
			t.Fatalf("service did not finish index refresh: %+v", item)
		}
		time.Sleep(20 * time.Millisecond)
	}
	call["tool"] = "operation_status"
	call["arguments"] = map[string]any{"operation_id": "service-write-1"}
	observed := post("call", call)
	if observed["ok"] != true || observed["record_id"] != id {
		t.Fatalf("live service could not read its receipt: %+v", observed)
	}
	shutdown := frame("shutdown-1")
	shutdown["caller_role"], shutdown["bootstrap_secret"] = "host_hook", bootstrap.Secrets["host_hook"]
	if denied := post("shutdown", shutdown); denied["ok"] != false {
		t.Fatalf("host hook shut down the listener: %+v", denied)
	}
	shutdown["caller_role"], shutdown["bootstrap_secret"] = "operator", bootstrap.Secrets["host_hook"]
	if denied := post("shutdown", shutdown); denied["ok"] != false {
		t.Fatalf("wrong operator secret shut down the listener: %+v", denied)
	}
	shutdown["bootstrap_secret"] = bootstrap.Secrets["operator"]
	if stopped := post("shutdown", shutdown); stopped["ok"] != true || stopped["state"] != "shutting_down" {
		t.Fatalf("operator shutdown failed: %+v", stopped)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}
	if _, err := os.Stat(paths.Registry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("service left a stale listener registry: %v", err)
	}
}
