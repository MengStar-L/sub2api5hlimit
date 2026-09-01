package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const quotaResetJobColumns = `id, status, total_count, pending_count, running_count,
 succeeded_count, failed_count, unknown_count, skipped_count, requested_by_user_id,
 created_at, started_at, completed_at`

const quotaResetItemColumns = `id, job_id, user_id, username, display_name, user_status,
 upstream_key_id, key_mask, status, error_code, created_at, updated_at, started_at, completed_at`

type quotaResetCounters struct {
	Total     int `json:"total_count"`
	Pending   int `json:"pending_count"`
	Running   int `json:"running_count"`
	Succeeded int `json:"succeeded_count"`
	Failed    int `json:"failed_count"`
	Unknown   int `json:"unknown_count"`
	Skipped   int `json:"skipped_count"`
}

func scanQuotaResetJob(scanner interface{ Scan(...any) error }) (QuotaResetJob, error) {
	var out QuotaResetJob
	var requestedBy, started, completed sql.NullInt64
	err := scanner.Scan(
		&out.ID, &out.Status, &out.TotalCount, &out.PendingCount, &out.RunningCount,
		&out.SucceededCount, &out.FailedCount, &out.UnknownCount, &out.SkippedCount,
		&requestedBy, &out.CreatedAt, &started, &completed,
	)
	out.RequestedByUserID = intPtr(requestedBy)
	out.StartedAt = intPtr(started)
	out.CompletedAt = intPtr(completed)
	return out, err
}

func scanQuotaResetItem(scanner interface{ Scan(...any) error }) (QuotaResetJobItem, error) {
	var out QuotaResetJobItem
	var upstreamKeyID, started, completed sql.NullInt64
	err := scanner.Scan(
		&out.ID, &out.JobID, &out.UserID, &out.Username, &out.DisplayName, &out.UserStatus,
		&upstreamKeyID, &out.KeyMask, &out.Status, &out.ErrorCode, &out.CreatedAt, &out.UpdatedAt,
		&started, &completed,
	)
	out.UpstreamKeyID = intPtr(upstreamKeyID)
	out.StartedAt = intPtr(started)
	out.CompletedAt = intPtr(completed)
	return out, err
}

func (s *Store) CreateQuotaResetJob(ctx context.Context, actorID int64) (QuotaResetJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QuotaResetJob{}, err
	}
	defer tx.Rollback()

	now := nowUnix()
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO quota_reset_jobs (
        status, requested_by_user_id, created_at, updated_at
      ) VALUES ('queued', ?, ?, ?)`, actor, now, now)
	if err != nil {
		if isQuotaResetActiveConstraint(err) {
			return QuotaResetJob{}, ErrQuotaResetJobActive
		}
		return QuotaResetJob{}, err
	}
	jobID, err := result.LastInsertId()
	if err != nil {
		return QuotaResetJob{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO quota_reset_job_items (
        job_id, user_id, username, display_name, user_status, upstream_key_id, key_mask,
        status, created_at, updated_at
      )
      SELECT ?, u.id, u.username, u.display_name, u.status, b.upstream_key_id,
             COALESCE(b.key_mask, ''), 'pending', ?, ?
        FROM users u
        LEFT JOIN key_bindings b ON b.user_id = u.id
       WHERE u.role = 'user' AND u.status IN ('active', 'disabled')
       ORDER BY u.id`, jobID, now, now); err != nil {
		return QuotaResetJob{}, err
	}

	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_reset_job_items WHERE job_id=?`, jobID).Scan(&total); err != nil {
		return QuotaResetJob{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quota_reset_jobs SET total_count=?, pending_count=? WHERE id=?`, total, total, jobID); err != nil {
		return QuotaResetJob{}, err
	}
	metadata, err := json.Marshal(map[string]int{"total_count": total})
	if err != nil {
		return QuotaResetJob{}, err
	}
	if err := addAudit(ctx, tx, actorID, "quota_reset.batch.create", "quota_reset_job", fmt.Sprint(jobID), string(metadata)); err != nil {
		return QuotaResetJob{}, err
	}
	if err := tx.Commit(); err != nil {
		if isQuotaResetActiveConstraint(err) {
			return QuotaResetJob{}, ErrQuotaResetJobActive
		}
		return QuotaResetJob{}, err
	}
	return s.quotaResetJobByID(ctx, jobID)
}

func (s *Store) CurrentQuotaResetJob(ctx context.Context) (QuotaResetJob, error) {
	job, err := scanQuotaResetJob(s.db.QueryRowContext(ctx, `SELECT `+quotaResetJobColumns+`
      FROM quota_reset_jobs
     ORDER BY CASE WHEN status IN ('queued', 'running') THEN 0 ELSE 1 END,
              created_at DESC, id DESC
     LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return QuotaResetJob{}, ErrNotFound
	}
	return job, err
}

// RecoverInterruptedQuotaResetJobs closes work left active by a previous
// process. Reset requests are intentionally never replayed because a timeout or
// process exit can happen after Sub2API applied the mutation.
func (s *Store) RecoverInterruptedQuotaResetJobs(ctx context.Context) error {
	return s.recoverInterruptedQuotaResetJobs(ctx, 0, "PROCESS_RESTARTED")
}

func (s *Store) recoverInterruptedQuotaResetJobs(ctx context.Context, actorID int64, safeErrorCode string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM quota_reset_jobs
      WHERE status IN ('queued', 'running') ORDER BY id`)
	if err != nil {
		return err
	}
	var jobIDs []int64
	for rows.Next() {
		var jobID int64
		if err := rows.Scan(&jobID); err != nil {
			rows.Close()
			return err
		}
		jobIDs = append(jobIDs, jobID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	now := nowUnix()
	for _, jobID := range jobIDs {
		if _, err := abortQuotaResetJobTx(ctx, tx, jobID, actorID, safeErrorCode, "quota_reset.batch.recovered", now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AbortQuotaResetJob terminates an active job after an internal orchestration
// failure. Terminal items are retained; work whose upstream outcome is not
// known is marked unknown and will never be replayed automatically.
func (s *Store) AbortQuotaResetJob(ctx context.Context, jobID, actorID int64, safeErrorCode string) error {
	if safeErrorCode == "" || !isSafeQuotaResetErrorCode(safeErrorCode) {
		return errors.New("quota reset abort code must be a non-empty uppercase code token")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := abortQuotaResetJobTx(ctx, tx, jobID, actorID, safeErrorCode, "quota_reset.batch.abort", nowUnix()); err != nil {
		return err
	}
	return tx.Commit()
}

func abortQuotaResetJobTx(ctx context.Context, tx *sql.Tx, jobID, actorID int64, safeErrorCode, auditAction string, now int64) (bool, error) {
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM quota_reset_jobs WHERE id=?`, jobID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, err
	}
	if status == QuotaResetJobCompleted {
		return false, nil
	}
	if status != QuotaResetJobQueued && status != QuotaResetJobRunning {
		return false, ErrQuotaResetTransition
	}
	if _, err := tx.ExecContext(ctx, `UPDATE quota_reset_job_items
      SET status='unknown', error_code=?, completed_at=?, updated_at=?
      WHERE job_id=? AND status IN ('pending', 'running')`, safeErrorCode, now, now, jobID); err != nil {
		return false, err
	}
	counters, err := recomputeQuotaResetCounters(ctx, tx, jobID, now)
	if err != nil {
		return false, err
	}
	if counters.Pending != 0 || counters.Running != 0 {
		return false, ErrQuotaResetTransition
	}
	result, err := tx.ExecContext(ctx, `UPDATE quota_reset_jobs
      SET status='completed', completed_at=?, updated_at=?
      WHERE id=? AND status IN ('queued', 'running')`, now, now, jobID)
	if err != nil {
		return false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return false, err
	} else if affected != 1 {
		return false, ErrQuotaResetTransition
	}
	metadata, err := json.Marshal(struct {
		quotaResetCounters
		ErrorCode string `json:"error_code"`
	}{counters, safeErrorCode})
	if err != nil {
		return false, err
	}
	if err := addAudit(ctx, tx, actorID, auditAction, "quota_reset_job", fmt.Sprint(jobID), string(metadata)); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) QuotaResetJobWithItems(ctx context.Context, jobID int64) (QuotaResetJob, error) {
	job, err := s.quotaResetJobByID(ctx, jobID)
	if err != nil {
		return QuotaResetJob{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+quotaResetItemColumns+`
      FROM quota_reset_job_items WHERE job_id=? ORDER BY id`, jobID)
	if err != nil {
		return QuotaResetJob{}, err
	}
	defer rows.Close()
	job.Items = make([]QuotaResetJobItem, 0, job.TotalCount)
	for rows.Next() {
		item, err := scanQuotaResetItem(rows)
		if err != nil {
			return QuotaResetJob{}, err
		}
		job.Items = append(job.Items, item)
	}
	if err := rows.Err(); err != nil {
		return QuotaResetJob{}, err
	}
	return job, nil
}

func (s *Store) MarkQuotaResetJobRunning(ctx context.Context, jobID int64) error {
	now := nowUnix()
	result, err := s.db.ExecContext(ctx, `UPDATE quota_reset_jobs
      SET status='running', started_at=?, updated_at=?
      WHERE id=? AND status='queued'`, now, now, jobID)
	if err != nil {
		return err
	}
	return s.quotaResetTransitionResult(ctx, result, jobID, "quota_reset_jobs")
}

func (s *Store) MarkQuotaResetItemRunning(ctx context.Context, jobID, itemID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUnix()
	result, err := tx.ExecContext(ctx, `UPDATE quota_reset_job_items
      SET status='running', started_at=?, error_code='', updated_at=?
      WHERE id=? AND job_id=? AND status='pending'
        AND EXISTS (SELECT 1 FROM quota_reset_jobs WHERE id=? AND status='running')`, now, now, itemID, jobID, jobID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return quotaResetItemTransitionError(ctx, tx, jobID, itemID)
	}
	if _, err := recomputeQuotaResetCounters(ctx, tx, jobID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CompleteQuotaResetItem(ctx context.Context, jobID, itemID int64, terminalStatus, safeErrorCode string) error {
	if !isQuotaResetTerminalStatus(terminalStatus) {
		return ErrQuotaResetTransition
	}
	if !isSafeQuotaResetErrorCode(safeErrorCode) {
		return errors.New("quota reset error code must be an uppercase code token")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUnix()
	result, err := tx.ExecContext(ctx, `UPDATE quota_reset_job_items
      SET status=?, error_code=?, completed_at=?, updated_at=?
      WHERE id=? AND job_id=? AND status IN ('pending', 'running')
        AND EXISTS (SELECT 1 FROM quota_reset_jobs WHERE id=? AND status='running')`,
		terminalStatus, safeErrorCode, now, now, itemID, jobID, jobID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return quotaResetItemTransitionError(ctx, tx, jobID, itemID)
	}
	if _, err := recomputeQuotaResetCounters(ctx, tx, jobID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishQuotaResetJob(ctx context.Context, jobID, actorID int64) (QuotaResetJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QuotaResetJob{}, err
	}
	defer tx.Rollback()

	var status string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM quota_reset_jobs WHERE id=?`, jobID).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QuotaResetJob{}, ErrNotFound
		}
		return QuotaResetJob{}, err
	}
	if status != QuotaResetJobRunning {
		return QuotaResetJob{}, ErrQuotaResetTransition
	}
	now := nowUnix()
	counters, err := recomputeQuotaResetCounters(ctx, tx, jobID, now)
	if err != nil {
		return QuotaResetJob{}, err
	}
	if counters.Pending != 0 || counters.Running != 0 {
		return QuotaResetJob{}, ErrQuotaResetTransition
	}
	result, err := tx.ExecContext(ctx, `UPDATE quota_reset_jobs
      SET status='completed', completed_at=?, updated_at=?
      WHERE id=? AND status='running'`, now, now, jobID)
	if err != nil {
		return QuotaResetJob{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return QuotaResetJob{}, err
	} else if affected == 0 {
		return QuotaResetJob{}, ErrQuotaResetTransition
	}
	metadata, err := json.Marshal(counters)
	if err != nil {
		return QuotaResetJob{}, err
	}
	if err := addAudit(ctx, tx, actorID, "quota_reset.batch.complete", "quota_reset_job", fmt.Sprint(jobID), string(metadata)); err != nil {
		return QuotaResetJob{}, err
	}
	if err := tx.Commit(); err != nil {
		return QuotaResetJob{}, err
	}
	return s.quotaResetJobByID(ctx, jobID)
}

func (s *Store) RecordSingleQuotaResetAudit(ctx context.Context, actorID, userID, upstreamKeyID int64, terminalStatus, safeErrorCode string) error {
	if !isQuotaResetTerminalStatus(terminalStatus) {
		return ErrQuotaResetTransition
	}
	if !isSafeQuotaResetErrorCode(safeErrorCode) {
		return errors.New("quota reset error code must be an uppercase code token")
	}
	metadata, err := json.Marshal(struct {
		UpstreamKeyID int64  `json:"upstream_key_id"`
		Status        string `json:"status"`
		ErrorCode     string `json:"error_code,omitempty"`
	}{upstreamKeyID, terminalStatus, safeErrorCode})
	if err != nil {
		return err
	}
	return addAudit(ctx, s.db, actorID, "quota_reset.single", "user", fmt.Sprint(userID), string(metadata))
}

func (s *Store) quotaResetJobByID(ctx context.Context, jobID int64) (QuotaResetJob, error) {
	job, err := scanQuotaResetJob(s.db.QueryRowContext(ctx, `SELECT `+quotaResetJobColumns+` FROM quota_reset_jobs WHERE id=?`, jobID))
	if errors.Is(err, sql.ErrNoRows) {
		return QuotaResetJob{}, ErrNotFound
	}
	return job, err
}

func (s *Store) quotaResetTransitionResult(ctx context.Context, result sql.Result, id int64, table string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=?)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrQuotaResetTransition
}

func quotaResetItemTransitionError(ctx context.Context, tx *sql.Tx, jobID, itemID int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
      SELECT 1 FROM quota_reset_job_items WHERE id=? AND job_id=?
    )`, itemID, jobID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrQuotaResetTransition
}

func recomputeQuotaResetCounters(ctx context.Context, tx *sql.Tx, jobID, now int64) (quotaResetCounters, error) {
	var out quotaResetCounters
	err := tx.QueryRowContext(ctx, `SELECT
        COUNT(*),
        COALESCE(SUM(status='pending'), 0),
        COALESCE(SUM(status='running'), 0),
        COALESCE(SUM(status='succeeded'), 0),
        COALESCE(SUM(status='failed'), 0),
        COALESCE(SUM(status='unknown'), 0),
        COALESCE(SUM(status='skipped'), 0)
      FROM quota_reset_job_items WHERE job_id=?`, jobID).Scan(
		&out.Total, &out.Pending, &out.Running, &out.Succeeded, &out.Failed, &out.Unknown, &out.Skipped,
	)
	if err != nil {
		return quotaResetCounters{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE quota_reset_jobs SET
        total_count=?, pending_count=?, running_count=?, succeeded_count=?,
        failed_count=?, unknown_count=?, skipped_count=?, updated_at=?
      WHERE id=?`, out.Total, out.Pending, out.Running, out.Succeeded, out.Failed,
		out.Unknown, out.Skipped, now, jobID)
	if err != nil {
		return quotaResetCounters{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return quotaResetCounters{}, err
	} else if affected == 0 {
		return quotaResetCounters{}, ErrNotFound
	}
	return out, nil
}

func isQuotaResetTerminalStatus(status string) bool {
	switch status {
	case QuotaResetItemSucceeded, QuotaResetItemFailed, QuotaResetItemUnknown, QuotaResetItemSkipped:
		return true
	default:
		return false
	}
}

func isSafeQuotaResetErrorCode(code string) bool {
	if code == "" {
		return true
	}
	if len(code) > 64 || code[0] < 'A' || code[0] > 'Z' {
		return false
	}
	for index := 1; index < len(code); index++ {
		char := code[index]
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func isQuotaResetActiveConstraint(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") && strings.Contains(message, "quota_reset_jobs")
}
