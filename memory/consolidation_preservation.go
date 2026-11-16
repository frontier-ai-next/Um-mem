package memory

import (
	"errors"
	"strings"
)

// PreservationManifest records the transitive raw sources and exact values
// that a working fold or durable consolidation must keep traceable.
type PreservationManifest struct {
	RawSourceIDs      []string      `json:"raw_source_ids"`
	ProtectedLiterals []string      `json:"protected_literals"`
	CorrectionIDs     []string      `json:"correction_ids"`
	EvidenceRefs      []EvidenceRef `json:"evidence_refs"`
}

func NewPreservationManifest(rawSourceIDs, protectedLiterals, correctionIDs []string,
	evidenceRefs []EvidenceRef) (PreservationManifest, error) {
	result := PreservationManifest{}
	var err error
	result.RawSourceIDs, err = sortedSignalIDs(rawSourceIDs, true)
	if err != nil {
		return result, errors.New("transitive raw source IDs are required and must be unique")
	}
	result.ProtectedLiterals, err = sortedSignalIDs(protectedLiterals, false)
	if err != nil {
		return result, err
	}
	result.CorrectionIDs, err = sortedSignalIDs(correctionIDs, false)
	if err != nil {
		return result, err
	}
	result.EvidenceRefs, err = uniqueEvidenceRefs(evidenceRefs, true)
	if err != nil {
		return result, err
	}
	return result, nil
}

type ConsolidationSourceEnvelope struct {
	EnvelopeID             string               `json:"envelope_id"`
	RecordKind             string               `json:"record_kind"`
	Lifetime               string               `json:"lifetime"`
	DirectSourceIDs        []string             `json:"direct_source_ids"`
	TransitiveRawSourceIDs []string             `json:"transitive_raw_source_ids"`
	Preservation           PreservationManifest `json:"preservation"`
}

func BuildConsolidationSourceEnvelope(envelopeID, recordKind, lifetime string,
	directSourceIDs []string, preservation PreservationManifest) (ConsolidationSourceEnvelope, error) {
	result := ConsolidationSourceEnvelope{}
	envelopeID = strings.TrimSpace(envelopeID)
	if envelopeID == "" {
		return result, errors.New("envelope_id is required")
	}
	if recordKind != "working_fold" && recordKind != "durable_consolidation" {
		return result, errors.New("consolidation source record kind is invalid")
	}
	if lifetime != "task" && lifetime != "session" && lifetime != "durable" {
		return result, errors.New("consolidation source lifetime is invalid")
	}
	if recordKind == "working_fold" && lifetime == "durable" {
		return result, errors.New("working fold cannot claim durable lifetime")
	}
	if recordKind == "durable_consolidation" && lifetime != "durable" {
		return result, errors.New("durable consolidation requires durable lifetime")
	}
	ids, err := sortedSignalIDs(directSourceIDs, true)
	if err != nil {
		return result, err
	}
	manifest, err := NewPreservationManifest(preservation.RawSourceIDs,
		preservation.ProtectedLiterals, preservation.CorrectionIDs, preservation.EvidenceRefs)
	if err != nil {
		return result, err
	}
	return ConsolidationSourceEnvelope{EnvelopeID: envelopeID, RecordKind: recordKind,
		Lifetime: lifetime, DirectSourceIDs: ids,
		TransitiveRawSourceIDs: append([]string{}, manifest.RawSourceIDs...),
		Preservation:           manifest}, nil
}
