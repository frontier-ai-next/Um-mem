package erasure

import (
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/ledger"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestEraseAuthorizationBindsRequestTargetAndReason(t *testing.T) {
	repo := t.TempDir()
	request := memory.MemoryEraseRequest{RequestID: "erase-1", MemoryID: "mem-private",
		WorkspaceID: "alpha", Reason: "user privacy request"}
	if ValidAuthorization(repo, "alpha", request) {
		t.Fatal("unapproved erase was authorized")
	}
	approval, err := ledger.Append(repo, "alpha", "supervisor", "privacy", "authorize_memory_erase",
		map[string]any{"action": "erase_memory", "approved": true,
			"request_id": request.RequestID, "memory_id_sha256": memory.EraseSHA256(request.MemoryID),
			"reason_sha256": memory.EraseSHA256(request.Reason)})
	if err != nil {
		t.Fatal(err)
	}
	request.Authorization = memory.EvidenceRef{RefType: "ledger_event", RefID: approval.ID,
		Hash: &approval.Hash, ProducedBy: "supervisor", ProducerID: "supervisor:" + approval.ID}
	if !ValidAuthorization(repo, "alpha", request) {
		t.Fatal("valid supervisor approval was rejected")
	}
	changed := request
	changed.Reason = "different reason"
	if ValidAuthorization(repo, "alpha", changed) {
		t.Fatal("authorization was reused for a different reason")
	}
	changed = request
	changed.MemoryID = "different-memory"
	if ValidAuthorization(repo, "alpha", changed) {
		t.Fatal("authorization was reused for a different memory")
	}
	changed = request
	changed.Authorization.ProducedBy = "agent"
	if ValidAuthorization(repo, "alpha", changed) {
		t.Fatal("agent forged an erase authorization")
	}
}
