package memory

import "encoding/json"

// EdgeRecord uses the persisted graph payload shared by all backend indexes.
type EdgeRecord struct {
	SrcID        string         `json:"src_id"`
	DstID        string         `json:"dst_id"`
	EdgeType     string         `json:"edge_type"`
	WorkspaceID  string         `json:"workspace_id"`
	RunID        string         `json:"run_id"`
	SubtaskID    string         `json:"subtask_id"`
	Reason       string         `json:"reason"`
	Weight       json.Number    `json:"weight"`
	EvidenceRefs []EvidenceRef  `json:"evidence_refs"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    string         `json:"created_at"`
}
