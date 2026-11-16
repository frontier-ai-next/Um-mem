package engine

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestProjectionBarrierObservesOnlyRequestedRevision(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	staged, err := selected.writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	deadline, err := selected.ProjectionBarrier(ctx, record.BlockID, staged.CommitRevision, 0)
	if err != nil || deadline["reason_code"] != "projection_deadline_exceeded" ||
		deadline["commit_revision"] != staged.CommitRevision || deadline["retryable"] != true {
		t.Fatalf("pending write did not produce a scoped deadline result: %+v, %v", deadline, err)
	}
	mismatch, err := selected.ProjectionBarrier(ctx, record.BlockID, staged.CommitRevision+1, 1)
	if err != nil || mismatch["reason_code"] != "projection_revision_mismatch" ||
		mismatch["projection_revision"] != int64(0) || mismatch["retryable"] != false {
		t.Fatalf("different revision was accepted: %+v, %v", mismatch, err)
	}
	other := record
	other.BlockID = "unrelated-pending"
	other.RevisionID = "unrelated-revision"
	other.BlockMembership = nil
	if err := selected.BindRootRecord(ctx, &other, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.writer.PrepareRootWrite(ctx, other); err != nil {
		t.Fatal(err)
	}
	complete := make(chan error, 1)
	go func() {
		time.Sleep(80 * time.Millisecond)
		_, err := selected.writer.CompleteProjection(ctx, staged.CommitRevision, verifiedProjector{})
		complete <- err
	}()
	committed, err := selected.ProjectionBarrier(ctx, record.BlockID, staged.CommitRevision, 1)
	if err != nil || committed["ok"] != true || committed["projection_revision"] != staged.CommitRevision {
		t.Fatalf("named revision was not observed after native completion: %+v, %v", committed, err)
	}
	if err := <-complete; err != nil {
		t.Fatal(err)
	}
	committedMismatch, err := selected.ProjectionBarrier(ctx, record.BlockID, staged.CommitRevision+1, 0)
	if err != nil || committedMismatch["reason_code"] != "projection_revision_mismatch" ||
		committedMismatch["projection_revision"] != staged.CommitRevision {
		t.Fatalf("committed revision mismatch did not report the observed revision: %+v, %v", committedMismatch, err)
	}
	otherProfile, err := Open(Config{RepoRoot: selected.config.RepoRoot, WorkspaceID: "alpha", ProfileID: "other", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer otherProfile.Close()
	if _, err := otherProfile.ProjectionBarrier(ctx, record.BlockID, 0, 0); err == nil {
		t.Fatal("projection barrier revealed an out-of-profile record")
	}
}

func TestProjectionBarrierReportsExhaustedRetryWithoutRecovering(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	staged, err := selected.writer.PrepareRootWrite(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", selected.IndexPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE write_outbox SET error=? WHERE commit_revision=?`,
		`{"kind":"native_projection","attempts":3}`, staged.CommitRevision); err != nil {
		t.Fatal(err)
	}
	attention, err := selected.ProjectionBarrier(ctx, record.BlockID, staged.CommitRevision, 0)
	if err != nil || attention["reason_code"] != "native_projection_attention" ||
		attention["projection_status"] != "pending" || attention["attention_required"] != true ||
		attention["retryable"] != false {
		t.Fatalf("exhausted native retry was not reported: %+v, %v", attention, err)
	}
	status, revision, err := selected.index.ProjectionStatus(ctx, record.BlockID)
	if err != nil || status != "pending" || revision != staged.CommitRevision {
		t.Fatalf("barrier started recovery or changed the journal: %q %d %v", status, revision, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE write_outbox SET status='failed' WHERE commit_revision=?`, staged.CommitRevision); err != nil {
		t.Fatal(err)
	}
	failed, err := selected.ProjectionBarrier(ctx, record.BlockID, staged.CommitRevision, 0)
	if err != nil || failed["reason_code"] != "native_projection_attention" || failed["projection_status"] != "failed" {
		t.Fatalf("failed native projection was not reported: %+v, %v", failed, err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM write_outbox WHERE commit_revision=?`, staged.CommitRevision); err != nil {
		t.Fatal(err)
	}
	missing, err := selected.ProjectionBarrier(ctx, record.BlockID, 0, 0)
	if err != nil || missing["ok"] != false || missing["projection_status"] != "pending" ||
		missing["reason_code"] != "projection_deadline_exceeded" {
		t.Fatalf("missing outbox entry was treated as a successful projection: %+v, %v", missing, err)
	}
}
