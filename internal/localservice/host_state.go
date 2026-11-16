package localservice

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/scheduler"
)

func (server *Server) submitHostState(ctx context.Context, request map[string]any) map[string]any {
	ready := server.health()["readiness"] == "ready"
	return server.submitHostStateAtReadiness(ctx, request, ready)
}

// submitHostStateAtReadiness keeps the current listener's fail-closed
// readiness gate while allowing protocol tests to exercise an accepted event.
func (server *Server) submitHostStateAtReadiness(ctx context.Context, request map[string]any, ready bool) map[string]any {
	if err := frameError(request); err != "" {
		return failure(err)
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
	draining := server.draining
	queue := server.scheduler
	server.mu.Unlock()
	if !exists {
		return failure("local service lease rejected")
	}
	if entry.role != "host_state" {
		return failure("local service host-state role is required")
	}
	if request["caller_role"] != entry.role {
		return failure("local service caller role mismatch")
	}
	if request["session_id"] != entry.sessionID {
		return failure("local service session identity rejected")
	}
	if draining {
		return failure("local service listener is draining")
	}
	if queue == nil {
		return failure("local service scheduler unavailable")
	}
	if !ready {
		return failure("local service listener is not ready")
	}
	if entry.policyBundleRevision == "" || entry.catalogRevision == "" {
		return failure("local service policy bundle unavailable")
	}
	rawEvent, ok := request["event"].(map[string]any)
	if !ok {
		return failure("local service host state rejected")
	}
	event, err := scheduler.EventFromRequest(rawEvent, server.identity.WorkspaceID,
		entry.policyBundleRevision, server.now())
	if err != nil {
		return failure("local service host state rejected")
	}
	submission, err := queue.Submit(ctx, event)
	if err != nil {
		return failure("local service host state rejected")
	}
	jobs := make([]map[string]any, 0, len(submission.Jobs))
	for _, job := range submission.Jobs {
		jobs = append(jobs, map[string]any{"job_id": job.JobID,
			"kind": job.Kind, "status": job.Status, "reason_code": job.ReasonCode})
	}
	return map[string]any{"ok": true, "replayed": submission.Replayed,
		"event_id_sha256":             submission.EventIDSHA256,
		"event_fingerprint_sha256":    submission.EventFingerprintSHA256,
		"policy_bundle_revision":      entry.policyBundleRevision,
		"catalog_revision":            entry.catalogRevision,
		"decision_revision":           submission.Plan.DecisionRevision,
		"scheduler_plan_sha256":       submission.PlanSHA256,
		"matched_rule_sha256s":        append([]string{}, submission.Plan.MatchedRuleSHA256s...),
		"matched_action_sha256s":      append([]string{}, submission.Plan.MatchedActionSHA256s...),
		"deferred_trigger_owner_refs": append([]string{}, submission.Plan.DeferredTriggerOwnerRefs...),
		"jobs":                        jobs}
}
