package httpapi

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

const (
	sessionCookie = "sub2api_limit_session"
	csrfCookie    = "sub2api_limit_csrf"
	maxBody       = 64 << 10
	setupTTL      = 30 * time.Minute
	idleTTL       = 12 * time.Hour
	absoluteTTL   = 7 * 24 * time.Hour
)

type Server struct {
	store          *store.Store
	upstream       UpstreamManager
	log            *slog.Logger
	cookieSecure   bool
	mux            *http.ServeMux
	setupToken     string
	loginMu        sync.Mutex
	loginFails     map[string][]time.Time
	settingsMu     sync.Mutex
	quotaResetMu   sync.Mutex
	quotaResetKeys map[int64]struct{}
	maintenanceMu  sync.Mutex
	bindingMu      sync.RWMutex
	updates        UpdateManager
}

type contextKey string

const sessionKey contextKey = "session"

func New(data *store.Store, upstream UpstreamManager, logger *slog.Logger, cookieSecure bool) (*Server, error) {
	server := &Server{
		store: data, upstream: upstream, log: logger, cookieSecure: cookieSecure, mux: http.NewServeMux(),
		loginFails: make(map[string][]time.Time), quotaResetKeys: make(map[int64]struct{}),
	}
	if err := data.RecoverInterruptedQuotaResetJobs(context.Background()); err != nil {
		return nil, err
	}
	status, err := data.SetupStatus(context.Background())
	if err != nil {
		return nil, err
	}
	if !status.Complete {
		plain, hash, err := secure.GenerateToken(32)
		if err != nil {
			return nil, err
		}
		if err := data.SetSetupToken(context.Background(), hash, time.Now().Add(setupTTL).Unix()); err != nil {
			return nil, err
		}
		server.setupToken = plain
	}
	server.routes()
	return server, nil
}

func (s *Server) SetupToken() string                     { return s.setupToken }
func (s *Server) Handler() http.Handler                  { return securityHeaders(s.mux) }
func (s *Server) MountFrontend(frontend http.Handler)    { s.mux.Handle("/", frontend) }
func (s *Server) SetUpdateManager(manager UpdateManager) { s.updates = manager }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /readyz", s.ready)
	s.mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	s.mux.HandleFunc("POST /api/setup/probe", s.setupProbe)
	s.mux.HandleFunc("POST /api/setup/complete", s.setupComplete)
	s.mux.HandleFunc("POST /api/auth/login", s.login)
	s.mux.Handle("POST /api/auth/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	s.mux.Handle("GET /api/auth/session", s.requireAuth(http.HandlerFunc(s.sessionView)))
	s.mux.Handle("PUT /api/auth/password", s.requireAuth(http.HandlerFunc(s.changePassword)))
	s.mux.Handle("GET /api/me/dashboard", s.requireRole(store.RoleUser, http.HandlerFunc(s.dashboard)))

	s.mux.Handle("GET /api/admin/users", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.listUsers)))
	s.mux.Handle("POST /api/admin/users", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.createUser)))
	s.mux.Handle("PUT /api/admin/users/{id}", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.updateUser)))
	s.mux.Handle("DELETE /api/admin/users/{id}", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.deleteUser)))
	s.mux.Handle("PUT /api/admin/users/{id}/password", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.resetPassword)))
	s.mux.Handle("PUT /api/admin/users/{id}/binding", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.setBinding)))
	s.mux.Handle("DELETE /api/admin/users/{id}/binding", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.deleteBinding)))
	s.mux.Handle("POST /api/admin/users/{id}/quota-reset", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.resetUserQuota)))
	s.mux.Handle("GET /api/admin/upstream-keys", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.upstreamKeys)))
	s.mux.Handle("GET /api/admin/pool", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.adminPool)))
	s.mux.Handle("PUT /api/admin/pool", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.publishPool)))
	s.mux.Handle("GET /api/admin/settings", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.getSettings)))
	s.mux.Handle("PUT /api/admin/settings", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.putSettings)))
	s.mux.Handle("POST /api/admin/sync", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.syncNow)))
	s.mux.Handle("GET /api/admin/update", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.updateStatus)))
	s.mux.Handle("POST /api/admin/update/check", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.checkUpdate)))
	s.mux.Handle("POST /api/admin/update/apply", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.applyUpdate)))
	s.mux.Handle("POST /api/admin/quota-resets", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.createQuotaResetJob)))
	s.mux.Handle("GET /api/admin/quota-resets/current", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.currentQuotaResetJob)))
	s.mux.Handle("GET /api/admin/quota-resets/{id}", s.requireRole(store.RoleAdmin, http.HandlerFunc(s.quotaResetJob)))
	s.mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, 404, "NOT_FOUND", "API endpoint not found")
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeData(w, 200, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, 503, "DATABASE_UNAVAILABLE", "数据库不可用")
		return
	}
	status, err := s.store.SetupStatus(r.Context())
	if err != nil || !status.Complete {
		writeError(w, 503, "SETUP_REQUIRED", "首次设置尚未完成")
		return
	}
	if _, err := s.store.GetSettings(r.Context()); err != nil {
		writeError(w, 503, "SECRETS_LOCKED", "连接密钥不可用")
		return
	}
	writeData(w, 200, map[string]string{"status": "ready"})
}

func (s *Server) requireRole(role string, next http.Handler) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := currentSession(r)
		if session.User.Role != role {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "无权执行此操作")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil || strings.TrimSpace(cookie.Value) == "" {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
			return
		}
		now := time.Now()
		session, err := s.store.Session(r.Context(), secure.HashToken(cookie.Value), now.Unix(), now.Add(idleTTL).Unix())
		if err != nil {
			s.clearCookies(w)
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "登录已过期")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, "ORIGIN_REJECTED", "请求来源无效")
				return
			}
			csrf := r.Header.Get("X-CSRF-Token")
			if csrf == "" || subtle.ConstantTimeCompare([]byte(secure.HashToken(csrf)), []byte(session.CSRFHash)) != 1 {
				writeError(w, http.StatusForbidden, "CSRF_REJECTED", "安全令牌无效")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, session)))
	})
}

func currentSession(r *http.Request) store.Session {
	session, _ := r.Context().Value(sessionKey).(store.Session)
	return session
}

func (s *Server) sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return strings.EqualFold(parsed.Scheme, scheme) && strings.EqualFold(parsed.Host, r.Host)
}

func (s *Server) setCookies(w http.ResponseWriter, sessionToken, csrfToken string, absoluteExpires int64) {
	expires := time.Unix(absoluteExpires, 0)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: sessionToken, Path: "/", HttpOnly: true, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrfToken, Path: "/", HttpOnly: false, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}

func (s *Server) clearCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: name == sessionCookie, Secure: s.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(1, 0)})
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,64}$`)

func validUsername(value string) bool { return usernamePattern.MatchString(strings.TrimSpace(value)) }
