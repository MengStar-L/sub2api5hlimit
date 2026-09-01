package httpapi

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/sub2api"
)

const keyLimitTimeout = 15 * time.Second

// maxLimitUSD mirrors the client-side bound so an obviously mistyped limit is
// rejected before any upstream call is made.
const maxLimitUSD = 1_000_000

type keyLimitRequest struct {
	RateLimit5h *float64 `json:"rate_limit_5h"`
	RateLimit7d *float64 `json:"rate_limit_7d"`
}

type keyLimitResponse struct {
	UserID          int64          `json:"user_id"`
	UpstreamKeyID   int64          `json:"upstream_key_id"`
	SnapshotUpdated bool           `json:"snapshot_updated"`
	WarningCode     string         `json:"warning_code,omitempty"`
	FiveHour        *keyWindowView `json:"window_5h"`
	SevenDay        *keyWindowView `json:"window_7d"`
	Snapshot        snapshotMeta   `json:"snapshot"`
}

// limitEditable reuses the reset precondition: the binding must still point at
// a key that exists upstream. A key the portal can no longer see must not have
// its limits written blindly.
func limitEditable(user store.User) bool { return userResettable(user) }

func validLimitValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value > 0 && value <= maxLimitUSD
}

// limitFailureCode maps an upstream limit-change error to a stable code. A
// SchemaError means the upstream answered 200 without applying the change,
// which must never be reported to the admin as success.
func limitFailureCode(err error) (int, string, string) {
	var schema *sub2api.SchemaError
	if errors.As(err, &schema) {
		return http.StatusBadGateway, "LIMIT_NOT_APPLIED", "上游返回成功但未真正写入新限额，请确认 Sub2API 版本支持修改限额"
	}
	if resetTimedOut(err) {
		return http.StatusGatewayTimeout, "LIMIT_UPDATE_UNKNOWN", "未能确认上游是否已写入新限额；请刷新后核对，系统不会自动重试"
	}
	var upstream *sub2api.UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusBadGateway, "UPSTREAM_AUTH", "上游拒绝了本次限额修改（凭据无权限）"
		case http.StatusNotFound:
			return http.StatusBadGateway, "UPSTREAM_KEY_MISSING", "上游已不存在该 Key"
		case http.StatusTooManyRequests:
			return http.StatusBadGateway, "UPSTREAM_RATE_LIMITED", "上游限流，请稍后重试"
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return http.StatusBadGateway, "UPSTREAM_REJECTED_LIMIT", "上游拒绝了该限额值"
		default:
			if upstream.StatusCode >= 500 {
				return http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "上游暂时不可用"
			}
		}
	}
	return http.StatusBadGateway, "UPSTREAM_ERROR", "上游限额修改失败"
}

// setUserKeyLimits writes new native 5h and 7d limits to the bound upstream key.
// Both windows must stay positive, which is the portal's binding precondition.
func (s *Server) setUserKeyLimits(w http.ResponseWriter, r *http.Request) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if s.updates != nil {
		view, err := s.updates.View(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "UPDATE_STATE_ERROR", "无法确认程序更新状态")
			return
		}
		if view.Operation != nil && view.Operation.MaintenancePending() {
			writeError(w, http.StatusConflict, "UPDATE_IN_PROGRESS", "程序更新期间不能修改限额")
			return
		}
	}
	userID, ok := pathID(w, r)
	if !ok {
		return
	}
	var request keyLimitRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.RateLimit5h == nil || request.RateLimit7d == nil {
		writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "必须同时提供 5h 与 7d 限额")
		return
	}
	if !validLimitValue(*request.RateLimit5h) || !validLimitValue(*request.RateLimit7d) {
		writeError(w, http.StatusBadRequest, "INVALID_LIMIT", "5h 与 7d 限额必须是大于 0 且不超过 1000000 的数值")
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
	if !limitEditable(user) {
		writeError(w, http.StatusConflict, "LIMIT_EDIT_NOT_AVAILABLE", "该用户当前没有仍存在于上游的 Key 绑定")
		return
	}
	if job, jobErr := s.store.CurrentQuotaResetJob(r.Context()); jobErr == nil &&
		(job.Status == store.QuotaResetJobQueued || job.Status == store.QuotaResetJobRunning) {
		writeError(w, http.StatusConflict, "QUOTA_RESET_BATCH_ACTIVE", "批量重置任务执行期间不能修改限额")
		return
	} else if jobErr != nil && !errors.Is(jobErr, store.ErrNotFound) {
		writeStoreError(w, jobErr)
		return
	}
	actorID := currentSession(r).User.ID
	keyID := user.Binding.UpstreamKeyID
	// Reuse the per-key reset gate so a limit change and a quota reset can never
	// hit the same upstream key concurrently.
	if !s.beginQuotaReset(keyID) {
		writeError(w, http.StatusConflict, "KEY_BUSY", "该 Key 正在执行额度重置或限额修改")
		return
	}
	defer s.endQuotaReset(keyID)
	ctx, cancel := context.WithTimeout(r.Context(), keyLimitTimeout)
	result, limitErr := s.upstream.SetKeyLimits(ctx, keyID, *request.RateLimit5h, *request.RateLimit7d, actorID)
	cancel()
	s.bindingMu.RUnlock()
	bindingLocked = false
	if limitErr != nil || !result.Applied {
		status, code, message := limitFailureCode(limitErr)
		s.log.Warn("upstream limit change failed", "user_id", userID, "code", code,
			"error", secure.Redact(errorString(limitErr)))
		writeError(w, status, code, message)
		return
	}
	warningCode := ""
	syncCtx, syncCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	syncErr := s.upstream.Sync(syncCtx, "keys")
	syncCancel()
	// SetKeyLimits already persisted the confirmed limits, so a failed
	// reconciliation only means the remaining key metadata may lag.
	if syncErr != nil {
		warningCode = "SNAPSHOT_REFRESH_FAILED"
		s.log.Warn("limit change applied but snapshot refresh failed", "user_id", userID,
			"error", secure.Redact(syncErr.Error()))
	}
	response := keyLimitResponse{
		UserID: userID, UpstreamKeyID: keyID, SnapshotUpdated: syncErr == nil, WarningCode: warningCode,
		Snapshot: snapshotMeta{AsOf: time.Now().Unix(), Stale: true},
	}
	if refreshed, getErr := s.store.UserByID(r.Context(), userID); getErr == nil &&
		refreshed.Binding != nil && refreshed.Binding.UpstreamKeyID == keyID {
		view := newAdminUserView(refreshed, time.Now().Unix())
		response.FiveHour = view.FiveHour
		response.SevenDay = view.SevenDay
		response.Snapshot = view.Snapshot
	}
	if warningCode != "" {
		response.Snapshot.Stale = true
	}
	writeData(w, http.StatusOK, response)
}
