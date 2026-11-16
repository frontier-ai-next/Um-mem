package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

func shortSHA256(value string, digits int) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:digits]
}

// DeriveCanonicalKey preserves the Python creation identity. Its JSON array
// intentionally uses the default Python separators, including comma spaces.
func DeriveCanonicalKey(workspaceID, profileID, kind, recordID string) (string, error) {
	if recordID == "" {
		return "", errors.New("canonical creation requires an explicit record identity")
	}
	values := []string{workspaceID, profileID, kind, recordID}
	encoded := make([]string, 0, len(values))
	for _, value := range values {
		item, err := jsoncompat.MarshalPython(value, false)
		if err != nil {
			return "", err
		}
		encoded = append(encoded, string(item))
	}
	material := "[" + strings.Join(encoded, ", ") + "]"
	prefix := workspaceID + ":" + kind
	if profileID != "default" {
		prefix = workspaceID + ":" + profileID + ":" + kind
	}
	return prefix + ":" + shortSHA256(material, 24), nil
}

func BlockIDForCanonicalKey(canonicalKey string) string {
	return "mem_" + shortSHA256(canonicalKey, 16)
}

func BlockIDForRevisionID(revisionID string) string {
	return "mem_rev_" + shortSHA256(revisionID, 16)
}

func DuplicateClusterID(record MemoryRecord) (string, error) {
	axes := []string{strings.TrimSpace(record.WorkspaceID)}
	if record.ProfileID != "default" {
		axes = append(axes, "profile:"+strings.TrimSpace(record.ProfileID))
	}
	axes = append(axes, record.Kind, record.Content)
	encoded, err := jsoncompat.MarshalPython(axes, false)
	if err != nil {
		return "", err
	}
	return "dup_" + shortSHA256(string(encoded), 64), nil
}

var revisionOmittedFields = []string{
	"block_id", "canonical_key", "revision_id", "duplicate_cluster_id", "recorded_at",
	"created_at", "updated_at", "status", "retention", "delivery_policy",
	"temperature", "authority", "trust_level", "verification_status",
	"verification_methods", "promotion_decision", "revision_of", "supersedes_ids",
	"semantic_catalog_version",
}

var revisionMetadataOmittedFields = []string{
	"block_id", "canonical_id", "canonical_key", "backend_reference", "backend_revision",
	"commit_revision", "idempotency_key", "memory_id", "mutation_correlation_id",
	"native_id", "native_reference", "native_revision", "recorded_at", "record_id",
	"revision_id", "service_receipt_sha256",
}

// RevisionIDForRecord calculates the semantic revision identity used by the
// Python mutation planner. Mutable lifecycle fields are deliberately excluded.
func RevisionIDForRecord(canonicalKey string, record MemoryRecord, mutationAction, predecessorRevisionID string) (string, error) {
	if canonicalKey == "" {
		return "", errors.New("revision identity requires a canonical key")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}
	for _, field := range revisionOmittedFields {
		delete(payload, field)
	}
	metadata, ok := payload["metadata"].(map[string]any)
	if !ok {
		metadata = map[string]any{}
	}
	for _, field := range revisionMetadataOmittedFields {
		delete(metadata, field)
	}
	payload["metadata"] = metadata
	payload["canonical_key"] = canonicalKey
	if membership, ok := payload["block_membership"].(map[string]any); ok {
		secondary, ok := membership["secondary_block_ids"].([]any)
		if !ok {
			secondary = []any{}
		}
		payload["block_membership"] = map[string]any{
			"primary_block_id":    membership["primary_block_id"],
			"secondary_block_ids": secondary,
		}
	}
	if predecessorRevisionID != "" {
		delete(payload, "valid_from")
		delete(payload, "valid_to")
	}
	payload["mutation_action"] = mutationAction
	payload["predecessor_revision_id"] = predecessorRevisionID
	material, err := jsoncompat.MarshalPython(payload, false)
	if err != nil {
		return "", fmt.Errorf("encode semantic revision payload: %w", err)
	}
	return "rev_" + shortSHA256(string(material), 64), nil
}
