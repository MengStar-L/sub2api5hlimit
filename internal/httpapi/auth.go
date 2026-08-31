package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type userView struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Status      string `json:"status"`
}

func safeUser(user store.User) userView {
	return userView{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Role: user.Role, Status: user.Status}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeError(w, http.StatusForbidden, "ORIGIN_REJECTED", "请求来源无效")
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(request.Username)) + "|" + remoteIP(r)
	if s.loginBlocked(key) {
		writeError(w, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "登录尝试过多，请稍后重试")
		return
	}
	user, err := s.store.UserByUsername(r.Context(), request.Username)
	if err != nil || user.Status != store.StatusActive || !secure.VerifyPassword(user.PasswordHash, request.Password) {
		s.recordLoginFailure(key)
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "用户名或密码错误")
		return
	}
	s.clearLoginFailures(key)
	sessionToken, sessionHash, err := secure.GenerateToken(32)
	if err != nil {
		writeError(w, 500, "RANDOM_ERROR", "无法创建会话")
		return
	}
	csrfToken, csrfHash, err := secure.GenerateToken(24)
	if err != nil {
		writeError(w, 500, "RANDOM_ERROR", "无法创建会话")
		return
	}
	now := time.Now()
	absolute := now.Add(absoluteTTL).Unix()
	if err := s.store.CreateSession(r.Context(), sessionHash, csrfHash, user.ID, now.Unix(), now.Add(idleTTL).Unix(), absolute); err != nil {
		writeStoreError(w, err)
		return
	}
	_ = s.store.RecordLogin(r.Context(), user.ID)
	s.setCookies(w, sessionToken, csrfToken, absolute)
	writeData(w, 200, map[string]any{"user": safeUser(user), "csrf_token": csrfToken})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.store.DeleteSession(r.Context(), secure.HashToken(cookie.Value))
	}
	s.clearCookies(w)
	writeData(w, 200, map[string]bool{"logged_out": true})
}

func (s *Server) sessionView(w http.ResponseWriter, r *http.Request) {
	session := currentSession(r)
	csrf := ""
	if cookie, err := r.Cookie(csrfCookie); err == nil {
		csrf = cookie.Value
	}
	writeData(w, 200, map[string]any{"user": safeUser(session.User), "csrf_token": csrf})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	session := currentSession(r)
	user, err := s.store.UserByID(r.Context(), session.User.ID)
	if err != nil || !secure.VerifyPassword(user.PasswordHash, request.CurrentPassword) {
		writeError(w, 401, "INVALID_CREDENTIALS", "当前密码错误")
		return
	}
	hash, err := secure.HashPassword(request.NewPassword)
	if err != nil {
		writeError(w, 400, "INVALID_PASSWORD", err.Error())
		return
	}
	if err := s.store.SetUserPassword(r.Context(), user.ID, hash, user.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.clearCookies(w)
	writeData(w, 200, map[string]bool{"changed": true})
}

func (s *Server) loginBlocked(key string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	cutoff := time.Now().Add(-15 * time.Minute)
	values := s.loginFails[key][:0]
	for _, value := range s.loginFails[key] {
		if value.After(cutoff) {
			values = append(values, value)
		}
	}
	s.loginFails[key] = values
	return len(values) >= 5
}

func (s *Server) recordLoginFailure(key string) {
	s.loginMu.Lock()
	s.loginFails[key] = append(s.loginFails[key], time.Now())
	s.loginMu.Unlock()
}

func (s *Server) clearLoginFailures(key string) {
	s.loginMu.Lock()
	delete(s.loginFails, key)
	s.loginMu.Unlock()
}
