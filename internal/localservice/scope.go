package localservice

import (
	"errors"
	"strings"
)

func boundScopeChain(lineage Lineage, requested []string) ([]string, error) {
	local := []string{}
	for _, entry := range []struct{ kind, id string }{
		{"subtask", lineage.SubtaskID}, {"task", lineage.TaskID}, {"session", lineage.SessionID},
		{"workspace", lineage.WorkspaceID}, {"project", lineage.ProjectID},
	} {
		if entry.id != "" {
			local = append(local, entry.kind+":"+entry.id)
		}
	}
	if len(requested) == 0 {
		return local, nil
	}
	allowed := map[string]bool{}
	for _, key := range local {
		allowed[key] = true
	}
	if lineage.UserID != "" {
		allowed["user_global:"+lineage.UserID] = true
	}
	ranks := map[string]int{"subtask": 0, "task": 1, "session": 2,
		"workspace": 3, "project": 4, "user_global": 5}
	seenKind := map[string]bool{}
	previousRank := -1
	for _, key := range requested {
		kind, scopeID, found := strings.Cut(key, ":")
		rank, valid := ranks[kind]
		if !found || !valid || scopeID == "" || scopeID == "*" ||
			key != strings.TrimSpace(key) || seenKind[kind] || rank < previousRank {
			return nil, errors.New("scope chain is invalid")
		}
		if !allowed[key] {
			return nil, errors.New("scope chain is not bound to caller lineage")
		}
		seenKind[kind] = true
		previousRank = rank
	}
	return requested, nil
}
