package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type losslessProof struct {
	Metadata   map[string]any
	Segments   []any
	Witnesses  map[string]memory.MemoryRecord
	Registered bool
}

func newLosslessSourceID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func rawSourceSegments(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []map[string]any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = item
		}
		return result, true
	default:
		return nil, false
	}
}

func segmentString(segment map[string]any, key string) (string, error) {
	value, exists := segment[key]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", errors.New("lossless_atomic source segment text fields must be strings")
	}
	return text, nil
}

func sourceJoin(values []any, key string) string {
	seen := make(map[string]bool)
	result := make([]string, 0)
	for _, raw := range values {
		value := raw.(map[string]any)[key].(string)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return strings.Join(result, "; ")
}

// prepareLossless validates the exact ordered source text and, for bound
// segments, every byte slice against an immutable same-scope host witness.
func (engine *Engine) prepareLossless(ctx context.Context, request CandidateRequest) (losslessProof, error) {
	if !utf8.ValidString(request.Content) {
		return losslessProof{}, errors.New("lossless_atomic content must be valid UTF-8")
	}
	proof := losslessProof{Metadata: make(map[string]any, len(request.Metadata)+10),
		Witnesses: make(map[string]memory.MemoryRecord)}
	for key, value := range request.Metadata {
		if key != "source_segments" {
			proof.Metadata[key] = value
		}
	}
	segments, supplied := rawSourceSegments(request.Metadata["source_segments"])
	if !supplied && request.Metadata["source_segments"] != nil {
		return proof, errors.New("lossless_atomic source_segments must be a non-empty sequence")
	}
	if !supplied {
		id := request.SourceID
		if id == "" {
			id = request.OperationID
			if id == "" {
				var err error
				id, err = newLosslessSourceID()
				if err != nil {
					return proof, err
				}
			}
			id = "record:" + id
		}
		occurredAt, err := segmentString(request.Metadata, "occurred_at")
		if err != nil {
			return proof, err
		}
		speaker, err := segmentString(request.Metadata, "speaker")
		if err != nil {
			return proof, err
		}
		segments = []any{map[string]any{"source_id": id, "text": request.Content,
			"speaker": speaker, "occurred_at": occurredAt}}
	}
	if len(segments) == 0 {
		return proof, errors.New("lossless_atomic source_segments must be a non-empty sequence")
	}
	registeredRanges := make(map[string][]sourceInterval)
	registeredSizes := make(map[string]int)
	seenUnbound := make(map[string]bool)
	var fullText strings.Builder
	boundCount, coveredBytes := 0, 0
	for ordinal, raw := range segments {
		segment, ok := raw.(map[string]any)
		if !ok {
			return proof, errors.New("lossless_atomic source_segments must contain mappings")
		}
		for key := range segment {
			switch key {
			case "source_id", "text", "sha256", "speaker", "occurred_at", "source_sha256", "byte_start", "byte_end":
			default:
				return proof, fmt.Errorf("lossless_atomic source segment has unsupported field: %s", key)
			}
		}
		text, err := segmentString(segment, "text")
		if err != nil || text == "" || !utf8.ValidString(text) {
			return proof, errors.New("lossless_atomic source segment text is required")
		}
		id, err := segmentString(segment, "source_id")
		if err != nil {
			return proof, err
		}
		if id == "" {
			id = request.SourceID
		}
		if id == "" {
			fallback := request.OperationID
			if fallback == "" {
				fallback, err = newLosslessSourceID()
				if err != nil {
					return proof, err
				}
			}
			id = fmt.Sprintf("record:%s:%d", fallback, ordinal)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return proof, errors.New("lossless_atomic source segment source_id is required")
		}
		sha, err := segmentString(segment, "sha256")
		if err != nil {
			return proof, err
		}
		expectedHash := sha256Text(text)
		if sha != "" && strings.ToLower(strings.TrimSpace(sha)) != expectedHash {
			return proof, errors.New("lossless_atomic source segment sha256 does not match text")
		}
		speaker, err := segmentString(segment, "speaker")
		if err != nil {
			return proof, err
		}
		occurredAt, err := segmentString(segment, "occurred_at")
		if err != nil {
			return proof, err
		}
		normalized := map[string]any{"source_id": id, "text": text,
			"sha256": expectedHash, "speaker": speaker, "occurred_at": occurredAt}
		_, hashPresent := segment["source_sha256"]
		_, startPresent := segment["byte_start"]
		_, endPresent := segment["byte_end"]
		if hashPresent || startPresent || endPresent {
			if !hashPresent || !startPresent || !endPresent {
				return proof, errors.New("source segment binding requires hash and byte offsets")
			}
			boundCount++
			start, startOK := exactByteOffset(segment["byte_start"])
			end, endOK := exactByteOffset(segment["byte_end"])
			if !startOK || !endOK || start >= end {
				return proof, errors.New("source segment byte range is invalid")
			}
			canonicalKey, err := memory.DeriveCanonicalKey(engine.config.WorkspaceID,
				engine.config.ProfileID, "raw_host_witness", "host:"+id+":raw_host_witness")
			if err != nil {
				return proof, err
			}
			witness, err := engine.ReadStored(ctx, memory.BlockIDForCanonicalKey(canonicalKey))
			if err != nil {
				return proof, err
			}
			if witness == nil || witness.Metadata["record_kind"] != "raw_host_witness" ||
				witness.Metadata["host_event_id"] != id || witness.Metadata["host_event_kind"] != "conversation_turn" ||
				witness.SessionID != request.SessionID || witness.ProjectID != request.ProjectID {
				return proof, errors.New("source segment registration is unavailable in this scope")
			}
			if err := engine.requireCurrentRawWitness(ctx, *witness); err != nil {
				return proof, err
			}
			sourceHash := sha256Text(witness.Content)
			if segment["source_sha256"] != sourceHash {
				return proof, errors.New("source segment source_sha256 does not match registration")
			}
			if end > len([]byte(witness.Content)) || witness.Content[start:end] != text {
				return proof, errors.New("source segment byte range does not match registration")
			}
			registeredSpeaker, _ := witness.Metadata["source_speaker"].(string)
			registeredTime, _ := witness.Metadata["source_occurred_at"].(string)
			if (speaker != "" && speaker != registeredSpeaker) || (occurredAt != "" && occurredAt != registeredTime) {
				return proof, errors.New("source segment provenance does not match registration")
			}
			normalized["source_sha256"] = sourceHash
			normalized["byte_start"], normalized["byte_end"] = start, end
			normalized["witness_id"] = witness.BlockID
			normalized["speaker"], normalized["occurred_at"] = registeredSpeaker, registeredTime
			registeredSizes[id] = len([]byte(witness.Content))
			proof.Witnesses[id] = *witness
			registeredRanges[id] = append(registeredRanges[id], sourceInterval{start, end})
		} else {
			if seenUnbound[id] {
				return proof, errors.New("lossless_atomic source segment IDs must be unique within a record")
			}
			seenUnbound[id] = true
		}
		proof.Segments = append(proof.Segments, normalized)
		fullText.WriteString(text)
		coveredBytes += len([]byte(text))
	}
	if fullText.String() != request.Content {
		return proof, errors.New("lossless_atomic content must be the exact concatenation of source_segments")
	}
	if boundCount != 0 && boundCount != len(proof.Segments) {
		return proof, errors.New("source segment bindings cannot mix with unregistered segments")
	}
	proof.Registered = boundCount > 0
	proof.Metadata["memory_mode"] = "lossless_atomic"
	proof.Metadata["summary_count"] = 0
	proof.Metadata["source_segments"] = proof.Segments
	proof.Metadata["source_dates"] = sourceJoin(proof.Segments, "occurred_at")
	proof.Metadata["source_speakers"] = sourceJoin(proof.Segments, "speaker")
	proof.Metadata["stored_bytes"] = coveredBytes
	proof.Metadata["exact_segment_fidelity"] = 1.0
	if proof.Registered {
		registeredBytes, unionBytes := 0, 0
		for id, intervals := range registeredRanges {
			registeredBytes += registeredSizes[id]
			unionBytes += coveredBytesForSource(intervals)
		}
		proof.Metadata["source_bytes"] = registeredBytes
		proof.Metadata["exact_source_coverage"] = float64(unionBytes) / float64(registeredBytes)
		proof.Metadata["source_coverage_status"] = "registered"
	} else {
		proof.Metadata["source_bytes"] = len([]byte(request.Content))
		proof.Metadata["exact_source_coverage"] = float64(0)
		proof.Metadata["source_coverage_status"] = "unregistered"
	}
	return proof, nil
}

func coveredBytesForSource(intervals []sourceInterval) int { return coveredBytes(intervals) }

func sameLosslessSegments(left, right any) bool {
	leftJSON, leftErr := jsoncompat.MarshalPython(left, false)
	rightJSON, rightErr := jsoncompat.MarshalPython(right, false)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// A pending operation receipt can outlive both canonical publication and a
// later source retraction. Recover the committed result from its operation
// identity before trying to validate the source for a new publication.
func (engine *Engine) recoverCommittedLosslessOperation(ctx context.Context,
	request CandidateRequest) (CandidateResult, bool, error) {
	digest := sha256.Sum256([]byte(request.OperationID))
	key, err := memory.DeriveCanonicalKey(engine.config.WorkspaceID, engine.config.ProfileID,
		defaultValue(request.Kind, "observation"), "operation_"+hex.EncodeToString(digest[:]))
	if err != nil {
		return CandidateResult{}, false, err
	}
	prior, err := engine.ReadStored(ctx, memory.BlockIDForCanonicalKey(key))
	if err != nil || prior == nil {
		return CandidateResult{}, false, err
	}
	if prior.Content != request.Content || prior.SessionID != request.SessionID ||
		prior.ProjectID != request.ProjectID || prior.Status != "accepted" ||
		prior.VerificationStatus != "evidence_backed" || prior.Metadata["operation_id"] != request.OperationID ||
		prior.Metadata["operation_caller_role"] != request.operationCallerRole ||
		fmt.Sprint(prior.Metadata["operation_protocol_version"]) != fmt.Sprint(request.operationProtocolVersion) ||
		prior.Metadata["operation_request_fingerprint"] != request.operationFingerprint ||
		prior.Metadata["write_mode"] != "lossless_atomic" ||
		prior.Metadata["source_coverage_status"] != "registered" ||
		prior.BlockMembership == nil ||
		prior.BlockMembership.PrimaryBlockID != request.RequestedBlockID {
		return CandidateResult{}, false, errors.New("creation operation receipt conflict")
	}
	verified := false
	for _, ref := range prior.EvidenceRefs {
		if ref.RefType == "verification_report" && ref.ProducerID == "um-mem:exact-source-fidelity/1" {
			verified = true
			break
		}
	}
	segments, ok := prior.Metadata["source_segments"].([]any)
	if !verified || !ok || len(segments) == 0 {
		return CandidateResult{}, false, errors.New("committed lossless operation has no source proof")
	}
	boundWitnessIDs := make(map[string]bool)
	for _, raw := range segments {
		segment, ok := raw.(map[string]any)
		if !ok {
			return CandidateResult{}, false, errors.New("committed lossless source proof is invalid")
		}
		id, ok := segment["witness_id"].(string)
		if !ok || id == "" {
			return CandidateResult{}, false, errors.New("committed lossless source proof is invalid")
		}
		boundWitnessIDs[id] = true
	}
	_, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return CandidateResult{}, false, err
	}
	expectedWitnessRefs := make(map[string]memory.EvidenceRef)
	for _, ref := range prior.EvidenceRefs {
		if ref.RefType != "artifact" {
			continue
		}
		witness, err := engine.ReadStored(ctx, ref.RefID)
		if err != nil {
			return CandidateResult{}, false, err
		}
		if witness != nil && witness.Metadata["record_kind"] == "raw_host_witness" &&
			witness.SessionID == prior.SessionID && witness.ProjectID == prior.ProjectID &&
			validRawWitnessArtifactRef(ref, *witness) {
			expectedWitnessRefs[ref.RefID] = ref
		}
	}
	witnessIDs, linked := make([]string, 0), make(map[string]bool)
	for _, edge := range edges {
		ref, expected := expectedWitnessRefs[edge.DstID]
		if edge.SrcID == prior.BlockID && edge.EdgeType == "derived_from" &&
			edge.Reason == "agent_authored_from_raw_witness" && expected &&
			edge.RunID == prior.SessionID && edge.SubtaskID == prior.SubtaskID &&
			edge.Metadata["source_kind"] == "raw_host_witness" &&
			len(edge.EvidenceRefs) == 1 && reflect.DeepEqual(edge.EvidenceRefs[0], ref) &&
			!linked[edge.DstID] {
			witnessIDs = append(witnessIDs, edge.DstID)
			linked[edge.DstID] = true
		}
	}
	for id := range expectedWitnessRefs {
		if !linked[id] {
			return CandidateResult{}, false, errors.New("committed lossless source edge is unavailable")
		}
	}
	for id := range boundWitnessIDs {
		if !linked[id] {
			return CandidateResult{}, false, errors.New("committed lossless source edge is unavailable")
		}
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return CandidateResult{}, false, err
	}
	var revision int64
	for _, row := range rows {
		if row.Record.BlockID == prior.BlockID && row.Backend == engine.recordNativeStore(row.Record) &&
			row.Status == "committed" {
			revision = row.Revision
			break
		}
	}
	if revision == 0 {
		return CandidateResult{}, false, errors.New("committed lossless operation journal is unavailable")
	}
	route := memory.RouteDecision{Accepted: true,
		Reason:    stringValue(prior.Metadata["route_decision_reason"]),
		BlockID:   prior.BlockMembership.PrimaryBlockID,
		Retention: prior.Retention, DeliveryPolicy: prior.DeliveryPolicy,
		Temperature: prior.Temperature, Authority: prior.Authority,
		Scope: prior.Scope, TrustLevel: prior.TrustLevel,
		VerificationStatus:  prior.VerificationStatus,
		VerificationMethods: append([]string{}, prior.VerificationMethods...),
		PromotionDecision:   prior.PromotionDecision, Status: prior.Status,
		Tags: append([]string{}, prior.Tags...), PolicyIssues: []string{},
		CatalogRevisionID: prior.BlockMembership.CatalogRevisionID}
	receipt := store.WriteReceipt{RecordID: prior.BlockID, CommitRevision: revision, Replayed: true}
	return CandidateResult{Saved: true, RecordID: prior.BlockID,
		OperationID: request.OperationID, Replayed: true, Route: route,
		Receipt:         &receipt,
		ProvenanceEdges: rawWitnessProvenanceReceipts(prior.BlockID, witnessIDs, true)}, true, nil
}

func (engine *Engine) losslessEvidence(ctx context.Context, request CandidateRequest,
	proof losslessProof, route memory.RouteDecision, kind string) ([]memory.EvidenceRef, error) {
	refs := append([]memory.EvidenceRef{}, request.EvidenceRefs...)
	ids := make([]string, 0, len(proof.Witnesses))
	for id := range proof.Witnesses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		witness := proof.Witnesses[id]
		found := false
		for _, ref := range refs {
			if ref.RefID == witness.BlockID {
				if !validRawWitnessArtifactRef(ref, witness) {
					return nil, errors.New("raw witness evidence must bind exact host bytes and producer")
				}
				found = true
				break
			}
		}
		if found {
			continue
		}
		producerID := ""
		for _, ref := range witness.EvidenceRefs {
			if ref.ProducerID != "" {
				producerID = ref.ProducerID
				break
			}
		}
		hash := "sha256:" + sha256Text(witness.Content)
		refs = append(refs, memory.EvidenceRef{RefType: "artifact", RefID: witness.BlockID,
			Hash: &hash, ProducedBy: "watcher", ProducerID: producerID})
	}
	if request.OperationID != "" {
		digest := sha256.Sum256([]byte(request.OperationID))
		key, err := memory.DeriveCanonicalKey(engine.config.WorkspaceID, engine.config.ProfileID,
			kind, "operation_"+hex.EncodeToString(digest[:]))
		if err != nil {
			return nil, err
		}
		prior, err := engine.ReadStored(ctx, memory.BlockIDForCanonicalKey(key))
		if err != nil {
			return nil, err
		}
		if prior != nil {
			if prior.Content != request.Content || prior.ProfileID != engine.config.ProfileID ||
				prior.SessionID != request.SessionID ||
				prior.ProjectID != request.ProjectID || prior.Status != "accepted" ||
				prior.VerificationStatus != "evidence_backed" ||
				prior.Metadata["operation_id"] != request.OperationID ||
				prior.BlockMembership == nil || prior.BlockMembership.PrimaryBlockID != route.BlockID ||
				!sameLosslessSegments(prior.Metadata["source_segments"], proof.Segments) {
				return nil, errors.New("creation operation receipt conflict")
			}
			for _, ref := range prior.EvidenceRefs {
				if ref.RefType == "verification_report" && ref.ProducerID == "um-mem:exact-source-fidelity/1" {
					return append(refs, ref), nil
				}
			}
			return nil, errors.New("creation operation receipt conflict")
		}
	}
	segments := make([]any, 0, len(proof.Segments))
	for _, raw := range proof.Segments {
		segment := raw.(map[string]any)
		segments = append(segments, map[string]any{"source_id": segment["source_id"],
			"source_sha256": segment["source_sha256"], "byte_start": segment["byte_start"],
			"byte_end": segment["byte_end"], "sha256": segment["sha256"],
			"witness_id": segment["witness_id"]})
	}
	scopeID := engine.config.WorkspaceID
	switch route.Scope {
	case "project":
		scopeID = request.ProjectID
	case "session":
		scopeID = request.SessionID
	case "task":
		scopeID = request.TaskID
	case "subtask":
		scopeID = request.SubtaskID
	case "user_global":
		scopeID = request.UserID
	}
	result := map[string]any{"verifier_id": "um-mem:exact-source-fidelity/1",
		"method": "utf8_byte_equality", "content_sha256": sha256Text(request.Content),
		"source_segments": segments, "profile_id": engine.config.ProfileID,
		"session_id": request.SessionID, "target_block_id": route.BlockID,
		"scope": route.Scope, "scope_id": scopeID, "passed": true}
	var event ledger.Event
	var err error
	if request.OperationID == "" {
		event, err = ledger.Append(engine.config.RepoRoot, engine.config.WorkspaceID,
			"verifier", "exact_source_fidelity", "exact_source_fidelity", result)
	} else {
		identity := strings.Join([]string{engine.config.WorkspaceID, engine.config.ProfileID,
			engine.config.BackendID, kind, request.OperationID}, "\x00")
		event, err = ledger.AppendIdempotent(engine.config.RepoRoot, engine.config.WorkspaceID,
			"verifier", "exact_source_fidelity", "exact_source_fidelity", result, identity)
	}
	if err != nil {
		return nil, err
	}
	return append(refs, memory.EvidenceRef{RefType: "verification_report", RefID: event.ID,
		Hash: &event.Hash, ProducedBy: "verifier", ProducerID: "um-mem:exact-source-fidelity/1"}), nil
}
