package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type connectionRequest struct {
	BaseURL          string `json:"base_url"`
	AdminAPIKey      string `json:"admin_api_key"`
	AllowPrivateHTTP bool   `json:"allow_private_http"`
	OwnerUserID      int64  `json:"owner_user_id,omitempty"`
	OwnerLabel       string `json:"owner_label,omitempty"`
}

func (s *Server) setupStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.SetupStatus(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, 200, status)
}

func (s *Server) setupProbe(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.SetupStatus(r.Context())
	if err != nil || status.Complete {
		writeError(w, http.StatusNotFound, "SETUP_CLOSED", "首次设置入口已关闭")
		return
	}
	var request connectionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	settings := store.Settings{BaseURL: request.BaseURL, AdminAPIKey: request.AdminAPIKey, AllowPrivateHTTP: request.AllowPrivateHTTP, OwnerUserID: request.OwnerUserID, OwnerLabel: request.OwnerLabel}
	result, err := s.upstream.Probe(r.Context(), settings, request.OwnerUserID > 0)
	if err != nil {
		writeError(w, http.StatusBadGateway, "UPSTREAM_PROBE_FAILED", "无法验证 Sub2API 连接或所需能力")
		return
	}
	writeData(w, 200, result)
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	status, err := s.store.SetupStatus(r.Context())
	if err != nil || status.Complete {
		writeError(w, http.StatusNotFound, "SETUP_CLOSED", "首次设置入口已关闭")
		return
	}
	var request struct {
		Token        string `json:"token"`
		Username     string `json:"username"`
		DisplayName  string `json:"display_name"`
		Password     string `json:"password"`
		NonSimpleAck bool   `json:"non_simple_ack"`
		connectionRequest
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	tokenHash := secure.HashToken(request.Token)
	if err := s.store.ValidateSetupToken(r.Context(), tokenHash); err != nil {
		if errors.Is(err, store.ErrSetupComplete) {
			writeError(w, http.StatusConflict, "SETUP_CLOSED", "首次设置入口已关闭")
			return
		}
		if errors.Is(err, store.ErrInvalidToken) {
			writeError(w, http.StatusUnauthorized, "INVALID_SETUP_TOKEN", "初始化令牌无效或已过期")
			return
		}
		writeStoreError(w, err)
		return
	}
	if !validUsername(request.Username) {
		writeError(w, 400, "INVALID_USERNAME", "用户名必须是 3-64 位字母、数字、点、下划线或短横线")
		return
	}
	if strings.TrimSpace(request.DisplayName) == "" {
		request.DisplayName = request.Username
	}
	if !request.NonSimpleAck {
		writeError(w, 400, "MODE_ACK_REQUIRED", "必须确认 Sub2API 未使用 simple 模式")
		return
	}
	passwordHash, err := secure.HashPassword(request.Password)
	if err != nil {
		writeError(w, 400, "INVALID_PASSWORD", err.Error())
		return
	}
	uuid, _, err := secure.GenerateToken(18)
	if err != nil {
		writeError(w, 500, "RANDOM_ERROR", "无法生成连接标识")
		return
	}
	settings := store.Settings{ConnectionUUID: uuid, BaseURL: request.BaseURL, AdminAPIKey: request.AdminAPIKey,
		AllowPrivateHTTP: request.AllowPrivateHTTP, OwnerUserID: request.OwnerUserID, OwnerLabel: request.OwnerLabel}
	probe, err := s.upstream.Probe(r.Context(), settings, true)
	if err != nil || probe.Version == "" || request.OwnerUserID <= 0 {
		writeError(w, http.StatusBadGateway, "UPSTREAM_PROBE_FAILED", "Sub2API 版本、所有者或所需接口验证失败")
		return
	}
	if len(probe.Keys) == 0 {
		writeError(w, 400, "NO_KEYS", "所选 Sub2API 用户没有可读取的 Key")
		return
	}
	for _, owner := range probe.Owners {
		if owner.ID == request.OwnerUserID {
			settings.OwnerLabel = strings.TrimSpace(owner.Username)
			if settings.OwnerLabel == "" {
				settings.OwnerLabel = strings.TrimSpace(owner.Email)
			}
			break
		}
	}
	settings.BaseURL = strings.TrimRight(strings.TrimSpace(settings.BaseURL), "/")
	if err := s.store.CompleteSetup(r.Context(), tokenHash, store.User{Username: request.Username, DisplayName: request.DisplayName}, passwordHash, settings); err != nil {
		if errors.Is(err, store.ErrSetupComplete) {
			writeError(w, http.StatusConflict, "SETUP_CLOSED", "首次设置入口已关闭")
			return
		}
		if errors.Is(err, store.ErrInvalidToken) {
			writeError(w, 401, "INVALID_SETUP_TOKEN", "初始化令牌无效或已过期")
			return
		}
		writeStoreError(w, err)
		return
	}
	s.setupToken = ""
	s.runSync("all")
	writeData(w, http.StatusCreated, map[string]any{"complete": true, "version": probe.Version})
}
