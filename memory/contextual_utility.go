package memory

// ContextualUtilitySummary is shadow-only outcome evidence for one exact task
// context. It never changes trust, truth, deletion or host authority.
type ContextualUtilitySummary struct {
	SummaryID                  string  `json:"summary_id"`
	WorkspaceID                string  `json:"workspace_id"`
	MemoryID                   string  `json:"memory_id"`
	TaskFamily                 string  `json:"task_family"`
	Stage                      string  `json:"stage"`
	ProjectID                  string  `json:"project_id"`
	HostID                     string  `json:"host_id"`
	ProfileID                  string  `json:"profile_id"`
	MinimumSupport             int     `json:"minimum_support"`
	MinimumIndependentOutcomes int     `json:"minimum_independent_outcomes"`
	HelpedCount                int     `json:"helped_count"`
	HurtCount                  int     `json:"hurt_count"`
	NeutralCount               int     `json:"neutral_count"`
	OutcomeDiversity           int     `json:"outcome_diversity"`
	OldestCreditAt             string  `json:"oldest_credit_at"`
	NewestCreditAt             string  `json:"newest_credit_at"`
	SupportCount               int     `json:"support_count"`
	ShadowUtility              float64 `json:"shadow_utility"`
	ShadowEligible             bool    `json:"shadow_eligible"`
	RankingMode                string  `json:"ranking_mode"`
	AffectsTrust               bool    `json:"affects_trust"`
	AffectsTruth               bool    `json:"affects_truth"`
	AffectsDeletion            bool    `json:"affects_deletion"`
	AffectsAuthority           bool    `json:"affects_authority"`
}
