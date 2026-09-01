package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/sub2api"
)

const quotaResetTimeout = 15 * time.Second

type quotaResetResponse struct {
	UserID          int64          `json:"user_id"`
	UpstreamKeyID   int64          `json:"upstream_key_id"`
	Status          string         `json:"status"`
	SnapshotUpdated bool           `json:"snapshot_updated"`
	WarningCode     string         `json:"warning_code,omitempty"`
	FiveHour        *keyWindowView `json:"window_5h"`
	SevenDay        *keyWindowView `json:"window_7d"`
	Snapshot        snapshotMeta   `json:"snapshot"`
}

type createQuotaResetRequest struct {
	Scope string `json:"scope"`
}

func resetTerminalState(result QuotaResetResult, err error) (string, string) {
	if result.Applied {
		return store.QuotaResetItemSucceeded, ""
	}
	if err == nil {
		return store.QuotaResetItemFailed, "UPSTREAM_ERROR"
	}
	if errors.Is(err, context.Canceled) {
		return store.QuotaResetItemUnknown, "UPSTREAM_CANCELED"
	}
	if resetTimedOut(err) {
		return store.QuotaResetItemUnknown, "UPSTREAM_TIMEOUT"
	}
	var upstream *sub2api.UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return store.QuotaResetItemFailed, "UPSTREAM_AUTH"
		case http.StatusNotFound:
			return store.QuotaResetItemFailed, "UPSTREAM_KEY_MISSING"
		case http.StatusTooManyRequests:
			return store.QuotaResetItemFailed, "UPSTREAM_RATE_LIMITED"
		default:
			if upstream.StatusCode >= 500 {
				return store.QuotaResetItemFailed, "UPSTREAM_UNAVAILABLE"
			}
		}
	}
	return store.QuotaResetItemFailed, "UPSTREAM_ERROR"
}

func resetTimedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

func (s *Server) resetUserQuota(w http.ResponseWriter, r *http.Request) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.updates != nil {
		view, err := s.updates.View(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "UPDATE_STATE_ERROR", "无法确认程序更新状态")
			return
		}
		if view.Operation != nil && view.Operation.MaintenancePending() {
			writeError(w, http.StatusConflict, "UPDATE_IN_PROGRESS", "程序更新期间不能重置额度")
			return
		}
	}
	userID, ok := pathID(w, r)
	if !ok {
		return
	}
	s.bindingMu.RLock()
	bindingLocked := true
	defer func() {
		if bindingLocked {
			s.bindingMu.RUnlock()
		}
	}()
	user, err := s.store.UserByID(r.Context(), userID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !userResettable(user) {
		writeError(w, http.StatusConflict, "QUOTA_RESET_NOT_AVAILABLE", "该用户当前没有仍存在于上游的 Key 绑定")
		return
	}
	if job, jobErr := s.store.CurrentQuotaResetJob(r.Context()); jobErr == nil &&
		(job.Status == store.QuotaResetJobQueued || job.Status == store.QuotaResetJobRunning) {
		writeError(w, http.StatusConflict, "QUOTA_RESET_BATCH_ACTIVE", "批量重置任务执行期间不能单独重置用户")
		return
	} else if jobErr != nil && !errors.Is(jobErr, store.ErrNotFound) {
		writeStoreError(w, jobErr)
		return
	}
	actorID := currentSession(r).User.ID
	keyID := user.Binding.UpstreamKeyID
	if !s.beginQuotaReset(keyID) {
		writeError(w, http.StatusConflict, "QUOTA_RESET_IN_PROGRESS", "该 Key 正在执行额度重置")
		return
	}
	defer s.endQuotaReset(keyID)
	ctx, cancel := context.WithTimeout(r.Context(), quotaResetTimeout)
	result, resetErr := s.upstream.ResetQuota(ctx, keyID)
	cancel()
	s.bindingMu.RUnlock()
	bindingLocked = false
	status, errorCode := resetTerminalState(result, resetErr)
	snapshotRefreshFailed := false
	if status == store.QuotaResetItemSucceeded {
		syncCtx, syncCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		syncErr := s.upstream.Sync(syncCtx, "keys")
		syncCancel()
		// The response flag reports whether full reconciliation completed. A
		// confirmed reset may already have a safe zero-usage snapshot persisted
		// by ResetQuota even when this complete sync fails.
		result.SnapshotUpdated = syncErr == nil
		if syncErr != nil {
			snapshotRefreshFailed = true
			errorCode = "SNAPSHOT_REFRESH_FAILED"
			s.log.Warn("quota reset applied but snapshot refresh failed", "user_id", userID, "error", secure.Redact(syncErr.Error()))
		}
		if resetErr != nil {
			s.log.Warn("quota reset reported applied with an additional error", "user_id", userID, "error", secure.Redact(resetErr.Error()))
		}
	}
	if auditErr := s.store.RecordSingleQuotaResetAudit(context.WithoutCancel(r.Context()), actorID, userID, keyID, status, errorCode); auditErr != nil {
		s.log.Error("record single quota reset audit", "user_id", userID, "error", secure.Redact(auditErr.Error()))
	}
	if status == store.QuotaResetItemUnknown {
		writeError(w, http.StatusGatewayTimeout, "QUOTA_RESET_UNKNOWN", "未能确认上游重置结果；系统不会自动重试")
		return
	}
	if status == store.QuotaResetItemFailed {
		s.log.Warn("upstream quota reset failed", "user_id", userID, "code", errorCode, "error", secure.Redact(errorString(resetErr)))
		writeError(w, http.StatusBadGateway, errorCode, "上游额度重置失败")
		return
	}
	response := quotaResetResponse{
		UserID: userID, UpstreamKeyID: keyID, Status: status, SnapshotUpdated: result.SnapshotUpdated,
		Snapshot: snapshotMeta{AsOf: time.Now().Unix(), Stale: true},
	}
	if refreshed, getErr := s.store.UserByID(r.Context(), userID); getErr == nil && refreshed.Binding != nil && refreshed.Binding.UpstreamKeyID == keyID {
		view := newAdminUserView(refreshed, time.Now().Unix())
		response.FiveHour = view.FiveHour
		response.SevenDay = view.SevenDay
		response.Snapshot = view.Snapshot
	}
	if snapshotRefreshFailed {
		response.WarningCode = "SNAPSHOT_REFRESH_FAILED"
		response.Snapshot.Stale = true
	}
	writeData(w, http.StatusOK, response)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) createQuotaResetJob(w http.ResponseWriter, r *http.Request) {
	var request createQuotaResetRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Scope != "all_non_deleted" {
		writeError(w, http.StatusBadRequest, "INVALID_RESET_SCOPE", "批量重置范围必须为 all_non_deleted")
		return
	}
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.updates != nil {
		view, err := s.updates.View(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "UPDATE_STATE_ERROR", "无法确认程序更新状态")
			return
		}
		if view.Operation != nil && view.Operation.MaintenancePending() {
			writeError(w, http.StatusConflict, "UPDATE_IN_PROGRESS", "程序更新期间不能启动额度重置")
			return
		}
	}
	actorID := currentSession(r).User.ID
	job, err := s.store.CreateQuotaResetJob(r.Context(), actorID)
	if err != nil {
		if errors.Is(err, store.ErrQuotaResetJobActive) {
			writeError(w, http.StatusConflict, "QUOTA_RESET_ALREADY_RUNNING", "已有批量重置任务正在执行")
			return
		}
		writeStoreError(w, err)
		return
	}
	go s.runQuotaResetJob(job.ID, actorID)
	writeData(w, http.StatusAccepted, struct {
		store.QuotaResetJob
		JobID int64 `json:"job_id"`
	}{QuotaResetJob: job, JobID: job.ID})
}

func (s *Server) currentQuotaResetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.CurrentQuotaResetJob(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, job)
}

func (s *Server) quotaResetJob(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || jobID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "无效的任务 ID")
		return
	}
	job, err := s.store.QuotaResetJobWithItems(r.Context(), jobID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, job)
}

func (s *Server) runQuotaResetJob(jobID, actorID int64) {
	ctx := context.Background()
	if err := s.store.MarkQuotaResetJobRunning(ctx, jobID); err != nil {
		s.abortQuotaResetJob(jobID, actorID, "STORE_ERROR", fmt.Errorf("mark job running: %w", err))
		return
	}
	job, err := s.store.QuotaResetJobWithItems(ctx, jobID)
	if err != nil {
		s.abortQuotaResetJob(jobID, actorID, "STORE_ERROR", fmt.Errorf("load job items: %w", err))
		return
	}
	jobCtx, cancelJob := context.WithCancel(context.Background())
	defer cancelJob()
	items := make(chan store.QuotaResetJobItem, len(job.Items))
	for _, item := range job.Items {
		items <- item
	}
	close(items)
	var workers sync.WaitGroup
	var fatalOnce sync.Once
	var fatalErr error
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range items {
				if jobCtx.Err() != nil {
					continue
				}
				if err := s.runQuotaResetItem(jobCtx, jobID, item); err != nil {
					fatalOnce.Do(func() {
						fatalErr = err
						cancelJob()
					})
				}
			}
		}()
	}
	workers.Wait()
	if fatalErr != nil {
		s.abortQuotaResetJob(jobID, actorID, "STORE_ERROR", fatalErr)
		return
	}
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := s.upstream.Sync(syncCtx, "keys"); err != nil {
		s.log.Warn("final quota reset snapshot sync failed", "job_id", jobID, "error", secure.Redact(err.Error()))
	}
	cancel()
	if _, err := s.store.FinishQuotaResetJob(ctx, jobID, actorID); err != nil {
		s.abortQuotaResetJob(jobID, actorID, "INTERNAL_INTERRUPTION", fmt.Errorf("finish job: %w", err))
	}
}

func (s *Server) runQuotaResetItem(ctx context.Context, jobID int64, item store.QuotaResetJobItem) error {
	if err := s.store.MarkQuotaResetItemRunning(ctx, jobID, item.ID); err != nil {
		return fmt.Errorf("mark item %d running: %w", item.ID, err)
	}
	s.bindingMu.RLock()
	status, code, err := s.validateQuotaResetItem(ctx, item)
	if err != nil {
		s.bindingMu.RUnlock()
		return fmt.Errorf("validate item %d: %w", item.ID, err)
	}
	if status != "" {
		s.bindingMu.RUnlock()
		return s.completeQuotaResetItem(jobID, item.ID, status, code)
	}
	if !s.beginQuotaReset(*item.UpstreamKeyID) {
		s.bindingMu.RUnlock()
		return s.completeQuotaResetItem(jobID, item.ID, store.QuotaResetItemSkipped, "RESET_IN_PROGRESS")
	}
	resetCtx, cancel := context.WithTimeout(ctx, quotaResetTimeout)
	result, err := s.upstream.ResetQuota(resetCtx, *item.UpstreamKeyID)
	cancel()
	s.endQuotaReset(*item.UpstreamKeyID)
	s.bindingMu.RUnlock()
	status, code = resetTerminalState(result, err)
	if result.Applied && err != nil {
		s.log.Warn("batch quota reset reported applied with an additional error", "job_id", jobID, "item_id", item.ID, "error", secure.Redact(err.Error()))
	}
	return s.completeQuotaResetItem(jobID, item.ID, status, code)
}

func (s *Server) beginQuotaReset(keyID int64) bool {
	s.quotaResetMu.Lock()
	defer s.quotaResetMu.Unlock()
	if _, exists := s.quotaResetKeys[keyID]; exists {
		return false
	}
	s.quotaResetKeys[keyID] = struct{}{}
	return true
}

func (s *Server) endQuotaReset(keyID int64) {
	s.quotaResetMu.Lock()
	delete(s.quotaResetKeys, keyID)
	s.quotaResetMu.Unlock()
}

func (s *Server) validateQuotaResetItem(ctx context.Context, item store.QuotaResetJobItem) (string, string, error) {
	user, err := s.store.UserByID(ctx, item.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && user.Status == store.StatusDeleted) {
		return store.QuotaResetItemSkipped, "USER_DELETED", nil
	}
	if err != nil {
		return "", "", err
	}
	if item.UpstreamKeyID == nil || user.Binding == nil {
		return store.QuotaResetItemSkipped, "USER_UNBOUND", nil
	}
	if user.Binding.UpstreamKeyID != *item.UpstreamKeyID {
		return store.QuotaResetItemSkipped, "BINDING_CHANGED", nil
	}
	if !userResettable(user) {
		return store.QuotaResetItemSkipped, "BINDING_NOT_RESETTABLE", nil
	}
	return "", "", nil
}

func (s *Server) completeQuotaResetItem(jobID, itemID int64, status, code string) error {
	if err := s.store.CompleteQuotaResetItem(context.Background(), jobID, itemID, status, code); err != nil {
		return fmt.Errorf("complete item %d: %w", itemID, err)
	}
	return nil
}

func (s *Server) abortQuotaResetJob(jobID, actorID int64, code string, cause error) {
	s.log.Error("abort quota reset job", "job_id", jobID, "code", code, "error", secure.Redact(errorString(cause)))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.store.AbortQuotaResetJob(ctx, jobID, actorID, code); err != nil {
		s.log.Error("persist quota reset abort", "job_id", jobID, "code", code, "error", secure.Redact(err.Error()))
	}
}
