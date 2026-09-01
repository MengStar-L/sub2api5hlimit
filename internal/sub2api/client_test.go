package sub2api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testAdminKey = "admin-sentinel-do-not-leak"

func TestNewClientValidatesBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		baseURL   string
		allowHTTP bool
		wantErr   bool
	}{
		{name: "https public host", baseURL: "https://sub2api.example.com"},
		{name: "http requires approval", baseURL: "http://127.0.0.1:2560", wantErr: true},
		{name: "approved loopback literal", baseURL: "http://127.0.0.1:2560", allowHTTP: true},
		{name: "approved private IPv4 literal", baseURL: "http://10.1.2.3:2560", allowHTTP: true},
		{name: "approved private IPv6 literal", baseURL: "http://[fd00::1]:2560", allowHTTP: true},
		{name: "approved localhost", baseURL: "http://localhost:2560", allowHTTP: true},
		{name: "public HTTP literal rejected", baseURL: "http://8.8.8.8", allowHTTP: true, wantErr: true},
		{name: "HTTP DNS host rejected", baseURL: "http://sub2api.internal", allowHTTP: true, wantErr: true},
		{name: "credentials rejected", baseURL: "https://user:pass@example.com", wantErr: true},
		{name: "path rejected", baseURL: "https://example.com/sub2api", wantErr: true},
		{name: "query rejected", baseURL: "https://example.com?x=1", wantErr: true},
		{name: "wrong scheme", baseURL: "ftp://example.com", wantErr: true},
		{name: "empty hostname", baseURL: "https://:443", wantErr: true},
		{name: "relative", baseURL: "example.com", wantErr: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(Config{
				BaseURL: test.baseURL, APIKey: testAdminKey,
				AllowPrivateHTTP: test.allowHTTP,
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("NewClient() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestClientSendsAdminKeyAndChecksVersion(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/system/version" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != testAdminKey {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("unexpected Authorization header %q", got)
		}
		writeEnvelope(t, w, map[string]any{"version": "v0.1.183"})
	}))
	defer server.Close()

	client := testClient(t, server.URL, Config{})
	version, err := client.CheckVersion(context.Background())
	if err != nil {
		t.Fatalf("CheckVersion() error = %v", err)
	}
	if version.Major != 0 || version.Minor != 1 || version.Patch != 183 {
		t.Fatalf("version = %#v", version)
	}
}

func TestCheckVersionRejectsOlderRelease(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEnvelope(t, w, map[string]any{"version": "0.1.182"})
	}))
	defer server.Close()

	_, err := testClient(t, server.URL, Config{}).CheckVersion(context.Background())
	var incompatible *IncompatibleVersionError
	if !errors.As(err, &incompatible) {
		t.Fatalf("error = %v, want IncompatibleVersionError", err)
	}
}

func TestUpstreamErrorRedactsAdminKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": http.StatusUnauthorized, "message": "invalid key " + testAdminKey,
		})
	}))
	defer server.Close()

	_, err := testClient(t, server.URL, Config{}).GetVersion(context.Background())
	if err == nil {
		t.Fatal("GetVersion() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), testAdminKey) {
		t.Fatalf("error leaked admin key: %v", err)
	}
	var upstreamError *UpstreamError
	if !errors.As(err, &upstreamError) || !strings.Contains(upstreamError.Message, "[redacted]") {
		t.Fatalf("error = %#v, want redacted UpstreamError", err)
	}
}

func TestListUsersPaginates(t *testing.T) {
	t.Parallel()

	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		page := r.URL.Query().Get("page")
		pageID := int64(0)
		_, _ = fmt.Sscanf(page, "%d", &pageID)
		items := []map[string]any{{
			"id": pageID, "email": "owner" + page + "@example.com", "username": "owner" + page,
			"role": "admin", "status": "active",
		}}
		writePage(t, w, page, 2, 2, items)
	}))
	defer server.Close()

	users, err := testClient(t, server.URL, Config{PageSize: 1}).ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if len(users) != 2 || users[0].ID != 1 || users[1].ID != 2 {
		t.Fatalf("users = %#v", users)
	}
	if !reflect.DeepEqual(pages, []string{"1", "2"}) {
		t.Fatalf("pages = %v", pages)
	}
}

func TestListAPIKeysPaginatesAndSanitizes(t *testing.T) {
	t.Parallel()

	rawKey := "sk-distribution-sentinel-abcd"
	lastIP := "203.0.113.77"
	updatedAt := "2026-08-31T10:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/admin/users/7/api-keys" {
			t.Errorf("path = %q", r.URL.Path)
		}
		page, _ := time.ParseDuration(r.URL.Query().Get("page") + "s")
		id := int64(page / time.Second)
		item := map[string]any{
			"id": id, "user_id": 7, "key": rawKey, "last_used_ip": lastIP,
			"name": fmt.Sprintf("key-%d", id), "status": "active",
			"rate_limit_5h": 10.0, "usage_5h": 3.0, "reset_5h_at": "2026-08-31T12:00:00Z",
			"rate_limit_7d": 100.0, "usage_7d": 30.0, "reset_7d_at": "2026-09-07T00:00:00Z",
			"updated_at": updatedAt,
		}
		writePage(t, w, r.URL.Query().Get("page"), 2, 2, []map[string]any{item})
	}))
	defer server.Close()

	keys, err := testClient(t, server.URL, Config{PageSize: 1}).ListAPIKeys(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListAPIKeys() error = %v", err)
	}
	if len(keys) != 2 || keys[0].MaskedKey != "sk-…abcd" || !keys[0].HasRequiredLimits() {
		t.Fatalf("keys = %#v", keys)
	}
	if keys[0].SourceUpdatedAt == nil || keys[0].SourceUpdatedAt.Format(time.RFC3339) != updatedAt {
		t.Fatalf("source updated at = %v", keys[0].SourceUpdatedAt)
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{rawKey, lastIP} {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("safe API key output leaked %q: %s", sentinel, encoded)
		}
	}
}

func TestResetAPIKeyRateLimitUsageUsesAdminEndpointAndNarrowResult(t *testing.T) {
	t.Parallel()

	rawKey := "sk-reset-response-sentinel-abcd"
	lastIP := "203.0.113.88"
	updatedAt := "2026-08-31T11:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/admin/api-keys/42" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != testAdminKey {
			t.Errorf("x-api-key = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !reflect.DeepEqual(request, map[string]any{"reset_rate_limit_usage": true}) {
			t.Errorf("request body = %#v", request)
		}
		writeEnvelope(t, w, map[string]any{
			"api_key": map[string]any{
				"id": 42, "key": rawKey, "last_used_ip": lastIP,
				"usage_5h": 0, "usage_7d": 0,
				"reset_5h_at": nil, "reset_7d_at": nil, "updated_at": updatedAt,
			},
			"auto_granted_group_access": false,
		})
	}))
	defer server.Close()

	result, err := testClient(t, server.URL, Config{}).ResetAPIKeyRateLimitUsage(context.Background(), 42)
	if err != nil {
		t.Fatalf("ResetAPIKeyRateLimitUsage() error = %v", err)
	}
	if result.ID != 42 || result.Usage5h != 0 || result.Usage7d != 0 || result.UpdatedAt == nil {
		t.Fatalf("result = %#v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{rawKey, lastIP} {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("safe reset output leaked %q: %s", sentinel, encoded)
		}
	}
}

func TestResetAPIKeyRateLimitUsageValidatesIDAndResponse(t *testing.T) {
	t.Parallel()

	client := testClient(t, "http://127.0.0.1:1", Config{})
	if _, err := client.ResetAPIKeyRateLimitUsage(context.Background(), 0); err == nil {
		t.Fatal("ResetAPIKeyRateLimitUsage() accepted a non-positive ID")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEnvelope(t, w, map[string]any{"api_key": map[string]any{"id": 41}})
	}))
	defer server.Close()
	_, err := testClient(t, server.URL, Config{}).ResetAPIKeyRateLimitUsage(context.Background(), 42)
	var schemaError *SchemaError
	if !errors.As(err, &schemaError) {
		t.Fatalf("error = %v, want SchemaError", err)
	}
}

func TestListAccountsPaginatesAndAllowlistsFields(t *testing.T) {
	t.Parallel()

	credentialSecret := "account-access-token-sentinel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pageID := int64(0)
		_, _ = fmt.Sscanf(page, "%d", &pageID)
		item := map[string]any{
			"id": pageID, "name": "pool-" + page, "platform": "openai", "type": "oauth",
			"status": "active", "schedulable": true,
			"credentials": map[string]any{
				"email": "pool" + page + "@example.com", "plan_type": "pro", "access_token": credentialSecret,
			},
			"extra": map[string]any{"private_data": credentialSecret},
		}
		writePage(t, w, page, 2, 2, []map[string]any{item})
	}))
	defer server.Close()

	accounts, err := testClient(t, server.URL, Config{PageSize: 1}).ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts() error = %v", err)
	}
	if len(accounts) != 2 || accounts[0].Email != "pool1@example.com" || accounts[0].PlanType != "pro" {
		t.Fatalf("accounts = %#v", accounts)
	}
	encoded, err := json.Marshal(accounts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), credentialSecret) {
		t.Fatalf("safe account output leaked credentials: %s", encoded)
	}
}

func TestBatchAccountUsageKeepsNullableWindowsAndPartialErrors(t *testing.T) {
	t.Parallel()

	resetAt := "2026-09-01T00:00:00Z"
	updatedAt := "2026-08-31T10:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/admin/accounts/usage/batch" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			AccountIDs []int64 `json:"account_ids"`
			Force      *bool   `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Force == nil || *request.Force {
			t.Errorf("force = %v, want explicit false", request.Force)
		}
		if !reflect.DeepEqual(request.AccountIDs, []int64{1, 2, 3}) {
			t.Errorf("account_ids = %v", request.AccountIDs)
		}
		writeEnvelope(t, w, map[string]any{
			"usage": map[string]any{
				"1": map[string]any{
					"source": "passive", "updated_at": updatedAt,
					"five_hour": nil,
					"seven_day": map[string]any{"utilization": 41.5, "resets_at": resetAt, "remaining_seconds": 300},
				},
				"2": map[string]any{
					"source":    "passive",
					"five_hour": map[string]any{"utilization": 8.0, "resets_at": nil, "remaining_seconds": 0},
					"seven_day": nil,
				},
			},
			"errors": map[string]string{"3": "provider unavailable"},
		})
	}))
	defer server.Close()

	result, err := testClient(t, server.URL, Config{}).BatchAccountUsage(context.Background(), []int64{1, 2, 2, 3})
	if err != nil {
		t.Fatalf("BatchAccountUsage() error = %v", err)
	}
	if result.Usage[1].FiveHour != nil || result.Usage[1].SevenDay == nil {
		t.Fatalf("account 1 usage = %#v", result.Usage[1])
	}
	if result.Usage[1].SevenDay.Utilization != 41.5 || result.Usage[1].SevenDay.ResetsAt == nil {
		t.Fatalf("account 1 seven day = %#v", result.Usage[1].SevenDay)
	}
	if result.Usage[2].FiveHour == nil || result.Usage[2].SevenDay != nil {
		t.Fatalf("account 2 usage = %#v", result.Usage[2])
	}
	if result.Errors[3] != "provider unavailable" {
		t.Fatalf("errors = %#v", result.Errors)
	}
}

func TestClientRejectsSchemaErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "invalid JSON", body: `{`},
		{name: "missing envelope code", body: `{"message":"success","data":{"version":"0.1.183"}}`},
		{name: "missing data", body: `{"code":0,"message":"success"}`},
		{name: "wrong version shape", body: `{"code":0,"message":"success","data":{"version":7}}`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()

			_, err := testClient(t, server.URL, Config{}).GetVersion(context.Background())
			var schemaError *SchemaError
			if !errors.As(err, &schemaError) {
				t.Fatalf("error = %v, want SchemaError", err)
			}
		})
	}
}

func TestPaginationRejectsInvalidSchema(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeEnvelope(t, w, map[string]any{
			"items": nil, "total": 0, "page": 1, "page_size": 100, "pages": 1,
		})
	}))
	defer server.Close()

	_, err := testClient(t, server.URL, Config{}).ListAccounts(context.Background())
	var schemaError *SchemaError
	if !errors.As(err, &schemaError) {
		t.Fatalf("error = %v, want SchemaError", err)
	}
}

func TestClientLimitsResponseSize(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
	}))
	defer server.Close()

	client := testClient(t, server.URL, Config{MaxResponseBytes: 128})
	_, err := client.GetVersion(context.Background())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientEnforcesTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := testClient(t, server.URL, Config{Timeout: 20 * time.Millisecond})
	started := time.Now()
	_, err := client.GetVersion(context.Background())
	if err == nil {
		t.Fatal("GetVersion() unexpectedly succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request timeout took %v", elapsed)
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var targetHits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		writeEnvelope(t, w, map[string]any{"version": "0.1.183"})
	}))
	defer target.Close()

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, target.URL, http.StatusFound)
	}))
	defer source.Close()

	_, err := testClient(t, source.URL, Config{}).GetVersion(context.Background())
	if !errors.Is(err, ErrRedirectBlocked) {
		t.Fatalf("error = %v, want ErrRedirectBlocked", err)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetHits.Load())
	}
}

func TestMaskAPIKeyNeverExposesShortMaterial(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":              "",
		"x":             "…",
		"abcd":          "…",
		"sk-x":          "sk-…",
		"sk-abcd":       "sk-…",
		"custom-abcdef": "…cdef",
		"sk-abcdef":     "sk-…cdef",
	}
	for raw, want := range tests {
		if got := MaskAPIKey(raw); got != want {
			t.Errorf("MaskAPIKey(%q) = %q, want %q", raw, got, want)
		}
	}
}

func testClient(t *testing.T, baseURL string, overrides Config) *Client {
	t.Helper()
	overrides.BaseURL = baseURL
	overrides.APIKey = testAdminKey
	overrides.AllowPrivateHTTP = true
	client, err := NewClient(overrides)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"code": 0, "message": "success", "data": data,
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writePage(t *testing.T, w http.ResponseWriter, page string, total, pages int, items any) {
	t.Helper()
	pageNumber := 0
	_, _ = fmt.Sscanf(page, "%d", &pageNumber)
	writeEnvelope(t, w, map[string]any{
		"items": items, "total": total, "page": pageNumber, "page_size": 1, "pages": pages,
	})
}
