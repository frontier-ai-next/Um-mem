package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// stageTopologyProjection copies one exact committed record into an unpublished
// native leaf. All selected Go backends already verify their Project result
// against the target partition; the catalog pointer remains unchanged here.
func (engine *Engine) stageTopologyProjection(ctx context.Context,
	source store.ProjectionRow, targetID, partitionID, operationID string) error {
	if source.Status != "committed" || !source.Current ||
		source.Backend != engine.recordNativeStore(source.Record) || source.Revision < 1 ||
		source.NativeID == "" || source.Record.BlockMembership == nil ||
		source.Record.WorkspaceID != engine.config.WorkspaceID ||
		source.Record.ProfileID != engine.config.ProfileID {
		return errors.New("topology source record is not a committed native projection")
	}
	if targetID == "" || targetID != strings.TrimSpace(targetID) ||
		partitionID == "" || partitionID != strings.TrimSpace(partitionID) ||
		operationID == "" || operationID != strings.TrimSpace(operationID) ||
		targetID == source.Record.BlockMembership.PrimaryBlockID {
		return errors.New("topology target projection identity is invalid")
	}
	projected := source.Record
	membership := *source.Record.BlockMembership
	membership.PrimaryBlockID = targetID
	membership.BackendPartitionID = partitionID
	membership.CatalogRevisionID = "topology:" + operationID
	secondary := make([]string, 0, len(membership.SecondaryBlockIDs))
	for _, id := range membership.SecondaryBlockIDs {
		if id != targetID {
			secondary = append(secondary, id)
		}
	}
	membership.SecondaryBlockIDs = secondary
	membership.MembershipRevisionID = membership.RevisionHash()
	projected.BlockMembership = &membership
	if err := projected.ValidateSafety(); err != nil {
		return err
	}
	payload, err := jsoncompat.MarshalPython(projected, false)
	if err != nil {
		return err
	}
	decoded, err := memory.DecodeCanonicalRecord(payload)
	if err != nil || !reflect.DeepEqual(decoded, projected) {
		return errors.New("topology target projection differs from source record")
	}
	if engine.projector == nil || engine.projector.BackendID() != engine.config.BackendID {
		return errors.New("selected backend projector is unavailable")
	}
	result, err := engine.projector.Project(ctx, projected, string(payload), source.Revision)
	if err != nil {
		return err
	}
	if !result.Verified || result.Skipped || result.NativeID == "" {
		return errors.New("selected backend did not verify staged topology projection")
	}
	return nil
}
