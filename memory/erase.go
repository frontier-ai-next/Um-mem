package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

type MemoryEraseRequest struct {
	RequestID     string        `json:"request_id"`
	MemoryID      string        `json:"memory_id"`
	WorkspaceID   string        `json:"workspace_id"`
	Reason        string        `json:"reason"`
	Authorization EvidenceRef   `json:"authorization"`
	EvidenceRefs  []EvidenceRef `json:"evidence_refs"`
}

type MemoryEraseCertificate struct {
	CertificateSHA256   string `json:"certificate_sha256"`
	RequestIDSHA256     string `json:"request_id_sha256"`
	TargetIDSHA256      string `json:"target_id_sha256"`
	Backend             string `json:"backend"`
	IssuedAt            string `json:"issued_at"`
	NativeExactIDAbsent bool   `json:"native_exact_id_absent"`
	NativeEdgesAbsent   bool   `json:"native_edges_absent"`
	RawEvidenceRetained bool   `json:"raw_evidence_retained"`
	SidecarStatus       string `json:"sidecar_status"`
}

func EraseSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// EraseFingerprint exactly matches the Python request identity. The journal
// stores only hashes; the reason and ledger event identity never enter it.
func EraseFingerprint(request MemoryEraseRequest, workspaceID, backend string) string {
	references := append([]EvidenceRef{request.Authorization}, request.EvidenceRefs...)
	fingerprints := make([]string, 0, len(references))
	for _, ref := range references {
		hash := ""
		if ref.Hash != nil {
			hash = *ref.Hash
		}
		fingerprints = append(fingerprints, strings.Join([]string{ref.RefType,
			EraseSHA256(ref.RefID), hash, ref.ProducedBy}, "|"))
	}
	sort.Strings(fingerprints)
	lines := append([]string{EraseSHA256(workspaceID), EraseSHA256(request.MemoryID),
		EraseSHA256(request.Reason), backend}, fingerprints...)
	return EraseSHA256(strings.Join(lines, "\n"))
}

func (certificate MemoryEraseCertificate) Mapping() map[string]any {
	return map[string]any{"certificate_sha256": certificate.CertificateSHA256,
		"request_id_sha256": certificate.RequestIDSHA256,
		"target_id_sha256":  certificate.TargetIDSHA256,
		"backend":           certificate.Backend, "issued_at": certificate.IssuedAt,
		"closure": map[string]any{"native_exact_id_absent": certificate.NativeExactIDAbsent,
			"native_edges_absent":   certificate.NativeEdgesAbsent,
			"raw_evidence_retained": certificate.RawEvidenceRetained,
			"sidecar_status":        certificate.SidecarStatus}}
}
