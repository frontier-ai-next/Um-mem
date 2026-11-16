package memory

type DependencyIssue struct {
	RecordID string `json:"record_id"`
	Code     string `json:"code"`
}

type DependencyValidation struct {
	RecordID         string            `json:"record_id"`
	WorkspaceID      string            `json:"workspace_id"`
	ProfileID        string            `json:"profile_id"`
	AsOf             string            `json:"as_of"`
	Status           string            `json:"status"`
	CheckedRecordIDs []string          `json:"checked_record_ids"`
	Issues           []DependencyIssue `json:"issues"`
	MaxNodes         int               `json:"max_nodes"`
	MaxEdges         int               `json:"max_edges"`
}
