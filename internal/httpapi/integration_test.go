package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

const (
	testAdminPassword = "Administrator password 123!"
	testUserPassword  = "Alice password 123!"
	testNewPassword   = "Alice replacement password 456!"

	testAdminAPIKey = "admin-json-response-sentinel"
	testFullAPIKey  = "sk-complete-distribution-key-sentinel-1234"
	testLastUsedIP  = "198.51.100.249"
)

func TestHTTPAPILifecycleRBACAndSecretBoundary(t *testing.T) {
	ctx := context.Background()
	fixture := newHTTPFixture(t)
	anonymousClient := newCookieClient(t)

	status, body, _ := fixture.request(t, anonymousClient, http.MethodGet, "/api/setup/status", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET setup status = %d, body = %s", status, body)
	}
	var initialStatus struct {
		Data store.SetupStatus `json:"data"`
	}
	decodeResponse(t, body, &initialStatus)
	if initialStatus.Data.Complete || initialStatus.Data.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("initial setup status = %#v", initialStatus.Data)
	}
	setupToken := fixture.api.SetupToken()
	if setupToken == "" {
		t.Fatal("new server did not expose its one-time setup token")
	}
	if bytes.Contains(body, []byte(setupToken)) {
		t.Fatal("setup status response exposed the one-time setup token")
	}

	completeRequest := map[string]any{
		"token":              setupToken,
		"username":           "admin",
		"display_name":       "Portal Administrator",
		"password":           testAdminPassword,
		"non_simple_ack":     true,
		"base_url":           "https://sub2api.example.com/",
		"admin_api_key":      testAdminAPIKey,
		"allow_private_http": false,
		"owner_user_id":      42,
		"owner_label":        "ignored-client-label",
	}
	invalidComplete := make(map[string]any, len(completeRequest))
	for key, value := range completeRequest {
		invalidComplete[key] = value
	}
	invalidComplete["token"] = "invalid-setup-token"
	status, body, _ = fixture.request(t, anonymousClient, http.MethodPost, "/api/setup/complete", invalidComplete, fixture.server.URL, "")
	assertAPIError(t, status, body, http.StatusUnauthorized, "INVALID_SETUP_TOKEN")
	if probe := fixture.upstream.lastProbe(); probe.BaseURL != "" {
		t.Fatalf("invalid setup token triggered an upstream probe: %#v", probe)
	}

	status, body, _ = fixture.request(t, anonymousClient, http.MethodPost, "/api/setup/complete", completeRequest, fixture.server.URL, "")
	if status != http.StatusCreated {
		t.Fatalf("POST setup complete = %d, body = %s", status, body)
	}
	var completed struct {
		Data struct {
			Complete bool   `json:"complete"`
			Version  string `json:"version"`
		} `json:"data"`
	}
	decodeResponse(t, body, &completed)
	if !completed.Data.Complete || completed.Data.Version != "v0.1.183" {
		t.Fatalf("setup complete response = %#v", completed.Data)
	}
	if fixture.api.SetupToken() != "" {
		t.Fatal("server retained the setup token after successful setup")
	}
	if probe := fixture.upstream.lastProbe(); probe.AdminAPIKey != testAdminAPIKey || probe.OwnerUserID != 42 {
		t.Fatalf("upstream probe settings = %#v", probe)
	}
	fixture.upstream.waitForSync(t, "all")

	status, body, _ = fixture.request(t, anonymousClient, http.MethodGet, "/api/setup/status", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET completed setup status = %d, body = %s", status, body)
	}
	var completedStatus struct {
		Data store.SetupStatus `json:"data"`
	}
	decodeResponse(t, body, &completedStatus)
	if !completedStatus.Data.Complete || completedStatus.Data.ExpiresAt != 0 {
		t.Fatalf("completed setup status = %#v", completedStatus.Data)
	}

	adminClient := newCookieClient(t)
	status, body, cookies := fixture.request(t, adminClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": testAdminPassword,
	}, "https://attacker.example", "")
	assertAPIError(t, status, body, http.StatusForbidden, "ORIGIN_REJECTED")
	if len(cookies) != 0 {
		t.Fatalf("rejected login set cookies: %#v", cookies)
	}

	status, body, cookies = fixture.request(t, adminClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": testAdminPassword,
	}, fixture.server.URL, "")
	if status != http.StatusOK {
		t.Fatalf("admin login = %d, body = %s", status, body)
	}
	adminCSRF := loginCSRF(t, body)
	assertLoginCookies(t, cookies)

	status, body, _ = fixture.request(t, adminClient, http.MethodGet, "/api/me/dashboard", nil, "", "")
	assertAPIError(t, status, body, http.StatusForbidden, "FORBIDDEN")

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/users", map[string]any{
		"username": "csrf-denied", "display_name": "CSRF Denied", "password": testUserPassword, "upstream_key_id": 1001,
	}, fixture.server.URL, "")
	assertAPIError(t, status, body, http.StatusForbidden, "CSRF_REJECTED")
	if _, err := fixture.store.UserByUsername(ctx, "csrf-denied"); err != store.ErrNotFound {
		t.Fatalf("CSRF-rejected mutation changed storage: %v", err)
	}

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/users", map[string]any{
		"username": "alice", "display_name": "Alice", "password": testUserPassword, "upstream_key_id": 1001,
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusCreated {
		t.Fatalf("admin create user = %d, body = %s", status, body)
	}
	var created struct {
		Data store.User `json:"data"`
	}
	decodeResponse(t, body, &created)
	if created.Data.ID <= 0 || created.Data.Username != "alice" || created.Data.Role != store.RoleUser || created.Data.Binding == nil {
		t.Fatalf("created user = %#v", created.Data)
	}
	if created.Data.Binding.UpstreamKeyID != 1001 || created.Data.Binding.KeyMask != secure.MaskAPIKey(testFullAPIKey) {
		t.Fatalf("created binding = %#v", created.Data.Binding)
	}
	aliceID := created.Data.ID

	secondHash, err := secure.HashPassword("Bob password 123!")
	if err != nil {
		t.Fatal(err)
	}
	secondKey := fixture.upstream.keys[1]
	if _, err := fixture.store.CreateUser(ctx, "bob", "Bob", secondHash, &secondKey, 0); err != nil {
		t.Fatalf("create isolation user: %v", err)
	}

	now := time.Now().Unix()
	if err := fixture.store.ApplyPoolInventory(ctx, []store.PoolInventory{
		{UpstreamAccountID: 501, Name: "Published account", Email: "pool.private@example.com", Platform: "openai", AccountType: "oauth", PlanType: "pro", Status: "active", Schedulable: true},
		{UpstreamAccountID: 502, Name: "Private account", Email: "never.visible@example.net", Platform: "anthropic", AccountType: "oauth", PlanType: "max", Status: "active", Schedulable: true},
	}, now); err != nil {
		t.Fatalf("ApplyPoolInventory() error = %v", err)
	}
	five := 12.5
	seven := 33.5
	resetFive := now + 3600
	resetSeven := now + 86400
	if err := fixture.store.ApplyPoolUsage(ctx, []store.PoolUsage{{
		UpstreamAccountID: 501, FiveSupported: true, FiveUtilization: &five, FiveResetAt: &resetFive,
		SevenSupported: true, SevenUtilization: &seven, SevenResetAt: &resetSeven, Source: "passive", SourceUpdatedAt: &now,
	}}, now); err != nil {
		t.Fatalf("ApplyPoolUsage() error = %v", err)
	}
	if err := fixture.store.SetPoolPublished(ctx, []int64{501}, true, 0); err != nil {
		t.Fatalf("SetPoolPublished() error = %v", err)
	}

	status, body, _ = fixture.request(t, adminClient, http.MethodGet, "/api/admin/upstream-keys", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET upstream keys = %d, body = %s", status, body)
	}
	if !bytes.Contains(body, []byte(secure.MaskAPIKey(testFullAPIKey))) {
		t.Fatalf("upstream key response lacks safe mask: %s", body)
	}
	status, body, _ = fixture.request(t, adminClient, http.MethodGet, "/api/admin/settings", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET settings = %d, body = %s", status, body)
	}
	if bytes.Contains(body, []byte("admin_api_key")) {
		t.Fatalf("public settings response exposes an admin key field: %s", body)
	}
	status, body, _ = fixture.request(t, adminClient, http.MethodGet, "/api/admin/users", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET users = %d, body = %s", status, body)
	}
	var adminUsers struct {
		Data []adminUserView `json:"data"`
	}
	decodeResponse(t, body, &adminUsers)
	var aliceView *adminUserView
	for index := range adminUsers.Data {
		if adminUsers.Data[index].ID == aliceID {
			aliceView = &adminUsers.Data[index]
			break
		}
	}
	if aliceView == nil || !aliceView.Resettable || aliceView.FiveHour == nil || aliceView.SevenDay == nil {
		t.Fatalf("admin user quota view = %#v", aliceView)
	}
	if aliceView.FiveHour.LimitUSD != 10 || aliceView.FiveHour.UsedUSD != 2 || aliceView.SevenDay.LimitUSD != 100 || aliceView.SevenDay.UsedUSD != 20 {
		t.Fatalf("admin user windows = 5h:%#v 7d:%#v", aliceView.FiveHour, aliceView.SevenDay)
	}
	if aliceView.Snapshot.LastSuccessAt == nil || aliceView.Snapshot.AsOf <= 0 {
		t.Fatalf("admin user snapshot = %#v", aliceView.Snapshot)
	}
	fixture.upstream.mu.Lock()
	fixture.upstream.resetFn = func(_ context.Context, keyID int64) (QuotaResetResult, error) {
		if keyID != 1001 {
			return QuotaResetResult{UpstreamKeyID: keyID}, fmt.Errorf("unexpected key id %d", keyID)
		}
		resetSnapshot := fixture.upstream.keys[0]
		resetSnapshot.Usage5h = 0
		resetSnapshot.Usage7d = 0
		resetSnapshot.Reset5hAt = nil
		resetSnapshot.Reset7dAt = nil
		if err := fixture.store.ApplyKeySnapshots(context.Background(), []store.KeySnapshot{resetSnapshot, fixture.upstream.keys[1]}, time.Now().Unix()); err != nil {
			return QuotaResetResult{UpstreamKeyID: keyID, Applied: true}, err
		}
		return QuotaResetResult{UpstreamKeyID: keyID, Applied: true}, nil
	}
	fixture.upstream.mu.Unlock()
	status, body, _ = fixture.request(t, adminClient, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/quota-reset", aliceID), nil, fixture.server.URL, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("POST single quota reset = %d, body = %s", status, body)
	}
	fixture.upstream.waitForSync(t, "keys")
	var resetResponse struct {
		Data quotaResetResponse `json:"data"`
	}
	decodeResponse(t, body, &resetResponse)
	if resetResponse.Data.Status != store.QuotaResetItemSucceeded || !resetResponse.Data.SnapshotUpdated || resetResponse.Data.UpstreamKeyID != 1001 {
		t.Fatalf("single reset response = %#v", resetResponse.Data)
	}
	resetBinding, err := fixture.store.BindingByUser(ctx, aliceID)
	if err != nil || resetBinding.Usage5h != 0 || resetBinding.Usage7d != 0 {
		t.Fatalf("single reset binding = %#v, err=%v", resetBinding, err)
	}

	aliceClient := newCookieClient(t)
	status, body, cookies = fixture.request(t, aliceClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "alice", "password": testUserPassword,
	}, fixture.server.URL, "")
	if status != http.StatusOK {
		t.Fatalf("alice login = %d, body = %s", status, body)
	}
	aliceCSRF := loginCSRF(t, body)
	assertLoginCookies(t, cookies)

	status, body, _ = fixture.request(t, aliceClient, http.MethodGet, "/api/admin/users", nil, "", "")
	assertAPIError(t, status, body, http.StatusForbidden, "FORBIDDEN")

	status, body, _ = fixture.request(t, aliceClient, http.MethodGet, "/api/me/dashboard", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("alice dashboard = %d, body = %s", status, body)
	}
	var dashboard struct {
		Data struct {
			User     userView         `json:"user"`
			Key      keyView          `json:"key"`
			Pool     []publicPoolView `json:"pool"`
			Snapshot snapshotMeta     `json:"snapshot"`
		} `json:"data"`
	}
	decodeResponse(t, body, &dashboard)
	if dashboard.Data.User.ID != aliceID || dashboard.Data.User.Username != "alice" {
		t.Fatalf("dashboard user = %#v", dashboard.Data.User)
	}
	if dashboard.Data.Key.Name != fixture.upstream.keys[0].Name || dashboard.Data.Key.Mask != fixture.upstream.keys[0].Mask {
		t.Fatalf("dashboard key = %#v", dashboard.Data.Key)
	}
	if dashboard.Data.Key.Name == fixture.upstream.keys[1].Name || dashboard.Data.Key.Mask == fixture.upstream.keys[1].Mask {
		t.Fatalf("dashboard exposed another user's binding: %#v", dashboard.Data.Key)
	}
	if len(dashboard.Data.Pool) != 1 {
		t.Fatalf("dashboard pool = %#v, want one published account", dashboard.Data.Pool)
	}
	publicAccount := dashboard.Data.Pool[0]
	if publicAccount.Account != "p***@example.com" || publicAccount.Account == "pool.private@example.com" {
		t.Fatalf("dashboard account was not masked: %#v", publicAccount)
	}
	if publicAccount.ID == "" || publicAccount.ID != publicAccount.Alias || publicAccount.Provider != "openai" {
		t.Fatalf("dashboard public account = %#v", publicAccount)
	}
	if bytes.Contains(body, []byte("never.visible@example.net")) || bytes.Contains(body, []byte(fixture.upstream.keys[1].Name)) {
		t.Fatalf("dashboard leaked private or another-user state: %s", body)
	}

	status, body, _ = fixture.request(t, adminClient, http.MethodPut, fmt.Sprintf("/api/admin/users/%d", aliceID), map[string]any{
		"display_name": "Alice", "status": store.StatusDisabled,
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("disable alice = %d, body = %s", status, body)
	}
	status, body, _ = fixture.request(t, aliceClient, http.MethodGet, "/api/auth/session", nil, "", "")
	assertAPIError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED")

	status, body, _ = fixture.request(t, adminClient, http.MethodPut, fmt.Sprintf("/api/admin/users/%d", aliceID), map[string]any{
		"display_name": "Alice", "status": store.StatusActive,
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("reactivate alice = %d, body = %s", status, body)
	}
	status, body, _ = fixture.request(t, aliceClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "alice", "password": testUserPassword,
	}, fixture.server.URL, "")
	if status != http.StatusOK {
		t.Fatalf("alice relogin = %d, body = %s", status, body)
	}
	aliceCSRF = loginCSRF(t, body)

	status, body, _ = fixture.request(t, adminClient, http.MethodPut, fmt.Sprintf("/api/admin/users/%d/password", aliceID), map[string]any{
		"password": testNewPassword,
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("reset alice password = %d, body = %s", status, body)
	}
	status, body, _ = fixture.request(t, aliceClient, http.MethodPut, "/api/auth/password", map[string]any{
		"current_password": testUserPassword, "new_password": "Another replacement password 789!",
	}, fixture.server.URL, aliceCSRF)
	assertAPIError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED")
	status, body, _ = fixture.request(t, aliceClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "alice", "password": testUserPassword,
	}, fixture.server.URL, "")
	assertAPIError(t, status, body, http.StatusUnauthorized, "INVALID_CREDENTIALS")
	status, body, _ = fixture.request(t, aliceClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "alice", "password": testNewPassword,
	}, fixture.server.URL, "")
	if status != http.StatusOK {
		t.Fatalf("alice login with reset password = %d, body = %s", status, body)
	}

	fixture.assertResponsesDoNotContain(
		t,
		testAdminAPIKey,
		testFullAPIKey,
		testLastUsedIP,
		`"admin_api_key"`,
		`"last_used_ip"`,
		`"setup_token"`,
	)
}

type fakeUpstreamManager struct {
	mu                sync.Mutex
	probe             ProbeResult
	probeFn           func(context.Context, store.Settings, bool) (ProbeResult, error)
	keys              []store.KeySnapshot
	settings          []store.Settings
	syncs             chan string
	syncErr           error
	clears            int
	rotationStarted   chan struct{}
	rotationRelease   chan struct{}
	clearKeysOnRotate bool
	resets            []int64
	resetFn           func(context.Context, int64) (QuotaResetResult, error)
	limitCalls        []fakeLimitCall
	limitFn           func(context.Context, int64, float64, float64, int64) (KeyLimitResult, error)
}

type fakeLimitCall struct {
	KeyID       int64
	RateLimit5h float64
	RateLimit7d float64
	ActorUserID int64
}

func (f *fakeUpstreamManager) SetKeyLimits(ctx context.Context, keyID int64, limit5h, limit7d float64, actorUserID int64) (KeyLimitResult, error) {
	f.mu.Lock()
	f.limitCalls = append(f.limitCalls, fakeLimitCall{KeyID: keyID, RateLimit5h: limit5h, RateLimit7d: limit7d, ActorUserID: actorUserID})
	limitFn := f.limitFn
	f.mu.Unlock()
	if limitFn != nil {
		return limitFn(ctx, keyID, limit5h, limit7d, actorUserID)
	}
	return KeyLimitResult{
		UpstreamKeyID: keyID, Applied: true, SnapshotUpdated: true,
		RateLimit5h: limit5h, RateLimit7d: limit7d,
	}, nil
}

func (f *fakeUpstreamManager) limitCallsSnapshot() []fakeLimitCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeLimitCall(nil), f.limitCalls...)
}

func (f *fakeUpstreamManager) ResetQuota(ctx context.Context, keyID int64) (QuotaResetResult, error) {
	f.mu.Lock()
	f.resets = append(f.resets, keyID)
	resetFn := f.resetFn
	f.mu.Unlock()
	if resetFn != nil {
		return resetFn(ctx, keyID)
	}
	return QuotaResetResult{UpstreamKeyID: keyID, Applied: true}, nil
}

func (f *fakeUpstreamManager) Probe(ctx context.Context, settings store.Settings, full bool) (ProbeResult, error) {
	f.mu.Lock()
	f.settings = append(f.settings, settings)
	probeFn := f.probeFn
	result := f.probe
	result.Keys = append([]store.KeySnapshot(nil), f.probe.Keys...)
	result.Owners = append([]Owner(nil), f.probe.Owners...)
	f.mu.Unlock()
	if probeFn != nil {
		return probeFn(ctx, settings, full)
	}
	return result, nil
}

func (f *fakeUpstreamManager) CachedKeys() KeyCache {
	f.mu.Lock()
	defer f.mu.Unlock()
	return KeyCache{Items: append([]store.KeySnapshot(nil), f.keys...), FetchedAt: time.Now()}
}

func (f *fakeUpstreamManager) BeginConnectionRotation() func() {
	f.mu.Lock()
	f.clears++
	started := f.rotationStarted
	release := f.rotationRelease
	if f.clearKeysOnRotate {
		f.keys = nil
	}
	f.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		<-release
	}
	return func() {}
}

func (f *fakeUpstreamManager) Sync(_ context.Context, scope string) error {
	f.mu.Lock()
	syncErr := f.syncErr
	f.mu.Unlock()
	select {
	case f.syncs <- scope:
	default:
	}
	return syncErr
}

func (f *fakeUpstreamManager) lastProbe() store.Settings {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.settings) == 0 {
		return store.Settings{}
	}
	return f.settings[len(f.settings)-1]
}

func (f *fakeUpstreamManager) waitForSync(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.syncs:
		if got != want {
			t.Fatalf("Sync() scope = %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for Sync(%q)", want)
	}
}

type httpFixture struct {
	store     *store.Store
	upstream  *fakeUpstreamManager
	api       *Server
	server    *httptest.Server
	responses [][]byte
}

func newHTTPFixture(t *testing.T) *httpFixture {
	t.Helper()
	box, err := secure.NewBox(bytes.Repeat([]byte{0x4d}, 32))
	if err != nil {
		t.Fatalf("secure.NewBox() error = %v", err)
	}
	data, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "portal.db"), box)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	fullSecondKey := "sk-second-user-key-sentinel-5678"
	keys := []store.KeySnapshot{
		{UpstreamKeyID: 1001, Name: "alice-key", Mask: secure.MaskAPIKey(testFullAPIKey), Status: "active", RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20},
		{UpstreamKeyID: 1002, Name: "bob-private-key", Mask: secure.MaskAPIKey(fullSecondKey), Status: "active", RateLimit5h: 20, Usage5h: 3, RateLimit7d: 200, Usage7d: 30},
	}
	upstream := &fakeUpstreamManager{
		probe: ProbeResult{
			Version:      "v0.1.183",
			Owners:       []Owner{{ID: 42, Email: "owner@example.com", Username: "distribution-owner"}},
			Keys:         append([]store.KeySnapshot(nil), keys...),
			AccountCount: 2,
		},
		keys:  keys,
		syncs: make(chan string, 8),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api, err := New(data, upstream, logger, false)
	if err != nil {
		_ = data.Close()
		t.Fatalf("httpapi.New() error = %v", err)
	}
	httpServer := httptest.NewServer(api.Handler())
	fixture := &httpFixture{store: data, upstream: upstream, api: api, server: httpServer}
	t.Cleanup(func() {
		httpServer.Close()
		if err := data.Close(); err != nil {
			t.Errorf("store.Close() error = %v", err)
		}
	})
	return fixture
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New() error = %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func (f *httpFixture) request(t *testing.T, client *http.Client, method, path string, payload any, origin, csrf string) (int, []byte, []*http.Cookie) {
	t.Helper()
	var requestBody io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, f.server.URL+path, requestBody)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, path, err)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("%s %s Content-Type = %q", method, path, contentType)
	}
	f.responses = append(f.responses, bytes.Clone(body))
	return response.StatusCode, body, response.Cookies()
}

func (f *httpFixture) assertResponsesDoNotContain(t *testing.T, sentinels ...string) {
	t.Helper()
	for index, body := range f.responses {
		for _, sentinel := range sentinels {
			if bytes.Contains(body, []byte(sentinel)) {
				t.Fatalf("JSON response %d leaked sentinel %q: %s", index, sentinel, body)
			}
		}
	}
}

func decodeResponse(t *testing.T, body []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
}

func assertAPIError(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", status, wantStatus, body)
	}
	var response apiError
	decodeResponse(t, body, &response)
	if response.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q; body = %s", response.Error.Code, wantCode, body)
	}
}

func loginCSRF(t *testing.T, body []byte) string {
	t.Helper()
	var response struct {
		Data struct {
			User      userView `json:"user"`
			CSRFToken string   `json:"csrf_token"`
		} `json:"data"`
	}
	decodeResponse(t, body, &response)
	if response.Data.User.ID <= 0 || response.Data.CSRFToken == "" {
		t.Fatalf("invalid login response: %s", body)
	}
	return response.Data.CSRFToken
}

func assertLoginCookies(t *testing.T, cookies []*http.Cookie) {
	t.Helper()
	values := make(map[string]*http.Cookie, len(cookies))
	for _, cookie := range cookies {
		values[cookie.Name] = cookie
	}
	session := values[sessionCookie]
	csrf := values[csrfCookie]
	if session == nil || csrf == nil {
		t.Fatalf("login cookies = %#v", cookies)
	}
	if !session.HttpOnly || csrf.HttpOnly {
		t.Fatalf("cookie HttpOnly flags: session=%v csrf=%v", session.HttpOnly, csrf.HttpOnly)
	}
	if session.Path != "/" || csrf.Path != "/" || session.SameSite != http.SameSiteLaxMode || csrf.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie security attributes: session=%#v csrf=%#v", session, csrf)
	}
	if session.Value == "" || csrf.Value == "" {
		t.Fatal("login cookies have empty values")
	}
}
