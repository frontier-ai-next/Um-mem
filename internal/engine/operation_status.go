package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/receipts"
)

// OperationStatus joins a Go CLI creation receipt with its current native
// projection state. It never claims or retries the operation.
func (engine *Engine) OperationStatus(ctx context.Context, operationID string) (map[string]any, error) {
	return engine.OperationStatusFor(ctx, operationID, "go_cli", 2)
}

// OperationStatusFor reads a receipt under the authenticated caller identity.
// A role cannot inspect another role's operation receipt by guessing its ID.
func (engine *Engine) OperationStatusFor(ctx context.Context, operationID, callerRole string, protocolVersion int) (map[string]any, error) {
	if operationID == "" || operationID != strings.TrimSpace(operationID) || len(operationID) > 256 {
		return nil, errors.New("operation_status requires a bounded operation_id")
	}
	if callerRole == "" || protocolVersion < 1 {
		return nil, errors.New("operation_status requires authenticated caller identity")
	}
	identity := receipts.Identity{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		CallerRole: callerRole, ProtocolVersion: protocolVersion}
	state, err := receipts.Status(ctx, engine.config.RepoRoot, identity, operationID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return map[string]any{"ok": false, "operation_id": operationID,
			"reason_code": "operation_not_found", "durable_status": "not_found"}, nil
	}
	if state.Status == "pending" {
		return map[string]any{"ok": false, "operation_id": operationID,
			"durable_status": "unknown", "projection_status": "unknown", "retryable": true}, nil
	}
	original := state.Response
	if state.Response["receipt_kind"] == "mutation_response" {
		inner, ok := state.Response["response"].(map[string]any)
		if !ok {
			return nil, errors.New("operation receipt response is invalid")
		}
		original = inner
	}
	result := map[string]any{"ok": original["ok"] == true, "operation_id": operationID,
		"durable_status": state.Status, "original_receipt": original,
		"record_id": "", "commit_revision": int64(0), "projection_status": "unknown",
		"projection_revision": int64(0), "reason_code": "", "attention_required": false,
		"retryable": false}
	if reason, ok := original["reason_code"].(string); ok {
		result["reason_code"] = reason
		if state.Status == "failed" && (reason == "native_transport_failure" ||
			reason == "transport_timeout" || reason == "store_lock_unavailable") {
			result["retryable"] = true
		}
	} else if reason, ok := original["reason"].(string); ok {
		result["reason_code"] = reason
	}
	if candidate, ok := original["result"].(map[string]any); ok {
		if id, ok := candidate["record_id"].(string); ok {
			result["record_id"] = id
		}
	}
	if result["record_id"] == "" {
		for _, field := range []string{"memory_id", "witness_id", "canonical_reference"} {
			if id, ok := original[field].(string); ok && id != "" {
				result["record_id"] = id
				break
			}
		}
	}
	recordID := result["record_id"].(string)
	if recordID == "" {
		return result, nil
	}
	status, revision, err := engine.index.ProjectionStatus(ctx, recordID)
	if err != nil {
		return nil, err
	}
	if status == "" {
		result["attention_required"] = true
		return result, nil
	}
	result["projection_status"] = status
	result["commit_revision"] = revision
	result["attention_required"] = status == "failed"
	result["retryable"] = status == "pending"
	if status == "committed" {
		result["projection_revision"] = revision
	}
	return result, nil
}
