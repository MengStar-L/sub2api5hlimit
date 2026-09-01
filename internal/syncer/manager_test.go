package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/sub2api"
)

const (
	testAdminKey = "admin-SENTINEL-UPSTREAM-CREDENTIAL"
	testRawKey   = "sk-SENTINEL-DISTRIBUTION-KEY-abcd"
	testLastIP   = "198.51.100.77"
)

func TestManagerSyncsSafeSnapshotsFromOfficialEnvelope(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	reset5 := now.Add(4 * time.Hour)
	reset7 := now.Add(6 * 24 * time.Hour)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testAdminKey {
			writeUpstream(t, w, http.StatusUnauthorized, 401, nil)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/system/version":
			writeUpstream(t, w, 200, 0, map[string]any{"version": "v0.1.183"})
		case "/api/v1/admin/users":
			writePage(t, w, r, []map[string]any{{"id": 1, "email": "owner@example.com", "username": "owner", "role": "user", "status": "active"}})
		case "/api/v1/admin/users/1/api-keys":
			writePage(t, w, r, []map[string]any{{
				"id": 7, "user_id": 1, "key": testRawKey, "name": "weekly-seat", "status": "active",
				"last_used_ip": testLastIP, "rate_limit_5h": 10.0, "usage_5h": 2.5,
				"rate_limit_7d": 70.0, "usage_7d": 14.0, "reset_5h_at": reset5,
				"reset_7d_at": reset7, "updated_at": now,
			}})
		case "/api/v1/admin/accounts":
			writePage(t, w, r, []map[string]any{{
				"id": 9, "name": "Codex Primary", "platform": "openai", "type": "oauth", "status": "active",
				"schedulable": true, "credentials": map[string]any{"email": "pool.owner@example.com", "plan_type": "team"},
				"updated_at": now,
			}})
		case "/api/v1/admin/accounts/usage/batch":
			var request struct {
				AccountIDs []int64 `json:"account_ids"`
				Force      bool    `json:"force"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Force {
				t.Errorf("unexpected batch request: %+v, err=%v", request, err)
			}
			usage := map[string]any{}
			for _, id := range request.AccountIDs {
				if id == 9 {
					usage["9"] = map[string]any{
						"source": "passive", "updated_at": now,
						"five_hour": map[string]any{"utilization": 37.5, "resets_at": reset5, "remaining_seconds": 3600},
						"seven_day": map[string]any{"utilization": 64.0, "resets_at": reset7, "remaining_seconds": 7200},
					}
				}
			}
			writeUpstream(t, w, 200, 0, map[string]any{"usage": usage, "errors": map[string]string{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	data, dbPath := configuredStore(t, upstream.URL)
	manager := New(data, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	probe, err := manager.Probe(context.Background(), store.Settings{
		BaseURL: upstream.URL, AdminAPIKey: testAdminKey, AllowPrivateHTTP: true, OwnerUserID: 1,
	}, true)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if probe.Version != "v0.1.183" || len(probe.Keys) != 1 || probe.AccountCount != 1 {
		t.Fatalf("unexpected probe: %+v", probe)
	}
	if probe.Keys[0].Mask != "sk-…abcd" || strings.Contains(probe.Keys[0].Mask, "SENTINEL") {
		t.Fatalf("unsafe key mask: %q", probe.Keys[0].Mask)
	}

	if err := manager.Sync(context.Background(), "all"); err != nil {
		t.Fatalf("initial Sync: %v", err)
	}
	cache := manager.CachedKeys()
	if len(cache.Items) != 1 || cache.Items[0].Mask != "sk-…abcd" || cache.FetchedAt.IsZero() {
		t.Fatalf("unexpected key cache: %+v", cache)
	}
	admin, err := data.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	user, err := data.CreateUser(context.Background(), "alice", "Alice", "test-hash", &cache.Items[0], admin.ID)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if err := data.SetPoolPublished(context.Background(), []int64{9}, true, admin.ID); err != nil {
		t.Fatalf("SetPoolPublished: %v", err)
	}
	if err := manager.Sync(context.Background(), "usage"); err != nil {
		t.Fatalf("usage Sync: %v", err)
	}
	binding, err := data.BindingByUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.KeyMask != "sk-…abcd" || binding.RateLimit5h != 10 || binding.RateLimit7d != 70 {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	pool, err := data.ListPool(context.Background(), true)
	if err != nil || len(pool) != 1 {
		t.Fatalf("ListPool: len=%d err=%v", len(pool), err)
	}
	if !pool[0].FiveSupported || pool[0].FiveUtilization == nil || *pool[0].FiveUtilization != 37.5 || pool[0].UsageSource != "passive" {
		t.Fatalf("unexpected pool snapshot: %+v", pool[0])
	}

	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		contents, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		text := string(contents)
		for _, secret := range []string{testRawKey, testLastIP, testAdminKey} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s contains sensitive sentinel %q", filepath.Base(candidate), secret)
			}
		}
	}
}

func TestManagerResetQuotaPersistsSafeSnapshotBeforeCallerRefreshes(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	usage5h, usage7d := 4.0, 40.0
	keyListCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testAdminKey {
			writeUpstream(t, w, http.StatusUnauthorized, 401, nil)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/users/1/api-keys":
			mu.Lock()
			keyListCalls++
			five, seven := usage5h, usage7d
			mu.Unlock()
			writePage(t, w, r, []map[string]any{{
				"id": 7, "user_id": 1, "key": testRawKey, "last_used_ip": testLastIP,
				"name": "seat", "status": "active", "rate_limit_5h": 10.0,
				"usage_5h": five, "rate_limit_7d": 100.0, "usage_7d": seven,
				"updated_at": time.Now().UTC(),
			}})
		case "/api/v1/admin/api-keys/7":
			if r.Method != http.MethodPut {
				t.Errorf("reset method = %s", r.Method)
			}
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["reset_rate_limit_usage"] != true {
				t.Errorf("reset request = %#v, err=%v", request, err)
			}
			mu.Lock()
			usage5h, usage7d = 0, 0
			mu.Unlock()
			writeUpstream(t, w, 200, 0, map[string]any{"api_key": map[string]any{
				"id": 7, "key": testRawKey, "last_used_ip": testLastIP,
				"usage_5h": 0, "usage_7d": 0, "updated_at": time.Now().UTC(),
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	data, _ := configuredStore(t, upstream.URL)
	manager := New(data, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := manager.Sync(context.Background(), "keys"); err != nil {
		t.Fatalf("Sync(keys): %v", err)
	}
	admin, err := data.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	key := manager.CachedKeys().Items[0]
	user, err := data.CreateUser(context.Background(), "reset-user", "Reset User", "hash", &key, admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	result, err := manager.ResetQuota(context.Background(), 7)
	if err != nil {
		t.Fatalf("ResetQuota: %v", err)
	}
	if !result.Applied || !result.SnapshotUpdated {
		t.Fatalf("result = %+v", result)
	}
	binding, err := data.BindingByUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Usage5h != 0 || binding.Usage7d != 0 || binding.Reset5hAt != nil || binding.Reset7dAt != nil ||
		binding.SourceUpdatedAt == nil || binding.LastSuccessAt == nil {
		t.Fatalf("ResetQuota did not persist its safe response snapshot: %+v", binding)
	}
	mu.Lock()
	listCallsAfterReset := keyListCalls
	mu.Unlock()
	if listCallsAfterReset != 1 {
		t.Fatalf("key list calls after ResetQuota = %d, want initial sync only", listCallsAfterReset)
	}
	if err := manager.Sync(context.Background(), "keys"); err != nil {
		t.Fatalf("Sync(keys) after reset: %v", err)
	}
	binding, err = data.BindingByUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Usage5h != 0 || binding.Usage7d != 0 || binding.Reset5hAt != nil || binding.Reset7dAt != nil {
		t.Fatalf("binding was not refreshed: %+v", binding)
	}
}

func TestQuotaResetSupersedesAnOlderInFlightKeySnapshot(t *testing.T) {
	var mu sync.Mutex
	usage5h, usage7d := 4.0, 40.0
	keyListCalls := 0
	staleStarted := make(chan struct{})
	releaseStale := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseStale) }) }
	defer release()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != testAdminKey {
			writeUpstream(t, w, http.StatusUnauthorized, 401, nil)
			return
		}
		switch r.URL.Path {
		case "/api/v1/admin/users/1/api-keys":
			mu.Lock()
			keyListCalls++
			call := keyListCalls
			five, seven := usage5h, usage7d
			mu.Unlock()
			if call == 2 {
				close(staleStarted)
				<-releaseStale
			}
			writePage(t, w, r, []map[string]any{{
				"id": 7, "user_id": 1, "key": testRawKey, "last_used_ip": testLastIP,
				"name": "seat", "status": "active", "rate_limit_5h": 10.0,
				"usage_5h": five, "rate_limit_7d": 100.0, "usage_7d": seven,
				"updated_at": time.Now().UTC(),
			}})
		case "/api/v1/admin/api-keys/7":
			mu.Lock()
			usage5h, usage7d = 0, 0
			mu.Unlock()
			writeUpstream(t, w, http.StatusOK, 0, map[string]any{"api_key": map[string]any{
				"id": 7, "usage_5h": 0, "usage_7d": 0, "updated_at": time.Now().UTC(),
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	data, _ := configuredStore(t, upstream.URL)
	manager := New(data, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := manager.Sync(context.Background(), "keys"); err != nil {
		t.Fatalf("initial Sync(keys): %v", err)
	}
	admin, err := data.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	key := manager.CachedKeys().Items[0]
	user, err := data.CreateUser(context.Background(), "race-user", "Race User", "hash", &key, admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	oldSyncDone := make(chan error, 1)
	go func() { oldSyncDone <- manager.Sync(context.Background(), "keys") }()
	select {
	case <-staleStarted:
	case <-time.After(time.Second):
		t.Fatal("older key sync did not reach its blocked response")
	}
	if _, err := manager.ResetQuota(context.Background(), 7); err != nil {
		t.Fatalf("ResetQuota during key sync: %v", err)
	}
	binding, err := data.BindingByUser(context.Background(), user.ID)
	if err != nil || binding.Usage5h != 0 || binding.Usage7d != 0 {
		t.Fatalf("safe reset snapshot = %+v, err=%v", binding, err)
	}

	freshCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Sync(freshCtx, "keys"); err != nil {
		t.Fatalf("fresh Sync(keys) joined the older flight: %v", err)
	}
	release()
	select {
	case err := <-oldSyncDone:
		if err != nil {
			t.Fatalf("superseded Sync(keys): %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("superseded key sync did not finish")
	}

	binding, err = data.BindingByUser(context.Background(), user.ID)
	if err != nil || binding.Usage5h != 0 || binding.Usage7d != 0 {
		t.Fatalf("older key snapshot overwrote reset: %+v, err=%v", binding, err)
	}
	cache := manager.CachedKeys()
	if len(cache.Items) != 1 || cache.Items[0].Usage5h != 0 || cache.Items[0].Usage7d != 0 {
		t.Fatalf("cached keys after superseded sync = %+v", cache)
	}
	mu.Lock()
	gotCalls := keyListCalls
	mu.Unlock()
	if gotCalls != 3 {
		t.Fatalf("key list calls = %d, want initial, superseded, and fresh", gotCalls)
	}
}

func TestConnectionRotationDiscardsAllOldScopeResponses(t *testing.T) {
	var mu sync.Mutex
	keyCalls, accountCalls := 0, 0
	oldKeyStarted := make(chan struct{})
	oldAccountStarted := make(chan struct{})
	oldUsageStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseOld) }) }
	defer release()

	oldUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/users/1/api-keys":
			mu.Lock()
			keyCalls++
			call := keyCalls
			mu.Unlock()
			if call == 2 {
				close(oldKeyStarted)
				<-releaseOld
			}
			writePage(t, w, r, []map[string]any{{
				"id": 7, "user_id": 1, "key": testRawKey, "name": "old-key", "status": "active",
				"rate_limit_5h": 10, "usage_5h": 4, "rate_limit_7d": 100, "usage_7d": 40,
			}})
		case "/api/v1/admin/accounts":
			mu.Lock()
			accountCalls++
			call := accountCalls
			mu.Unlock()
			if call == 2 {
				close(oldAccountStarted)
				<-releaseOld
			}
			writePage(t, w, r, []map[string]any{{
				"id": 9, "name": "old-account", "platform": "openai", "type": "oauth", "status": "active",
				"schedulable": true, "credentials": map[string]any{"email": "old@example.com"},
			}})
		case "/api/v1/admin/accounts/usage/batch":
			close(oldUsageStarted)
			<-releaseOld
			writeUpstream(t, w, http.StatusOK, 0, map[string]any{
				"usage": map[string]any{"9": map[string]any{
					"source": "passive", "five_hour": map[string]any{"utilization": 91},
					"seven_day": map[string]any{"utilization": 92},
				}},
				"errors": map[string]string{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer oldUpstream.Close()

	newUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/users/1/api-keys":
			writePage(t, w, r, []map[string]any{{
				"id": 77, "user_id": 1, "key": "sk-new-connection-zz99", "name": "new-key", "status": "active",
				"rate_limit_5h": 20, "usage_5h": 1, "rate_limit_7d": 200, "usage_7d": 2,
			}})
		case "/api/v1/admin/accounts":
			writePage(t, w, r, []map[string]any{{
				"id": 99, "name": "new-account", "platform": "openai", "type": "oauth", "status": "active",
				"schedulable": true, "credentials": map[string]any{"email": "new@example.com"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer newUpstream.Close()

	data, _ := configuredStore(t, oldUpstream.URL)
	manager := New(data, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := manager.Sync(context.Background(), "keys"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Sync(context.Background(), "accounts"); err != nil {
		t.Fatal(err)
	}
	admin, err := data.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := data.SetPoolPublished(context.Background(), []int64{9}, true, admin.ID); err != nil {
		t.Fatal(err)
	}

	oldDone := make(chan error, 3)
	for _, scope := range []string{"keys", "accounts", "usage"} {
		scope := scope
		go func() { oldDone <- manager.Sync(context.Background(), scope) }()
	}
	for name, started := range map[string]<-chan struct{}{
		"keys": oldKeyStarted, "accounts": oldAccountStarted, "usage": oldUsageStarted,
	} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("old %s sync did not start", name)
		}
	}
	if err := data.SetPoolPublished(context.Background(), []int64{9}, false, admin.ID); err != nil {
		t.Fatal(err)
	}

	releaseRotation := manager.BeginConnectionRotation()
	current, err := data.GetSettings(context.Background())
	if err != nil {
		releaseRotation()
		t.Fatal(err)
	}
	current.BaseURL = newUpstream.URL
	current.ConnectionUUID = "connection-2"
	if err := data.UpdateSettings(context.Background(), current, false, true); err != nil {
		releaseRotation()
		t.Fatal(err)
	}
	releaseRotation()
	if err := manager.Sync(context.Background(), "all"); err != nil {
		t.Fatalf("new connection Sync(all): %v", err)
	}
	release()
	for range 3 {
		select {
		case err := <-oldDone:
			if err != nil {
				t.Fatalf("superseded old sync: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("superseded old sync did not finish")
		}
	}

	cache := manager.CachedKeys()
	if len(cache.Items) != 1 || cache.Items[0].UpstreamKeyID != 77 || cache.Items[0].Name != "new-key" {
		t.Fatalf("key cache contains old connection data: %+v", cache)
	}
	pool, err := data.ListPool(context.Background(), false)
	if err != nil || len(pool) != 1 || pool[0].UpstreamAccountID != 99 || pool[0].Name != "new-account" {
		t.Fatalf("pool contains old connection data: %+v, err=%v", pool, err)
	}
}

func TestErrorCodesAndBackoffStayBounded(t *testing.T) {
	t.Parallel()
	if got := syncErrorCode(&sub2api.UpstreamError{StatusCode: 429}); got != "UPSTREAM_RATE_LIMITED" {
		t.Fatalf("429 code = %q", got)
	}
	if got := syncErrorCode(context.DeadlineExceeded); got != "UPSTREAM_TIMEOUT" {
		t.Fatalf("timeout code = %q", got)
	}
	if got := syncErrorCode(&sub2api.SchemaError{Detail: "bad"}); got != "UPSTREAM_SCHEMA" {
		t.Fatalf("schema code = %q", got)
	}
	for failures := 0; failures < 20; failures++ {
		delay := nextDelay(15*time.Second, failures)
		if delay <= 0 || delay > maxBackoff+maxBackoff/10 {
			t.Fatalf("failure %d produced invalid delay %s", failures, delay)
		}
	}
	if got := safeUsageSource(testRawKey); got != "upstream" {
		t.Fatalf("unsafe source was retained: %q", got)
	}
	if !errors.Is(errors.Join(context.Canceled, context.DeadlineExceeded), context.Canceled) {
		t.Fatal("sanity check failed")
	}
}

func configuredStore(t *testing.T, upstreamURL string) (*store.Store, string) {
	t.Helper()
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	box, err := secure.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "portal.db")
	data, err := store.Open(context.Background(), dbPath, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.SetSetupToken(context.Background(), secure.HashToken("setup-token"), time.Now().Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := data.CompleteSetup(context.Background(), secure.HashToken("setup-token"), store.User{Username: "admin", DisplayName: "Admin"}, "test-hash", store.Settings{
		ConnectionUUID: "connection-1", BaseURL: upstreamURL, AdminAPIKey: testAdminKey,
		OwnerUserID: 1, OwnerLabel: "owner", AllowPrivateHTTP: true,
	}); err != nil {
		t.Fatal(err)
	}
	return data, dbPath
}

func writePage(t *testing.T, w http.ResponseWriter, r *http.Request, items any) {
	t.Helper()
	if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("page_size") != "100" {
		t.Errorf("unexpected pagination query: %s", r.URL.RawQuery)
	}
	writeUpstream(t, w, 200, 0, map[string]any{"items": items, "total": 1, "page": 1, "page_size": 100, "pages": 1})
}

func writeUpstream(t *testing.T, w http.ResponseWriter, status, code int, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{"code": code, "message": "success", "data": data}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
