package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type adminPoolView struct {
	ID          int64          `json:"id"`
	DisplayName string         `json:"display_name"`
	Account     string         `json:"account"`
	Alias       string         `json:"alias"`
	Provider    string         `json:"provider"`
	PlanType    string         `json:"plan_type"`
	Status      string         `json:"status"`
	Published   bool           `json:"published"`
	Missing     bool           `json:"missing"`
	FiveHour    poolWindowView `json:"window_5h"`
	SevenDay    poolWindowView `json:"window_7d"`
	Snapshot    snapshotMeta   `json:"snapshot"`
}

func (s *Server) adminPool(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListPool(r.Context(), false)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := time.Now().Unix()
	views := make([]adminPoolView, 0, len(items))
	for _, item := range items {
		account := strings.TrimSpace(item.Email)
		if account == "" {
			account = item.PublicAlias
		}
		views = append(views, adminPoolView{
			ID: item.UpstreamAccountID, DisplayName: item.Name, Account: account, Alias: item.PublicAlias,
			Provider: item.Platform, PlanType: item.PlanType, Status: item.NormalizedStatus,
			Published: item.Published, Missing: item.Missing,
			FiveHour: poolWindowView{Supported: item.FiveSupported, Utilization: item.FiveUtilization, ResetAt: item.FiveResetAt},
			SevenDay: poolWindowView{Supported: item.SevenSupported, Utilization: item.SevenUtilization, ResetAt: item.SevenResetAt},
			Snapshot: snapshotMeta{AsOf: now, SourceUpdatedAt: item.SourceUpdatedAt, LastSuccessAt: item.LastSuccessAt,
				Stale: item.LastSuccessAt == nil || now-*item.LastSuccessAt > 180},
		})
	}
	writeData(w, http.StatusOK, map[string]any{"accounts": views})
}

func (s *Server) publishPool(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AccountIDs []int64 `json:"account_ids"`
		Published  bool    `json:"published"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.AccountIDs) == 0 || len(request.AccountIDs) > 100 {
		writeError(w, http.StatusBadRequest, "INVALID_ACCOUNTS", "请选择 1 至 100 个账号")
		return
	}
	seen := make(map[int64]struct{}, len(request.AccountIDs))
	for _, id := range request.AccountIDs {
		if id <= 0 {
			writeError(w, http.StatusBadRequest, "INVALID_ACCOUNTS", "账号 ID 无效")
			return
		}
		if _, exists := seen[id]; exists {
			writeError(w, http.StatusBadRequest, "INVALID_ACCOUNTS", "账号列表包含重复项")
			return
		}
		seen[id] = struct{}{}
	}
	if err := s.store.SetPoolPublished(r.Context(), request.AccountIDs, request.Published, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	if request.Published {
		s.runSync("usage")
	}
	writeData(w, http.StatusOK, map[string]any{"updated": len(request.AccountIDs)})
}

type settingsView struct {
	BaseURL              string `json:"base_url"`
	OwnerUserID          int64  `json:"owner_user_id"`
	OwnerUsername        string `json:"owner_username"`
	AllowInsecureHTTP    bool   `json:"allow_insecure_http"`
	ConnectionID         string `json:"connection_id"`
	KeyLastSuccessAt     *int64 `json:"key_last_success_at,omitempty"`
	AccountLastSuccessAt *int64 `json:"account_last_success_at,omitempty"`
	UsageLastSuccessAt   *int64 `json:"usage_last_success_at,omitempty"`
	LastSuccessAt        *int64 `json:"last_success_at,omitempty"`
}

func publicSettings(value store.Settings) settingsView {
	return settingsView{
		BaseURL: value.BaseURL, OwnerUserID: value.OwnerUserID, OwnerUsername: value.OwnerLabel,
		AllowInsecureHTTP: value.AllowPrivateHTTP, ConnectionID: value.ConnectionUUID,
		KeyLastSuccessAt: value.LastKeySyncAt, AccountLastSuccessAt: value.LastAccountSyncAt,
		UsageLastSuccessAt: value.LastUsageSyncAt,
		LastSuccessAt:      latestTimestamp(value.LastKeySyncAt, value.LastAccountSyncAt, value.LastUsageSyncAt),
	}
}

func latestTimestamp(values ...*int64) *int64 {
	var latest int64
	for _, value := range values {
		if value != nil && *value > latest {
			latest = *value
		}
	}
	if latest == 0 {
		return nil
	}
	return &latest
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, publicSettings(settings))
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		BaseURL           string `json:"base_url"`
		OwnerUserID       int64  `json:"owner_user_id"`
		AdminAPIKey       string `json:"admin_api_key"`
		AllowInsecureHTTP bool   `json:"allow_insecure_http"`
		ConfirmNonSimple  bool   `json:"confirm_non_simple"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !request.ConfirmNonSimple {
		writeError(w, http.StatusBadRequest, "MODE_ACK_REQUIRED", "必须确认 Sub2API 未使用 simple 模式")
		return
	}
	current, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	baseURL := strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	if baseURL == "" || request.OwnerUserID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_SETTINGS", "Base URL 与固定 Key 所有者不能为空")
		return
	}
	adminKey := strings.TrimSpace(request.AdminAPIKey)
	replaceKey := adminKey != ""
	if !replaceKey {
		adminKey = current.AdminAPIKey
	}
	next := store.Settings{
		ConnectionUUID: current.ConnectionUUID, BaseURL: baseURL, AdminAPIKey: adminKey,
		OwnerUserID: request.OwnerUserID, AllowPrivateHTTP: request.AllowInsecureHTTP,
	}
	probe, err := s.upstream.Probe(r.Context(), next, true)
	if err != nil {
		s.log.Warn("upstream settings probe failed", "error", secure.Redact(err.Error()))
		writeError(w, http.StatusBadGateway, "UPSTREAM_PROBE_FAILED", "无法验证 Sub2API 连接、版本、所有者或所需接口")
		return
	}
	for _, owner := range probe.Owners {
		if owner.ID == request.OwnerUserID {
			next.OwnerLabel = strings.TrimSpace(owner.Username)
			if next.OwnerLabel == "" {
				next.OwnerLabel = strings.TrimSpace(owner.Email)
			}
			break
		}
	}
	if next.OwnerLabel == "" {
		writeError(w, http.StatusBadRequest, "OWNER_NOT_FOUND", "固定 Key 所有者不存在")
		return
	}
	rotate := strings.TrimRight(current.BaseURL, "/") != baseURL || current.OwnerUserID != next.OwnerUserID
	if rotate {
		connectionID, _, randomErr := secure.GenerateToken(18)
		if randomErr != nil {
			writeError(w, http.StatusInternalServerError, "RANDOM_ERROR", "无法生成连接标识")
			return
		}
		next.ConnectionUUID = connectionID
	}
	if err := s.store.UpdateSettings(r.Context(), next, replaceKey, rotate); err != nil {
		if strings.Contains(err.Error(), "unbind all users") {
			writeError(w, http.StatusConflict, "UPSTREAM_IN_USE", "更换 Base URL 或所有者前，请先解绑全部用户并取消所有账号发布")
			return
		}
		writeStoreError(w, err)
		return
	}
	updated, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if rotate {
		s.upstream.ClearSnapshots()
	}
	s.runSync("all")
	writeData(w, http.StatusOK, publicSettings(updated))
}

func (s *Server) syncNow(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Scope string `json:"scope"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	switch request.Scope {
	case "all", "keys", "accounts", "usage":
	default:
		writeError(w, http.StatusBadRequest, "INVALID_SYNC_SCOPE", "同步范围无效")
		return
	}
	s.runSync(request.Scope)
	writeData(w, http.StatusAccepted, map[string]any{"started": true, "scope": request.Scope})
}

func (s *Server) runSync(scope string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := s.upstream.Sync(ctx, scope); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("upstream sync failed", "scope", scope, "error", secure.Redact(err.Error()))
		}
	}()
}
