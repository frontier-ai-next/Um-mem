package mempalace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/memory"
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

func Partition(record memory.MemoryRecord) (PartitionIdentity, error) {
	if record.BlockMembership == nil {
		return PartitionIdentity{}, errors.New("MemPalace native partition requires typed block membership")
	}
	workspace := strings.TrimSpace(record.WorkspaceID)
	primary := strings.TrimSpace(record.BlockMembership.PrimaryBlockID)
	revision := strings.TrimSpace(record.BlockMembership.CatalogRevisionID)
	partition := strings.TrimSpace(record.BlockMembership.BackendPartitionID)
	if workspace == "" || primary == "" || revision == "" || partition == "" {
		return PartitionIdentity{}, errors.New("MemPalace native partition requires workspace, block and catalog revision")
	}
	encoded, err := jsoncompat.MarshalPython([]string{workspace, partition}, true)
	if err != nil {
		return PartitionIdentity{}, err
	}
	sum := sha256.Sum256(encoded)
	return PartitionIdentity{BackendID: "mempalace", WorkspaceID: workspace,
		PrimaryBlockID: primary, CatalogRevisionID: revision,
		PartitionID:    "mempalace:collection-family:" + hex.EncodeToString(sum[:]),
		IsolationClass: "physical", PartitionMode: "native_collection_family"}, nil
}

func StoreForBlock(record memory.MemoryRecord) string {
	return memory.MemPalaceStoreForBlock(record)
}

func CollectionName(record memory.MemoryRecord) (string, error) {
	partition, err := Partition(record)
	if err != nil {
		return "", err
	}
	digest := strings.TrimPrefix(partition.PartitionID, "mempalace:collection-family:")
	return "ummem_" + digest + "_" + strings.ReplaceAll(StoreForBlock(record), ".", "_"), nil
}

func PalacePath(repoRoot string, record memory.MemoryRecord) (string, error) {
	path, err := paths.PalacePathFor(repoRoot, record.WorkspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
