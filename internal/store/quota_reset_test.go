package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestQuotaResetMigrationAndImmutableBatchSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := openTestStore(t)

	boundID := insertQuotaResetTestUser(t, store, "bound", "Bound User", StatusActive, ptrSnapshot(compliantSnapshot(101)))
	unboundID := insertQuotaResetTestUser(t, store, "unbound", "Unbound User", StatusDisabled, nil)
	deletedID := insertQuotaResetTestUser(t, store, "deleted", "Deleted User", StatusActive, ptrSnapshot(compliantSnapshot(103)))
	if err := store.DeleteUser(ctx, deletedID, 0); err != nil {
		t.Fatalf("DeleteUser() error = %v", err)
	}
	insertQuotaResetTestAdmin(t, store, "admin")

	job, err := store.CreateQuotaResetJob(ctx, 0)
	if err != nil {
		t.Fatalf("CreateQuotaResetJob() error = %v", err)
	}
	if job.Status != QuotaResetJobQueued || job.TotalCount != 2 || job.PendingCount != 2 {
		t.Fatalf("created job = %#v", job)
	}

	got, err := store.QuotaResetJobWithItems(ctx, job.ID)
	if err != nil {
		t.Fatalf("QuotaResetJobWithItems() error = %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("item count = %d, want 2", len(got.Items))
	}
	items := quotaResetItemsByUser(got.Items)
	bound := items[boundID]
	if bound.UpstreamKeyID == nil || *bound.UpstreamKeyID != 101 || bound.KeyMask != compliantSnapshot(101).Mask {
		t.Fatalf("bound item = %#v", bound)
	}
	unbound := items[unboundID]
	if unbound.UpstreamKeyID != nil || unbound.KeyMask != "" || unbound.Status != QuotaResetItemPending {
		t.Fatalf("unbound item = %#v", unbound)
	}
	if _, ok := items[deletedID]; ok {
		t.Fatal("deleted portal user was included in quota-reset snapshot")
	}

	replacement := compliantSnapshot(202)
	if err := store.SetBinding(ctx, boundID, replacement, 0); err != nil {
		t.Fatalf("SetBinding() error = %v", err)
	}
	if err := store.UpdateUser(ctx, boundID, "Renamed User", StatusDisabled, 0); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	got, err = store.QuotaResetJobWithItems(ctx, job.ID)
	if err != nil {
		t.Fatalf("QuotaResetJobWithItems() after concurrent changes error = %v", err)
	}
	bound = quotaResetItemsByUser(got.Items)[boundID]
	if bound.DisplayName != "Bound User" || bound.UserStatus != StatusActive || bound.UpstreamKeyID == nil || *bound.UpstreamKeyID != 101 {
		t.Fatalf("batch snapshot changed with live user/binding: %#v", bound)
	}

	var tableCount, indexCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('quota_reset_jobs','quota_reset_job_items')`).Scan(&tableCount); err != nil {
		t.Fatalf("inspect migrated tables: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='quota_reset_jobs_one_active_idx'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect active-job index: %v", err)
	}
	if tableCount != 2 || indexCount != 1 {
		t.Fatalf("migration objects: tables=%d active_indexes=%d", tableCount, indexCount)
	}
}

func TestQuotaResetTransitionsCountersAndOneActiveJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := openTestStore(t)

	boundID := insertQuotaResetTestUser(t, store, "bound", "Bound", StatusActive, ptrSnapshot(compliantSnapshot(301)))
	unboundID := insertQuotaResetTestUser(t, store, "unbound", "Unbound", StatusDisabled, nil)
	job, err := store.CreateQuotaResetJob(ctx, 0)
	if err != nil {
		t.Fatalf("CreateQuotaResetJob() error = %v", err)
	}
	if _, err := store.CreateQuotaResetJob(ctx, 0); !errors.Is(err, ErrQuotaResetJobActive) {
		t.Fatalf("second CreateQuotaResetJob() error = %v, want ErrQuotaResetJobActive", err)
	}

	current, err := store.CurrentQuotaResetJob(ctx)
	if err != nil || current.ID != job.ID {
		t.Fatalf("CurrentQuotaResetJob() = %#v, %v", current, err)
	}
	items := quotaResetItemsByUser(mustQuotaResetJob(t, store, job.ID).Items)
	bound := items[boundID]
	unbound := items[unboundID]

	if err := store.MarkQuotaResetJobRunning(ctx, job.ID); err != nil {
		t.Fatalf("MarkQuotaResetJobRunning() error = %v", err)
	}
	if err := store.MarkQuotaResetItemRunning(ctx, job.ID, bound.ID); err != nil {
		t.Fatalf("MarkQuotaResetItemRunning() error = %v", err)
	}
	got := mustQuotaResetJob(t, store, job.ID)
	if got.PendingCount != 1 || got.RunningCount != 1 {
		t.Fatalf("running counters = %#v", got)
	}
	if err := store.CompleteQuotaResetItem(ctx, job.ID, bound.ID, QuotaResetItemFailed, "upstream connection timed out"); err == nil {
		t.Fatal("CompleteQuotaResetItem() accepted a raw error instead of a safe code")
	}
	if err := store.CompleteQuotaResetItem(ctx, job.ID, bound.ID, QuotaResetItemSucceeded, ""); err != nil {
		t.Fatalf("complete bound item error = %v", err)
	}
	if err := store.CompleteQuotaResetItem(ctx, job.ID, unbound.ID, QuotaResetItemSkipped, "NO_BINDING"); err != nil {
		t.Fatalf("skip unbound item error = %v", err)
	}
	got = mustQuotaResetJob(t, store, job.ID)
	if got.PendingCount != 0 || got.RunningCount != 0 || got.SucceededCount != 1 || got.SkippedCount != 1 {
		t.Fatalf("terminal counters before finish = %#v", got)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE quota_reset_jobs SET succeeded_count=0, failed_count=2 WHERE id=?`, job.ID); err != nil {
		t.Fatalf("perturb job counters: %v", err)
	}
	finished, err := store.FinishQuotaResetJob(ctx, job.ID, 0)
	if err != nil {
		t.Fatalf("FinishQuotaResetJob() error = %v", err)
	}
	if finished.Status != QuotaResetJobCompleted || finished.SucceededCount != 1 || finished.FailedCount != 0 || finished.SkippedCount != 1 || finished.CompletedAt == nil {
		t.Fatalf("finished job = %#v", finished)
	}

	latest, err := store.CurrentQuotaResetJob(ctx)
	if err != nil || latest.ID != job.ID || latest.Status != QuotaResetJobCompleted {
		t.Fatalf("latest completed job = %#v, %v", latest, err)
	}
	if _, err := store.CreateQuotaResetJob(ctx, 0); err != nil {
		t.Fatalf("CreateQuotaResetJob() after completion error = %v", err)
	}
}

func TestRecoverInterruptedQuotaResetJobsIsTransactionalAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	data, _ := openTestStore(t)

	firstID := insertQuotaResetTestUser(t, data, "recover-running", "Recover Running", StatusActive, ptrSnapshot(compliantSnapshot(351)))
	secondID := insertQuotaResetTestUser(t, data, "recover-terminal", "Recover Terminal", StatusActive, ptrSnapshot(compliantSnapshot(352)))
	thirdID := insertQuotaResetTestUser(t, data, "recover-pending", "Recover Pending", StatusDisabled, nil)
	job, err := data.CreateQuotaResetJob(ctx, 0)
	if err != nil {
		t.Fatalf("CreateQuotaResetJob() error = %v", err)
	}
	items := quotaResetItemsByUser(mustQuotaResetJob(t, data, job.ID).Items)
	if err := data.MarkQuotaResetJobRunning(ctx, job.ID); err != nil {
		t.Fatalf("MarkQuotaResetJobRunning() error = %v", err)
	}
	if err := data.MarkQuotaResetItemRunning(ctx, job.ID, items[firstID].ID); err != nil {
		t.Fatalf("MarkQuotaResetItemRunning() error = %v", err)
	}
	if err := data.CompleteQuotaResetItem(ctx, job.ID, items[secondID].ID, QuotaResetItemFailed, "UPSTREAM_AUTH"); err != nil {
		t.Fatalf("CompleteQuotaResetItem() error = %v", err)
	}

	if err := data.RecoverInterruptedQuotaResetJobs(ctx); err != nil {
		t.Fatalf("RecoverInterruptedQuotaResetJobs() error = %v", err)
	}
	recovered := mustQuotaResetJob(t, data, job.ID)
	if recovered.Status != QuotaResetJobCompleted || recovered.PendingCount != 0 || recovered.RunningCount != 0 ||
		recovered.UnknownCount != 2 || recovered.FailedCount != 1 || recovered.CompletedAt == nil {
		t.Fatalf("recovered running job = %#v", recovered)
	}
	recoveredItems := quotaResetItemsByUser(recovered.Items)
	for _, userID := range []int64{firstID, thirdID} {
		item := recoveredItems[userID]
		if item.Status != QuotaResetItemUnknown || item.ErrorCode != "PROCESS_RESTARTED" || item.CompletedAt == nil {
			t.Fatalf("interrupted item for user %d = %#v", userID, item)
		}
	}
	if item := recoveredItems[secondID]; item.Status != QuotaResetItemFailed || item.ErrorCode != "UPSTREAM_AUTH" {
		t.Fatalf("terminal item was changed by recovery: %#v", item)
	}
	if err := data.RecoverInterruptedQuotaResetJobs(ctx); err != nil {
		t.Fatalf("second recovery error = %v", err)
	}

	queued, err := data.CreateQuotaResetJob(ctx, 0)
	if err != nil {
		t.Fatalf("CreateQuotaResetJob() after recovery error = %v", err)
	}
	if err := data.RecoverInterruptedQuotaResetJobs(ctx); err != nil {
		t.Fatalf("recover queued job error = %v", err)
	}
	queued = mustQuotaResetJob(t, data, queued.ID)
	if queued.Status != QuotaResetJobCompleted || queued.UnknownCount != 3 || queued.PendingCount != 0 || queued.RunningCount != 0 {
		t.Fatalf("recovered queued job = %#v", queued)
	}
	if err := data.RecoverInterruptedQuotaResetJobs(ctx); err != nil {
		t.Fatalf("repeated queued recovery error = %v", err)
	}
	var recoveryAudits int
	if err := data.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action='quota_reset.batch.recovered'`).Scan(&recoveryAudits); err != nil {
		t.Fatalf("count recovery audits: %v", err)
	}
	if recoveryAudits != 2 {
		t.Fatalf("recovery audit count = %d, want 2", recoveryAudits)
	}
}

func TestAbortQuotaResetJobClosesActiveWorkAndPreservesTerminalItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	data, _ := openTestStore(t)
	actorID := insertQuotaResetTestAdmin(t, data, "abort-actor")
	firstID := insertQuotaResetTestUser(t, data, "abort-success", "Abort Success", StatusActive, ptrSnapshot(compliantSnapshot(361)))
	secondID := insertQuotaResetTestUser(t, data, "abort-running", "Abort Running", StatusActive, ptrSnapshot(compliantSnapshot(362)))
	thirdID := insertQuotaResetTestUser(t, data, "abort-pending", "Abort Pending", StatusDisabled, nil)
	job, err := data.CreateQuotaResetJob(ctx, actorID)
	if err != nil {
		t.Fatal(err)
	}
	items := quotaResetItemsByUser(mustQuotaResetJob(t, data, job.ID).Items)
	if err := data.MarkQuotaResetJobRunning(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if err := data.CompleteQuotaResetItem(ctx, job.ID, items[firstID].ID, QuotaResetItemSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := data.MarkQuotaResetItemRunning(ctx, job.ID, items[secondID].ID); err != nil {
		t.Fatal(err)
	}
	if err := data.AbortQuotaResetJob(ctx, job.ID, actorID, "STORE_ERROR"); err != nil {
		t.Fatalf("AbortQuotaResetJob() error = %v", err)
	}
	aborted := mustQuotaResetJob(t, data, job.ID)
	if aborted.Status != QuotaResetJobCompleted || aborted.SucceededCount != 1 || aborted.UnknownCount != 2 ||
		aborted.PendingCount != 0 || aborted.RunningCount != 0 || aborted.CompletedAt == nil {
		t.Fatalf("aborted job = %#v", aborted)
	}
	abortedItems := quotaResetItemsByUser(aborted.Items)
	if item := abortedItems[firstID]; item.Status != QuotaResetItemSucceeded || item.ErrorCode != "" {
		t.Fatalf("terminal item changed during abort: %#v", item)
	}
	for _, userID := range []int64{secondID, thirdID} {
		item := abortedItems[userID]
		if item.Status != QuotaResetItemUnknown || item.ErrorCode != "STORE_ERROR" {
			t.Fatalf("aborted item for user %d = %#v", userID, item)
		}
	}
	if err := data.AbortQuotaResetJob(ctx, job.ID, actorID, "INTERNAL_INTERRUPTION"); err != nil {
		t.Fatalf("idempotent AbortQuotaResetJob() error = %v", err)
	}
	var abortAudits int
	if err := data.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action='quota_reset.batch.abort' AND target_id=?`, fmt.Sprint(job.ID)).Scan(&abortAudits); err != nil {
		t.Fatalf("count abort audits: %v", err)
	}
	if abortAudits != 1 {
		t.Fatalf("abort audit count = %d, want 1", abortAudits)
	}

	queued, err := data.CreateQuotaResetJob(ctx, actorID)
	if err != nil {
		t.Fatalf("active lock remained after abort: %v", err)
	}
	if err := data.AbortQuotaResetJob(ctx, queued.ID, actorID, "raw database error"); err == nil {
		t.Fatal("AbortQuotaResetJob() accepted an unsafe error message")
	}
	if got := mustQuotaResetJob(t, data, queued.ID); got.Status != QuotaResetJobQueued {
		t.Fatalf("invalid abort mutated queued job: %#v", got)
	}
	if err := data.AbortQuotaResetJob(ctx, queued.ID, actorID, "INTERNAL_INTERRUPTION"); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaResetAudits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := openTestStore(t)

	actorID := insertQuotaResetTestAdmin(t, store, "actor")
	userID := insertQuotaResetTestUser(t, store, "target", "Target", StatusActive, ptrSnapshot(compliantSnapshot(401)))
	job, err := store.CreateQuotaResetJob(ctx, actorID)
	if err != nil {
		t.Fatalf("CreateQuotaResetJob() error = %v", err)
	}
	item := mustQuotaResetJob(t, store, job.ID).Items[0]
	if err := store.MarkQuotaResetJobRunning(ctx, job.ID); err != nil {
		t.Fatalf("MarkQuotaResetJobRunning() error = %v", err)
	}
	if err := store.CompleteQuotaResetItem(ctx, job.ID, item.ID, QuotaResetItemUnknown, "UPSTREAM_RESULT_UNKNOWN"); err != nil {
		t.Fatalf("CompleteQuotaResetItem() error = %v", err)
	}
	if _, err := store.FinishQuotaResetJob(ctx, job.ID, actorID); err != nil {
		t.Fatalf("FinishQuotaResetJob() error = %v", err)
	}
	if err := store.RecordSingleQuotaResetAudit(ctx, actorID, userID, 401, QuotaResetItemFailed, "UPSTREAM_REJECTED"); err != nil {
		t.Fatalf("RecordSingleQuotaResetAudit() error = %v", err)
	}

	rows, err := store.db.QueryContext(ctx, `SELECT action, metadata_json FROM audit_events WHERE action LIKE 'quota_reset.%' ORDER BY id`)
	if err != nil {
		t.Fatalf("query quota-reset audits: %v", err)
	}
	defer rows.Close()
	var actions []string
	var metadata []string
	for rows.Next() {
		var action, payload string
		if err := rows.Scan(&action, &payload); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		actions = append(actions, action)
		metadata = append(metadata, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("audit rows: %v", err)
	}
	wantActions := []string{"quota_reset.batch.create", "quota_reset.batch.complete", "quota_reset.single"}
	if strings.Join(actions, ",") != strings.Join(wantActions, ",") {
		t.Fatalf("audit actions = %v, want %v", actions, wantActions)
	}
	joined := strings.Join(metadata, " ")
	for _, value := range []string{`"total_count":1`, `"unknown_count":1`, `"status":"failed"`, `"error_code":"UPSTREAM_REJECTED"`} {
		if !strings.Contains(joined, value) {
			t.Fatalf("audit metadata %q does not contain %q", joined, value)
		}
	}
}

func insertQuotaResetTestUser(t *testing.T, store *Store, username, displayName, status string, snapshot *KeySnapshot) int64 {
	t.Helper()
	ctx := context.Background()
	now := nowUnix()
	result, err := store.db.ExecContext(ctx, `INSERT INTO users (username, display_name, password_hash, role, status, created_at, updated_at) VALUES (?, ?, 'hash', 'user', ?, ?, ?)`, username, displayName, status, now, now)
	if err != nil {
		t.Fatalf("insert portal user: %v", err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("portal user id: %v", err)
	}
	if snapshot != nil {
		if err := insertBinding(ctx, store.db, userID, *snapshot, now); err != nil {
			t.Fatalf("insert portal user binding: %v", err)
		}
	}
	return userID
}

func insertQuotaResetTestAdmin(t *testing.T, store *Store, username string) int64 {
	t.Helper()
	now := nowUnix()
	result, err := store.db.Exec(`INSERT INTO users (username, display_name, password_hash, role, status, created_at, updated_at) VALUES (?, 'Administrator', 'hash', 'admin', 'active', ?, ?)`, username, now, now)
	if err != nil {
		t.Fatalf("insert administrator: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("administrator id: %v", err)
	}
	return id
}

func ptrSnapshot(snapshot KeySnapshot) *KeySnapshot { return &snapshot }

func quotaResetItemsByUser(items []QuotaResetJobItem) map[int64]QuotaResetJobItem {
	byUser := make(map[int64]QuotaResetJobItem, len(items))
	for _, item := range items {
		byUser[item.UserID] = item
	}
	return byUser
}

func mustQuotaResetJob(t *testing.T, store *Store, jobID int64) QuotaResetJob {
	t.Helper()
	job, err := store.QuotaResetJobWithItems(context.Background(), jobID)
	if err != nil {
		t.Fatalf("QuotaResetJobWithItems(%d) error = %v", jobID, err)
	}
	return job
}
