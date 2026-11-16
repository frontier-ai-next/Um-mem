package store

import (
	"context"
	"fmt"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// HeadInventory counts unresolved canonical-head reservations in this exact
// workspace/profile. It validates payload identity before reporting counts.
func (index *Index) HeadInventory(ctx context.Context) (pending, failed int, err error) {
	rows, err := index.db.QueryContext(ctx, `SELECT h.successor_id,h.status,COALESCE(b.payload_json,'')
		FROM canonical_head_outbox h LEFT JOIN blocks b ON b.block_id=h.successor_id
		WHERE h.workspace_id=? AND h.status IN ('pending','failed')`, index.workspaceID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, status, payload string
		if err := rows.Scan(&id, &status, &payload); err != nil {
			return 0, 0, err
		}
		record, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil || record.BlockID != id || record.WorkspaceID != index.workspaceID {
			return 0, 0, fmt.Errorf("canonical head inventory contains invalid record %q", id)
		}
		if record.ProfileID != index.profileID {
			continue
		}
		if status == "pending" {
			pending++
		} else {
			failed++
		}
	}
	return pending, failed, rows.Err()
}
