package memory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
)

const CurrentSchemaVersion = 9

type EvidenceRef struct {
	RefType           string  `json:"ref_type"`
	RefID             string  `json:"ref_id"`
	Hash              *string `json:"hash,omitempty"`
	ProducedBy        string  `json:"produced_by"`
	ProducerID        string  `json:"producer_id"`
	StageID           *string `json:"stage_id,omitempty"`
	SubtaskID         *string `json:"subtask_id,omitempty"`
	CreatedAfterEvent *string `json:"created_after_event,omitempty"`
}

// MarshalJSON keeps the canonical Python record shape with explicit nulls.
// The optional field tags above describe the public input schema separately.
func (ref EvidenceRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		RefType           string  `json:"ref_type"`
		RefID             string  `json:"ref_id"`
		Hash              *string `json:"hash"`
		ProducedBy        string  `json:"produced_by"`
		ProducerID        string  `json:"producer_id"`
		StageID           *string `json:"stage_id"`
		SubtaskID         *string `json:"subtask_id"`
		CreatedAfterEvent *string `json:"created_after_event"`
	}{ref.RefType, ref.RefID, ref.Hash, ref.ProducedBy, ref.ProducerID,
		ref.StageID, ref.SubtaskID, ref.CreatedAfterEvent})
}

type RecordBlockMembership struct {
	PrimaryBlockID       string   `json:"primary_block_id"`
	CatalogRevisionID    string   `json:"catalog_revision_id"`
	BackendPartitionID   string   `json:"backend_partition_id"`
	SecondaryBlockIDs    []string `json:"secondary_block_ids"`
	RecordID             string   `json:"record_id"`
	RecordRevisionID     string   `json:"record_revision_id"`
	WorkspaceID          string   `json:"workspace_id"`
	ValidFrom            string   `json:"valid_from"`
	ValidTo              string   `json:"valid_to"`
	MembershipRevisionID string   `json:"membership_revision_id"`
}

func StaticBackendPartitionID(profileID, blockID string) string {
	material, _ := jsoncompat.MarshalPython([]string{profileID, blockID}, true)
	digest := sha256.Sum256(material)
	return "static:" + hex.EncodeToString(digest[:])
}

func (membership RecordBlockMembership) RevisionHash() string {
	payload := map[string]any{
		"record_id":            membership.RecordID,
		"record_revision_id":   membership.RecordRevisionID,
		"workspace_id":         membership.WorkspaceID,
		"primary_block_id":     membership.PrimaryBlockID,
		"secondary_block_ids":  membership.SecondaryBlockIDs,
		"catalog_revision_id":  membership.CatalogRevisionID,
		"backend_partition_id": membership.BackendPartitionID,
		"valid_from":           membership.ValidFrom,
	}
	material, _ := jsoncompat.MarshalPython(payload, false)
	digest := sha256.Sum256(material)
	return "membership_" + hex.EncodeToString(digest[:])
}

func (membership RecordBlockMembership) Validate() error {
	if strings.TrimSpace(membership.PrimaryBlockID) == "" || strings.TrimSpace(membership.CatalogRevisionID) == "" ||
		strings.TrimSpace(membership.BackendPartitionID) == "" {
		return errors.New("record block membership requires primary block, catalog revision, and partition")
	}
	if len(membership.SecondaryBlockIDs) > 16 {
		return errors.New("record block membership accepts at most 16 secondary blocks")
	}
	seen := make(map[string]struct{}, len(membership.SecondaryBlockIDs))
	for _, blockID := range membership.SecondaryBlockIDs {
		if blockID == "" || blockID == membership.PrimaryBlockID {
			return errors.New("secondary blocks must be non-empty and different from the primary block")
		}
		if _, exists := seen[blockID]; exists {
			return errors.New("secondary block IDs must be unique")
		}
		seen[blockID] = struct{}{}
	}
	bound := membership.RecordID != "" || membership.RecordRevisionID != "" || membership.WorkspaceID != ""
	if bound && (membership.RecordID == "" || membership.RecordRevisionID == "" || membership.WorkspaceID == "") {
		return errors.New("record block membership identity requires record, revision, and workspace")
	}
	if membership.ValidFrom != "" {
		if _, err := time.Parse(time.RFC3339Nano, membership.ValidFrom); err != nil {
			return errors.New("membership valid_from must include a timezone")
		}
	}
	if membership.ValidTo != "" {
		if membership.ValidFrom == "" {
			return errors.New("membership valid_to requires valid_from")
		}
		end, err := time.Parse(time.RFC3339Nano, membership.ValidTo)
		if err != nil {
			return errors.New("membership valid_to must include a timezone")
		}
		start, _ := time.Parse(time.RFC3339Nano, membership.ValidFrom)
		if end.Before(start) {
			return errors.New("membership valid_to must not precede valid_from")
		}
	}
	if !bound && membership.MembershipRevisionID != "" {
		return errors.New("unbound membership cannot have a revision ID")
	}
	if bound && membership.MembershipRevisionID != membership.RevisionHash() {
		return errors.New("membership revision ID does not match its payload")
	}
	return nil
}

// MemoryRecord uses the canonical JSON field names so Go can read existing
// records without discarding their provenance and lifecycle fields.
type MemoryRecord struct {
	BlockID                string                 `json:"block_id"`
	Content                string                 `json:"content"`
	BlockMembership        *RecordBlockMembership `json:"block_membership"`
	CanonicalKey           string                 `json:"canonical_key"`
	RevisionID             string                 `json:"revision_id"`
	DuplicateClusterID     string                 `json:"duplicate_cluster_id"`
	RecordedAt             string                 `json:"recorded_at"`
	Title                  string                 `json:"title"`
	Kind                   string                 `json:"kind"`
	Status                 string                 `json:"status"`
	Retention              string                 `json:"retention"`
	DeliveryPolicy         string                 `json:"delivery_policy"`
	Temperature            string                 `json:"temperature"`
	Authority              string                 `json:"authority"`
	TrustLevel             string                 `json:"trust_level"`
	Scope                  string                 `json:"scope"`
	VerificationStatus     string                 `json:"verification_status"`
	VerificationMethods    []string               `json:"verification_methods"`
	PromotionDecision      string                 `json:"promotion_decision"`
	UserID                 string                 `json:"user_id"`
	WorkspaceID            string                 `json:"workspace_id"`
	ProfileID              string                 `json:"profile_id"`
	RunID                  string                 `json:"run_id"`
	StageID                string                 `json:"stage_id"`
	SubtaskID              string                 `json:"subtask_id"`
	Producer               string                 `json:"producer"`
	SourceBackend          string                 `json:"source_backend"`
	Tags                   []string               `json:"tags"`
	EvidenceRefs           []EvidenceRef          `json:"evidence_refs"`
	ExternalRefs           map[string]string      `json:"external_refs"`
	Sensitivity            string                 `json:"sensitivity"`
	ContentMode            string                 `json:"content_mode"`
	OperationalRisk        string                 `json:"operational_risk"`
	RevisionOf             string                 `json:"revision_of"`
	SupersedesIDs          []string               `json:"supersedes_ids"`
	ExpiresAt              string                 `json:"expires_at"`
	ChildClaims            []json.RawMessage      `json:"child_claims"`
	Metadata               map[string]any         `json:"metadata"`
	CreatedAt              string                 `json:"created_at"`
	UpdatedAt              string                 `json:"updated_at"`
	SessionID              string                 `json:"session_id"`
	TaskID                 string                 `json:"task_id"`
	ParentSessionID        string                 `json:"parent_session_id"`
	ArtifactID             string                 `json:"artifact_id"`
	ArtifactRevision       string                 `json:"artifact_revision"`
	ValidFrom              string                 `json:"valid_from"`
	ValidTo                string                 `json:"valid_to"`
	ProjectID              string                 `json:"project_id"`
	RepositoryID           string                 `json:"repository_id"`
	Checkout               string                 `json:"checkout"`
	Worktree               string                 `json:"worktree"`
	Branch                 string                 `json:"branch"`
	Revision               string                 `json:"revision"`
	DirtyState             string                 `json:"dirty_state"`
	SchemaVersion          int                    `json:"schema_version"`
	HardPolicyVersion      int                    `json:"hard_policy_version"`
	SemanticCatalogVersion string                 `json:"semantic_catalog_version"`
}

func DecodeCanonicalRecord(payload []byte) (MemoryRecord, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil || raw == nil {
		return MemoryRecord{}, errors.New("canonical memory record must be a JSON object")
	}
	shape := reflect.TypeOf(MemoryRecord{})
	fields := make(map[string]struct{}, shape.NumField())
	for index := range shape.NumField() {
		name := shape.Field(index).Tag.Get("json")
		fields[name] = struct{}{}
		if _, found := raw[name]; !found {
			return MemoryRecord{}, fmt.Errorf("canonical memory record missing %s", name)
		}
	}
	for name := range raw {
		if _, found := fields[name]; !found {
			return MemoryRecord{}, fmt.Errorf("canonical memory record has unknown field %s", name)
		}
	}
	var record MemoryRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&record); err != nil {
		return MemoryRecord{}, err
	}
	if err := record.ValidateSafety(); err != nil {
		return MemoryRecord{}, err
	}
	return record, nil
}

func NewMemoryRecord(content, workspaceID string) (MemoryRecord, error) {
	identifier := make([]byte, 8)
	if _, err := rand.Read(identifier); err != nil {
		return MemoryRecord{}, err
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return MemoryRecord{
		BlockID: "mem_" + hex.EncodeToString(identifier), Content: content,
		Kind: "observation", Status: "candidate", Retention: "durable",
		DeliveryPolicy: "on_demand", Temperature: "warm", Authority: "advisory",
		TrustLevel: "agent_reported", Scope: "workspace", VerificationStatus: "unverified",
		PromotionDecision: "not_requested", WorkspaceID: workspaceID, ProfileID: "default",
		Producer: "agent", Sensitivity: "internal", ContentMode: "full", OperationalRisk: "low",
		VerificationMethods: []string{}, Tags: []string{}, EvidenceRefs: []EvidenceRef{},
		ExternalRefs: map[string]string{}, SupersedesIDs: []string{}, ChildClaims: []json.RawMessage{},
		Metadata:  map[string]any{},
		CreatedAt: now, UpdatedAt: now, SchemaVersion: CurrentSchemaVersion, HardPolicyVersion: 1,
	}, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func (ref EvidenceRef) Validate() error {
	if !oneOf(ref.RefType, "ledger_event", "session_event", "message", "tool_call", "tool_result", "file_snapshot", "source_span", "diff", "test_run", "artifact", "verification_report", "human_approval") ||
		strings.TrimSpace(ref.RefID) == "" || strings.TrimSpace(ref.ProducerID) == "" ||
		!oneOf(ref.ProducedBy, "agent", "supervisor", "verifier", "watcher", "harness", "human") {
		return errors.New("evidence reference requires a supported type, actor, and producer identity")
	}
	return nil
}

var reservedMetadata = func() map[string]struct{} {
	keys := strings.Fields(`authority authority_class backend backend_id backend_reference backend_revision
		block_id canonical_id canonical_key child_claims commit_revision content_mode delivery_policy
		duplicate_cluster_id evidence_refs idempotency_key lifecycle logical_block_id memory_id
		mutation_correlation_id native_id native_reference native_revision operational_risk
		promotion_decision promotion_gate promotion_reason promotion_result promotion_status recorded_at
		record_id revision_id revision_of run_id privacy_class retention risk risk_level safety_class
		safety_policy selected_backend service_receipt_sha256 sensitivity source_backend scope scope_chain
		session_id stage_id status subtask_id surface temperature tier trust_level task_id supersedes_ids
		user_id verification_methods verification_status verified workspace_id project_id parent_session_id
		mutation_of supersedes superseded_by token_budget`)
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result
}()

func reservedMetadataKey(key string) bool {
	_, found := reservedMetadata[key]
	return found || key == "store" || strings.HasSuffix(key, "_store") || strings.HasSuffix(key, "_write_policy")
}

// ReservedMetadataKey exposes the same governance boundary to mutation entry points.
func ReservedMetadataKey(key string) bool { return reservedMetadataKey(key) }

func (r MemoryRecord) ScopeKey() (string, error) {
	var identity string
	switch r.Scope {
	case "subtask":
		identity = r.SubtaskID
	case "task":
		identity = r.TaskID
	case "session":
		identity = r.SessionID
	case "workspace":
		identity = r.WorkspaceID
	case "project":
		identity = r.ProjectID
	case "user_global":
		identity = r.UserID
	default:
		return "", fmt.Errorf("unsupported memory scope %q", r.Scope)
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return "", fmt.Errorf("%s scope requires identity", r.Scope)
	}
	return r.Scope + ":" + identity, nil
}

func (r MemoryRecord) IsProtected() bool {
	if !oneOf(r.Sensitivity, "public", "internal", "confidential", "secret") ||
		!oneOf(r.ContentMode, "full", "pointer_only", "non_injectable") ||
		!oneOf(r.OperationalRisk, "low", "medium", "high", "critical") {
		return true
	}
	return r.Sensitivity == "confidential" || r.Sensitivity == "secret" || r.ContentMode == "non_injectable"
}

// ValidateSafety checks the authority and scope gates before a record can be
// considered for storage or delivery. Storage-specific revision and membership
// checks are applied by the writer.
func (r MemoryRecord) ValidateSafety() error {
	if strings.TrimSpace(r.BlockID) == "" || strings.TrimSpace(r.Kind) == "" || strings.TrimSpace(r.ProfileID) == "" {
		return errors.New("memory identity, kind, and profile are required")
	}
	if !oneOf(r.Status, "candidate", "accepted", "quarantined", "superseded", "deprecated", "retracted") ||
		!oneOf(r.Retention, "ephemeral", "session", "durable", "archive") ||
		!oneOf(r.DeliveryPolicy, "always_on", "working_set", "on_demand", "reflection_only", "hidden") ||
		!oneOf(r.Temperature, "hot", "warm", "cold") ||
		!oneOf(r.Authority, "advisory", "directive") ||
		!oneOf(r.TrustLevel, "untrusted", "agent_reported", "source_observed", "workspace_verified", "external_verified", "supervisor_verified", "human_approved", "system_curated") ||
		!oneOf(r.VerificationStatus, "unverified", "observed", "evidence_backed", "verified", "disputed") ||
		!oneOf(r.PromotionDecision, "not_requested", "accepted", "rejected") ||
		!oneOf(r.Scope, "subtask", "task", "session", "workspace", "project", "user_global") {
		return errors.New("memory lifecycle or authority field is unsupported")
	}
	if !oneOf(r.Sensitivity, "public", "internal", "confidential", "secret") ||
		!oneOf(r.ContentMode, "full", "pointer_only", "non_injectable") ||
		!oneOf(r.OperationalRisk, "low", "medium", "high", "critical") {
		return errors.New("memory safety field is unsupported")
	}
	for key := range r.Metadata {
		if reservedMetadataKey(key) {
			return fmt.Errorf("metadata contains reserved governance field %q", key)
		}
	}
	for _, tag := range r.Tags {
		if strings.TrimSpace(tag) == "" || len([]rune(strings.TrimSpace(tag))) > 96 {
			return errors.New("memory tags must be non-empty and at most 96 characters")
		}
	}
	for _, method := range r.VerificationMethods {
		if !oneOf(method, "test_run", "verification_report", "human_review", "artifact_inspection", "diff_review", "tool_replay") {
			return fmt.Errorf("unsupported verification method %q", method)
		}
	}
	for _, ref := range r.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	if _, err := r.ScopeKey(); err != nil {
		return err
	}
	if r.BlockMembership != nil {
		if err := r.BlockMembership.Validate(); err != nil {
			return err
		}
		membership := r.BlockMembership
		if membership.RecordID != "" && (membership.RecordID != r.BlockID ||
			membership.RecordRevisionID != r.RevisionID || membership.WorkspaceID != r.WorkspaceID) {
			return errors.New("record block membership identity mismatch")
		}
	}
	if r.ParentSessionID != "" && r.SessionID == "" {
		return errors.New("parent_session_id requires session_id")
	}
	if (r.ArtifactID == "") != (r.ArtifactRevision == "") {
		return errors.New("artifact_id and artifact_revision must be provided together")
	}
	if r.RevisionOf != "" {
		if len(r.SupersedesIDs) != 1 || r.SupersedesIDs[0] != r.RevisionOf || r.RevisionOf == r.BlockID {
			return errors.New("revision successors must supersede exactly revision_of")
		}
	} else if len(r.SupersedesIDs) != 0 {
		return errors.New("root memory records cannot carry supersedes_ids")
	}
	var start, end time.Time
	if r.ValidFrom != "" {
		parsed, err := time.Parse(time.RFC3339Nano, r.ValidFrom)
		if err != nil {
			return errors.New("valid_from must be a timezone-aware ISO timestamp")
		}
		start = parsed
	}
	if r.ValidTo != "" {
		parsed, err := time.Parse(time.RFC3339Nano, r.ValidTo)
		if err != nil {
			return errors.New("valid_to must be a timezone-aware ISO timestamp")
		}
		end = parsed
	}
	if r.ValidTo != "" && (r.ValidFrom == "" || end.Before(start)) {
		return errors.New("valid_to requires valid_from and must not precede it")
	}
	if r.Scope == "session" || r.Scope == "task" || r.Scope == "subtask" {
		if r.WorkspaceID == "" || r.SessionID == "" {
			return errors.New("scoped memory requires workspace_id and session_id")
		}
	}
	if r.Scope == "task" || r.Scope == "subtask" {
		if r.TaskID == "" {
			return errors.New("task-scoped memory requires task_id")
		}
	}
	if r.Status == "retracted" && (r.Retention != "archive" || r.DeliveryPolicy != "hidden") {
		return errors.New("retracted memory requires archive retention and hidden delivery")
	}
	if r.Authority == "directive" && r.Status != "accepted" {
		return errors.New("directive memory requires accepted status")
	}
	if r.VerificationStatus == "evidence_backed" || r.VerificationStatus == "verified" {
		if len(r.VerificationMethods) == 0 || len(r.EvidenceRefs) == 0 {
			return errors.New("evidence-backed memory requires typed methods and evidence")
		}
	}
	if r.Status == "accepted" {
		semantic := r.VerificationStatus == "verified" && oneOf(r.TrustLevel, "workspace_verified", "external_verified", "supervisor_verified", "human_approved", "system_curated")
		integrity := r.Authority == "advisory" && r.VerificationStatus == "evidence_backed" && r.TrustLevel == "agent_reported"
		if !semantic && !integrity {
			return errors.New("accepted memory requires integrity-backed or semantic assurance")
		}
		if r.PromotionDecision != "accepted" || !hasDecisiveEvidence(r.EvidenceRefs) {
			return errors.New("accepted memory requires accepted promotion and decisive evidence")
		}
	}
	if r.SchemaVersion != CurrentSchemaVersion || r.HardPolicyVersion < 1 {
		return errors.New("memory schema version must be current and policy version positive")
	}
	return nil
}

func (r MemoryRecord) ValidateForStorage() error {
	if err := r.ValidateSafety(); err != nil {
		return err
	}
	if r.IsProtected() || r.isIndexOnly() {
		return errors.New("protected or index-only memory cannot enter selected backend storage")
	}
	if r.BlockMembership == nil || r.RevisionID == "" {
		return errors.New("canonical storage requires a bound primary block and record revision")
	}
	membership := r.BlockMembership
	if membership.RecordID != r.BlockID || membership.RecordRevisionID != r.RevisionID ||
		membership.WorkspaceID != r.WorkspaceID || membership.MembershipRevisionID == "" {
		return errors.New("record block membership identity does not match canonical record")
	}
	return nil
}

func hasDecisiveEvidence(refs []EvidenceRef) bool {
	for _, ref := range refs {
		if ref.ProducerID != "" && oneOf(ref.RefType, "verification_report", "test_run", "human_approval") {
			return true
		}
	}
	return false
}
