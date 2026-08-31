package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, 200, users)
}

func (s *Server) findKey(id int64) (store.KeySnapshot, bool) {
	cache := s.upstream.CachedKeys()
	for _, item := range cache.Items {
		if item.UpstreamKeyID == id {
			return item, true
		}
	}
	return store.KeySnapshot{}, false
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username      string `json:"username"`
		DisplayName   string `json:"display_name"`
		Password      string `json:"password"`
		UpstreamKeyID int64  `json:"upstream_key_id"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if !validUsername(request.Username) || strings.TrimSpace(request.DisplayName) == "" {
		writeError(w, 400, "INVALID_USER", "请填写有效用户名和显示名称")
		return
	}
	hash, err := secure.HashPassword(request.Password)
	if err != nil {
		writeError(w, 400, "INVALID_PASSWORD", err.Error())
		return
	}
	snapshot, ok := s.findKey(request.UpstreamKeyID)
	if !ok || !snapshot.Compliant() {
		writeError(w, 400, "INVALID_UPSTREAM_KEY", "请选择同时配置 5h 与 7d 正数限额的可用 Key")
		return
	}
	user, err := s.store.CreateUser(r.Context(), request.Username, request.DisplayName, hash, &snapshot, currentSession(r).User.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusCreated, user)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 400, "INVALID_ID", "无效的用户 ID")
		return 0, false
	}
	return id, true
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var request struct {
		DisplayName string `json:"display_name"`
		Status      string `json:"status"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.DisplayName) == "" {
		writeError(w, 400, "INVALID_USER", "显示名称不能为空")
		return
	}
	if err := s.store.UpdateUser(r.Context(), id, request.DisplayName, request.Status, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	user, _ := s.store.UserByID(r.Context(), id)
	writeData(w, 200, user)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteUser(r.Context(), id, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var request struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	hash, err := secure.HashPassword(request.Password)
	if err != nil {
		writeError(w, 400, "INVALID_PASSWORD", err.Error())
		return
	}
	if err := s.store.SetUserPassword(r.Context(), id, hash, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, 200, map[string]bool{"changed": true})
}

func (s *Server) setBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var request struct {
		UpstreamKeyID int64 `json:"upstream_key_id"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	snapshot, found := s.findKey(request.UpstreamKeyID)
	if !found || !snapshot.Compliant() {
		writeError(w, 400, "INVALID_UPSTREAM_KEY", "请选择同时配置 5h 与 7d 正数限额的可用 Key")
		return
	}
	if err := s.store.SetBinding(r.Context(), id, snapshot, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	binding, _ := s.store.BindingByUser(r.Context(), id)
	writeData(w, 200, binding)
}

func (s *Server) deleteBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteBinding(r.Context(), id, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, 200, map[string]bool{"deleted": true})
}

func (s *Server) upstreamKeys(w http.ResponseWriter, r *http.Request) {
	cache := s.upstream.CachedKeys()
	bindings, err := s.store.ListBindings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	bound := make(map[int64]int64, len(bindings))
	for _, binding := range bindings {
		bound[binding.UpstreamKeyID] = binding.UserID
	}
	type itemView struct {
		store.KeySnapshot
		BoundUserID int64 `json:"bound_user_id,omitempty"`
		Eligible    bool  `json:"eligible"`
	}
	items := make([]itemView, 0, len(cache.Items))
	for _, item := range cache.Items {
		items = append(items, itemView{KeySnapshot: item, BoundUserID: bound[item.UpstreamKeyID], Eligible: item.Compliant()})
	}
	writeData(w, 200, map[string]any{"keys": items, "fetched_at": cache.FetchedAt.Unix(), "stale": cache.FetchedAt.IsZero() || time.Since(cache.FetchedAt) > 45*time.Second, "last_error": cache.LastError})
}
