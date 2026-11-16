package erasure

import (
	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func ValidAuthorization(repoRoot, workspaceID string, request memory.MemoryEraseRequest) bool {
	authorization := request.Authorization
	if authorization.RefType != "ledger_event" ||
		(authorization.ProducedBy != "supervisor" && authorization.ProducedBy != "verifier" &&
			authorization.ProducedBy != "watcher" && authorization.ProducedBy != "harness") {
		return false
	}
	resolver, err := ledger.OpenResolver(repoRoot, workspaceID)
	if err != nil {
		return false
	}
	for _, ref := range append([]memory.EvidenceRef{authorization}, request.EvidenceRefs...) {
		if len(resolver.ValidateRef(ref)) != 0 {
			return false
		}
	}
	row := resolver.Row(authorization.RefID)
	if row == nil || row["workspace_id"] != workspaceID || row["actor"] != authorization.ProducedBy ||
		row["tool"] != "authorize_memory_erase" {
		return false
	}
	unsigned := make(map[string]any, len(row)-1)
	for key, value := range row {
		if key != "event_hash" {
			unsigned[key] = value
		}
	}
	eventHash, err := ledger.HashValue(unsigned)
	if err != nil || row["event_hash"] != eventHash {
		return false
	}
	approval, ok := row["result"].(map[string]any)
	if !ok {
		return false
	}
	resultHash, err := ledger.HashValue(approval)
	if err != nil || row["result_hash"] != resultHash {
		return false
	}
	return approval["action"] == "erase_memory" && approval["approved"] == true &&
		approval["request_id"] == request.RequestID &&
		approval["memory_id_sha256"] == memory.EraseSHA256(request.MemoryID) &&
		approval["reason_sha256"] == memory.EraseSHA256(request.Reason)
}
