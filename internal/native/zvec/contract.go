package zvec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/memory"
)

const (
	windowChars = 480
	windowStep  = 360
)

type PartitionIdentity struct {
	BackendID         string `json:"backend_id"`
	WorkspaceID       string `json:"workspace_id"`
	PrimaryBlockID    string `json:"primary_block_id"`
	CatalogRevisionID string `json:"catalog_revision_id"`
	PartitionID       string `json:"partition_id"`
	IsolationClass    string `json:"isolation_class"`
	PartitionMode     string `json:"partition_mode"`
}

type Chunk struct {
	ID      string `json:"id"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Content string `json:"content"`
}

// NativeCandidate is an untrusted index hit until it has been matched to a
// committed canonical record and its expected physical partition.
type NativeCandidate struct {
	ChunkID        string
	RecordID       string
	SourceSHA256   string
	CommitRevision int64
	Score          float64
}

func ValidateCandidate(candidate NativeCandidate, record memory.MemoryRecord, revision int64, expectedPartitionID string) error {
	partition, err := Partition(record)
	if err != nil {
		return err
	}
	if candidate.RecordID != record.BlockID || candidate.CommitRevision != revision ||
		candidate.SourceSHA256 != digest(record.Content) || partition.PartitionID != expectedPartitionID {
		return errors.New("Zvec native candidate differs from committed canonical record")
	}
	chunks, _ := Chunks(record)
	for _, chunk := range chunks {
		if chunk.ID == candidate.ChunkID {
			return nil
		}
	}
	return errors.New("Zvec native candidate has no canonical chunk")
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Partition reproduces the physical collection identity of Python Zvec 0.7.
func Partition(record memory.MemoryRecord) (PartitionIdentity, error) {
	if record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID == "" ||
		record.BlockMembership.CatalogRevisionID == "" ||
		record.BlockMembership.BackendPartitionID == "" || record.WorkspaceID == "" {
		return PartitionIdentity{}, errors.New("Zvec native partition requires exact leaf and workspace identity")
	}
	membership := record.BlockMembership
	return PartitionIdentity{BackendID: "zvec", WorkspaceID: record.WorkspaceID,
		PrimaryBlockID: membership.PrimaryBlockID, CatalogRevisionID: membership.CatalogRevisionID,
		PartitionID:    "zvec:collection:" + digest(record.WorkspaceID+"\x00"+membership.BackendPartitionID),
		IsolationClass: "physical", PartitionMode: "native_collection"}, nil
}

func CollectionPath(repoRoot string, record memory.MemoryRecord) (string, error) {
	partition, err := Partition(record)
	if err != nil {
		return "", err
	}
	root, err := paths.WorkspaceMemoryRoot(repoRoot, record.WorkspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "zvec", "partitions", partition.PartitionID[len("zvec:collection:"):]), nil
}

// ProjectionLockPath reproduces BackendSQLiteIndex.projection_gate's lock
// identity so Python and Go writers serialize projection of one partition.
func ProjectionLockPath(repoRoot string, record memory.MemoryRecord) (string, error) {
	if record.BlockMembership == nil || record.WorkspaceID == "" || record.ProfileID == "" ||
		record.BlockMembership.BackendPartitionID == "" {
		return "", errors.New("Zvec projection lock requires a bound record")
	}
	root, err := paths.WorkspaceMemoryRoot(repoRoot, record.WorkspaceID)
	if err != nil {
		return "", err
	}
	parts := []string{record.WorkspaceID, record.ProfileID, record.BlockMembership.BackendPartitionID}
	encoded := make([]string, len(parts))
	for i, part := range parts {
		literal, err := jsoncompat.MarshalPython(part, true)
		if err != nil {
			return "", err
		}
		encoded[i] = string(literal)
	}
	key := digest("[" + strings.Join(encoded, ", ") + "]")
	return filepath.Join(root, "zvec", "UM_MEM_index.sqlite.projection-"+key+".lock"), nil
}

// Chunks uses Python code-point positions, not UTF-8 byte offsets. Those
// positions are part of immutable Zvec document identities and metadata.
func Chunks(record memory.MemoryRecord) ([]Chunk, string) {
	content := []rune(record.Content)
	chunks := []Chunk{}
	for start := 0; start < max(1, len(content)); start += windowStep {
		end := min(start+windowChars, len(content))
		chunks = append(chunks, Chunk{ID: digest(fmt.Sprintf("%s\x00%d\x00%d", record.BlockID, start, end)),
			Start: start, End: end, Content: string(content[start:end])})
	}
	return chunks, digest(record.Content)
}
