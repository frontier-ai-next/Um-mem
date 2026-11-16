package erasure

import (
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func CompletedCertificate(state RequestState) memory.MemoryEraseCertificate {
	payload := strings.Join([]string{state.FingerprintSHA256, state.RequestIDSHA256,
		state.TargetIDSHA256, state.Backend, state.UpdatedAt,
		"native_exact_id_absent", "native_edges_absent", "raw_evidence_retained", "retracted"}, "\n")
	return memory.MemoryEraseCertificate{CertificateSHA256: memory.EraseSHA256(payload),
		RequestIDSHA256: state.RequestIDSHA256, TargetIDSHA256: state.TargetIDSHA256,
		Backend: state.Backend, IssuedAt: state.UpdatedAt, NativeExactIDAbsent: true,
		NativeEdgesAbsent: true, RawEvidenceRetained: true, SidecarStatus: "retracted"}
}
