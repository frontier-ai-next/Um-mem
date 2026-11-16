package engine

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/erasure"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/native/mempalace"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/telemetry"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/gofrs/flock"
)

func (engine *Engine) acquireEraseWorkspaceLock(ctx context.Context) (*flock.Flock, error) {
	path := filepath.Join(engine.config.RepoRoot, ".UM-MEM", "memory",
		"erase-workspace-"+memory.EraseSHA256(engine.config.WorkspaceID)+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := flock.New(path)
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("erase workspace lock timed out")
	}
	return lock, nil
}

func (engine *Engine) assertChainNotErased(ctx context.Context, canonicalKey string) error {
	if canonicalKey == "" {
		return nil
	}
	if _, err := os.Stat(erasure.JournalPath(engine.config.RepoRoot)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	journal, err := erasure.OpenJournal(engine.config.RepoRoot)
	if err != nil {
		return err
	}
	defer journal.Close()
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return err
	}
	targetSHAs := make([]string, 0)
	for _, row := range rows {
		if row.Record.CanonicalKey == canonicalKey && row.Record.WorkspaceID == engine.config.WorkspaceID &&
			row.Status == "committed" {
			targetSHAs = append(targetSHAs, memory.EraseSHA256(row.Record.BlockID))
		}
	}
	erased, err := journal.HasCompletedTarget(ctx,
		memory.EraseSHA256(engine.config.WorkspaceID), targetSHAs)
	if err != nil {
		return err
	}
	if erased {
		return errors.New("canonical chain has a completed erase certificate")
	}
	return nil
}

func eraseFailure(code string, state *erasure.RequestState) map[string]any {
	result := map[string]any{"ok": false, "error": code}
	if state != nil {
		result["erase_request"] = *state
	}
	return result
}

func (engine *Engine) eraseHead(ctx context.Context, root memory.MemoryRecord) (*memory.MemoryRecord, error) {
	if root.CanonicalKey == "" || root.BlockMembership == nil {
		return nil, errors.New("canonical current head is unavailable")
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	var current *memory.MemoryRecord
	for _, row := range rows {
		if !row.Current || row.Status != "committed" ||
			row.Backend != engine.recordNativeStore(row.Record) ||
			row.Record.CanonicalKey != root.CanonicalKey {
			continue
		}
		if current != nil {
			return nil, errors.New("canonical key has more than one current head")
		}
		copy := row.Record
		current = &copy
	}
	if current == nil {
		return nil, errors.New("canonical current head is unavailable")
	}
	return current, nil
}

func (engine *Engine) eraseOutcome(ctx context.Context, journal *erasure.Journal,
	state erasure.RequestState, status, code string) map[string]any {
	updated, err := journal.RecordOutcome(ctx, state, status, code)
	if err != nil {
		return eraseFailure("erase_journal_unavailable", nil)
	}
	return eraseFailure(code, &updated)
}

func (engine *Engine) eraseProjectionReady(ctx context.Context, recordID string) bool {
	barrier, err := engine.ProjectionBarrier(ctx, recordID, 0, 0)
	return err == nil && barrier["ok"] == true && barrier["projection_status"] == "committed"
}

func (engine *Engine) recordEraseEvent(request memory.MemoryEraseRequest,
	result map[string]any, operationErr error) {
	code, _ := result["error"].(string)
	if operationErr != nil {
		code = "erase_internal_error"
	}
	status := "blocked"
	eventType, decision, outcome := "memory_erase_rejected", "authorization_checked", code
	data := map[string]any{}
	if request.RequestID != "" {
		data["request_id_sha256"] = memory.EraseSHA256(request.RequestID)[:16]
	}
	if request.Reason != "" {
		data["reason_sha256"] = memory.EraseSHA256(request.Reason)[:16]
	}
	if state, ok := result["erase_request"].(erasure.RequestState); ok {
		if state.Status == "unavailable" || state.Status == "purging" {
			status = "unavailable"
		}
	}
	if result["ok"] == true {
		status, eventType, decision, outcome = "ok", "memory_erase_completed", "exact_native_closure", "completed"
		certificate, _ := result["certificate"].(map[string]any)
		data = map[string]any{"certificate_sha256": certificate["certificate_sha256"],
			"native_exact_id_absent": true, "native_edges_absent": true,
			"raw_evidence_retained": true}
	}
	// The erase journal and native verification determine the result. A
	// telemetry write failure cannot turn a completed deletion into a retry.
	if eventErr := telemetry.Record(engine.config.RepoRoot, telemetry.Event{EventType: eventType,
		WorkspaceID: engine.config.WorkspaceID, Backend: engine.config.BackendID,
		Status: status, Error: code, Operation: "governed_erase", Decision: decision,
		Outcome: outcome, Data: data, SelectedIDs: []string{request.MemoryID}}); eventErr != nil {
		log.Printf("UM-MEM erase telemetry unavailable: %v", eventErr)
	}
}

// Erase performs the authorized, resumable MemPalace privacy operation. It
// preserves raw canonical evidence in a retracted hidden successor and removes
// every native artifact for its canonical revision chain. Other backends fail
// closed as unsupported.
func (engine *Engine) Erase(ctx context.Context, request memory.MemoryEraseRequest) (result map[string]any, err error) {
	recordEvent := true
	defer func() {
		if recordEvent {
			engine.recordEraseEvent(request, result, err)
		}
	}()
	workspaceID := engine.config.WorkspaceID
	if strings.TrimSpace(request.RequestID) == "" || strings.TrimSpace(request.MemoryID) == "" ||
		strings.TrimSpace(request.Reason) == "" || request.WorkspaceID != workspaceID {
		return eraseFailure("invalid_erase_request", nil), nil
	}
	if !erasure.ValidAuthorization(engine.config.RepoRoot, workspaceID, request) {
		return eraseFailure("invalid_erase_authorization", nil), nil
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return eraseFailure("erase_workspace_unavailable", nil), nil
	}
	defer lock.Unlock()
	journal, err := erasure.OpenJournal(engine.config.RepoRoot)
	if err != nil {
		return eraseFailure("erase_journal_unavailable", nil), nil
	}
	defer journal.Close()
	backend := engine.config.BackendID
	state, created, err := journal.OpenRequest(ctx, memory.EraseSHA256(workspaceID),
		memory.EraseSHA256(request.RequestID), memory.EraseFingerprint(request, workspaceID, backend),
		memory.EraseSHA256(request.MemoryID), backend)
	if errors.Is(err, erasure.ErrRequestConflict) {
		return eraseFailure("erase_request_conflict", nil), nil
	}
	if err != nil {
		return eraseFailure("erase_journal_unavailable", nil), nil
	}
	completed := !created && state.Status == "completed"
	if !created && !completed && state.Status != "requested" && state.Status != "purging" {
		return eraseFailure(state.Error, &state), nil
	}
	resumingPurge := state.Status == "purging"
	requested, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return nil, err
	}
	if requested == nil {
		return engine.eraseOutcome(ctx, journal, state, "blocked", "not_found"), nil
	}
	if requested.WorkspaceID != workspaceID {
		return engine.eraseOutcome(ctx, journal, state, "blocked", "workspace_mismatch"), nil
	}
	current, err := engine.eraseHead(ctx, *requested)
	if err != nil {
		return eraseFailure("canonical_head_unavailable", &state), nil
	}
	if completed {
		if current.Status != "retracted" || current.Retention != "archive" || current.DeliveryPolicy != "hidden" {
			return eraseFailure("completed_erase_head_invalid", &state), nil
		}
		recordEvent = false
		return map[string]any{"ok": true, "block_id": current.BlockID,
			"erase_request": state, "certificate": erasure.CompletedCertificate(state).Mapping()}, nil
	}
	if backend != "mempalace" {
		return engine.eraseOutcome(ctx, journal, state, "unavailable", "native_purge_unsupported"), nil
	}
	state, err = journal.RecordOutcome(ctx, state, "purging", "")
	if err != nil {
		return eraseFailure("erase_journal_unavailable", nil), nil
	}
	originalContent := current.Content
	retained := current
	if current.Status != "retracted" || current.Retention != "archive" || current.DeliveryPolicy != "hidden" {
		refs := append([]memory.EvidenceRef{request.Authorization}, request.EvidenceRefs...)
		transition, transitionErr := engine.transitionInactiveLocked(ctx, ForgetRequest{MemoryID: current.BlockID,
			Reason: "governed erase authorized", EvidenceRefs: refs}, "retract")
		if transitionErr != nil || transition["ok"] != true {
			return engine.eraseOutcome(ctx, journal, state, "purging", "retraction_failed"), nil
		}
		retainedID, _ := transition["block_id"].(string)
		retained, err = engine.ReadStored(ctx, retainedID)
		if err != nil {
			return engine.eraseOutcome(ctx, journal, state, "purging", "raw_evidence_not_retained"), nil
		}
	}
	if retained == nil || retained.WorkspaceID != workspaceID || retained.Status != "retracted" ||
		retained.Retention != "archive" || retained.DeliveryPolicy != "hidden" ||
		retained.Content != originalContent {
		return engine.eraseOutcome(ctx, journal, state, "purging", "raw_evidence_not_retained"), nil
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return engine.eraseOutcome(ctx, journal, state, "purging", "canonical_history_unavailable"), nil
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Status != "committed" || row.Record.CanonicalKey != requested.CanonicalKey ||
			row.Record.WorkspaceID != workspaceID || row.Backend != engine.recordNativeStore(row.Record) {
			continue
		}
		target := row.Record
		if seen[target.BlockID] {
			continue
		}
		seen[target.BlockID] = true
		if !engine.eraseProjectionReady(ctx, target.BlockID) {
			return engine.eraseOutcome(ctx, journal, state, "purging", "native_purge_unavailable"), nil
		}
		purged, purgeErr := mempalace.PurgeExactNative(ctx, engine.config.RepoRoot, target, resumingPurge)
		if purgeErr != nil {
			return engine.eraseOutcome(ctx, journal, state, "purging", "native_purge_unavailable"), nil
		}
		if !purged.OK || !purged.NativeExactIDAbsent || !purged.NativeEdgesAbsent {
			code := purged.Error
			switch code {
			case "native_exact_id_not_found", "native_exact_id_not_deleted", "native_record_workspace_mismatch":
			default:
				code = "native_purge_unverified"
			}
			return engine.eraseOutcome(ctx, journal, state, "purging", code), nil
		}
	}
	if !seen[requested.BlockID] || !seen[retained.BlockID] {
		return engine.eraseOutcome(ctx, journal, state, "purging", "canonical_history_unavailable"), nil
	}
	state, err = journal.RecordOutcome(ctx, state, "completed", "")
	if err != nil {
		return eraseFailure("erase_journal_unavailable", nil), nil
	}
	return map[string]any{"ok": true, "block_id": retained.BlockID,
		"erase_request": state, "certificate": erasure.CompletedCertificate(state).Mapping()}, nil
}
