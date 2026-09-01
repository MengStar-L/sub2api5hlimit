package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/releasecheck"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/sub2api"
)

func TestBatchQuotaResetLifecycleConcurrencyAndSkips(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}

	users := make([]store.User, 0, 9)
	for index := 1; index <= 9; index++ {
		var snapshot *store.KeySnapshot
		if index != 6 {
			value := store.KeySnapshot{
				UpstreamKeyID: int64(2000 + index), Name: fmt.Sprintf("batch-key-%d", index),
				Mask: fmt.Sprintf("sk-…%04d", index), Status: "active",
				RateLimit5h: 10, Usage5h: float64(index), RateLimit7d: 100, Usage7d: float64(index * 10),
			}
			snapshot = &value
		}
		user, createErr := fixture.store.CreateUser(
			context.Background(), fmt.Sprintf("batch-user-%d", index), fmt.Sprintf("Batch User %d", index),
			"test-hash", snapshot, admin.ID,
		)
		if createErr != nil {
			t.Fatalf("CreateUser(%d): %v", index, createErr)
		}
		if index == 2 || index == 6 {
			if updateErr := fixture.store.UpdateUser(context.Background(), user.ID, user.DisplayName, store.StatusDisabled, admin.ID); updateErr != nil {
				t.Fatalf("disable user %d: %v", index, updateErr)
			}
			user.Status = store.StatusDisabled
		}
		users = append(users, user)
	}

	started := make(chan struct{}, 4)
	release := make(chan struct{})
	var concurrencyMu sync.Mutex
	active, maximum := 0, 0
	fixture.upstream.mu.Lock()
	fixture.upstream.syncErr = fmt.Errorf("snapshot refresh unavailable")
	fixture.upstream.resetFn = func(ctx context.Context, keyID int64) (QuotaResetResult, error) {
		concurrencyMu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		concurrencyMu.Unlock()
		defer func() {
			concurrencyMu.Lock()
			active--
			concurrencyMu.Unlock()
		}()
		if keyID >= 2001 && keyID <= 2004 {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return QuotaResetResult{UpstreamKeyID: keyID}, ctx.Err()
			}
		}
		if keyID == 2007 {
			return QuotaResetResult{UpstreamKeyID: keyID}, fmt.Errorf("upstream rejected reset")
		}
		if keyID == 2009 {
			return QuotaResetResult{UpstreamKeyID: keyID}, context.Canceled
		}
		return QuotaResetResult{UpstreamKeyID: keyID, Applied: true}, nil
	}
	fixture.upstream.mu.Unlock()

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "all_non_deleted",
	}, fixture.server.URL, "")
	assertAPIError(t, status, body, http.StatusForbidden, "CSRF_REJECTED")

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "selected_users",
	}, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusBadRequest, "INVALID_RESET_SCOPE")

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "all_non_deleted", "unexpected": true,
	}, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusBadRequest, "INVALID_JSON")

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "all_non_deleted",
	}, fixture.server.URL, csrf)
	if status != http.StatusAccepted {
		t.Fatalf("POST batch quota reset = %d, body = %s", status, body)
	}
	var created struct {
		Data struct {
			store.QuotaResetJob
			JobID int64 `json:"job_id"`
		} `json:"data"`
	}
	decodeResponse(t, body, &created)
	if created.Data.ID <= 0 || created.Data.JobID != created.Data.ID || created.Data.TotalCount != 9 || created.Data.Status != store.QuotaResetJobQueued {
		t.Fatalf("created quota reset job = %#v", created.Data)
	}

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "all_non_deleted",
	}, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusConflict, "QUOTA_RESET_ALREADY_RUNNING")

	for index := 0; index < 4; index++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for four quota reset workers")
		}
	}
	status, body, _ = fixture.request(t, adminClient, http.MethodGet, "/api/admin/quota-resets/current", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("GET current quota reset = %d, body = %s", status, body)
	}
	var current struct {
		Data store.QuotaResetJob `json:"data"`
	}
	decodeResponse(t, body, &current)
	if current.Data.ID != created.Data.ID || current.Data.Status != store.QuotaResetJobRunning || current.Data.RunningCount != 4 {
		t.Fatalf("current quota reset job = %#v", current.Data)
	}

	replacement := store.KeySnapshot{
		UpstreamKeyID: 9005, Name: "replacement", Mask: "sk-…9005", Status: "active",
		RateLimit5h: 10, RateLimit7d: 100,
	}
	if err := fixture.store.SetBinding(context.Background(), users[4].ID, replacement, admin.ID); err != nil {
		t.Fatalf("replace queued binding: %v", err)
	}
	if err := fixture.store.DeleteUser(context.Background(), users[7].ID, admin.ID); err != nil {
		t.Fatalf("delete queued user: %v", err)
	}
	close(release)

	var finished store.QuotaResetJob
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, body, _ = fixture.request(t, adminClient, http.MethodGet, fmt.Sprintf("/api/admin/quota-resets/%d", created.Data.ID), nil, "", "")
		if status != http.StatusOK {
			t.Fatalf("GET quota reset job = %d, body = %s", status, body)
		}
		var response struct {
			Data store.QuotaResetJob `json:"data"`
		}
		decodeResponse(t, body, &response)
		finished = response.Data
		if finished.Status == store.QuotaResetJobCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finished.Status != store.QuotaResetJobCompleted {
		t.Fatalf("quota reset job did not complete: %#v", finished)
	}
	if finished.TotalCount != 9 || finished.SucceededCount != 4 || finished.FailedCount != 1 ||
		finished.UnknownCount != 1 || finished.SkippedCount != 3 || finished.PendingCount != 0 || finished.RunningCount != 0 {
		t.Fatalf("finished quota reset counters = %#v", finished)
	}
	byUser := make(map[int64]store.QuotaResetJobItem, len(finished.Items))
	for _, item := range finished.Items {
		byUser[item.UserID] = item
	}
	if byUser[users[1].ID].Status != store.QuotaResetItemSucceeded {
		t.Fatalf("disabled bound user was not reset: %#v", byUser[users[1].ID])
	}
	if item := byUser[users[4].ID]; item.Status != store.QuotaResetItemSkipped || item.ErrorCode != "BINDING_CHANGED" {
		t.Fatalf("rebound user item = %#v", item)
	}
	if item := byUser[users[5].ID]; item.Status != store.QuotaResetItemSkipped || item.ErrorCode != "USER_UNBOUND" {
		t.Fatalf("unbound user item = %#v", item)
	}
	if item := byUser[users[6].ID]; item.Status != store.QuotaResetItemFailed || item.ErrorCode != "UPSTREAM_ERROR" {
		t.Fatalf("failed user item = %#v", item)
	}
	if item := byUser[users[7].ID]; item.Status != store.QuotaResetItemSkipped || item.ErrorCode != "USER_DELETED" {
		t.Fatalf("deleted user item = %#v", item)
	}
	if item := byUser[users[8].ID]; item.Status != store.QuotaResetItemUnknown || item.ErrorCode != "UPSTREAM_CANCELED" {
		t.Fatalf("canceled user item = %#v", item)
	}
	fixture.upstream.mu.Lock()
	canceledCalls := 0
	for _, keyID := range fixture.upstream.resets {
		if keyID == 2009 {
			canceledCalls++
		}
	}
	fixture.upstream.mu.Unlock()
	if canceledCalls != 1 {
		t.Fatalf("canceled batch reset calls = %d, want exactly one", canceledCalls)
	}
	concurrencyMu.Lock()
	observedMaximum := maximum
	concurrencyMu.Unlock()
	if observedMaximum != 4 {
		t.Fatalf("maximum quota reset concurrency = %d, want 4", observedMaximum)
	}
	fixture.upstream.waitForSync(t, "keys")
	select {
	case unexpected := <-fixture.upstream.syncs:
		t.Fatalf("batch performed an extra Sync(%q)", unexpected)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSingleQuotaResetTimeoutIsUnknownAndNotRetried(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.KeySnapshot{
		UpstreamKeyID: 7001, Name: "timeout-key", Mask: "sk-…7001", Status: "active",
		RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20,
	}
	user, err := fixture.store.CreateUser(context.Background(), "timeout-user", "Timeout User", "hash", &snapshot, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	fixture.upstream.mu.Lock()
	fixture.upstream.resetFn = func(context.Context, int64) (QuotaResetResult, error) {
		calls.Add(1)
		return QuotaResetResult{UpstreamKeyID: 7001}, context.DeadlineExceeded
	}
	fixture.upstream.mu.Unlock()

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/quota-reset", user.ID), nil, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusGatewayTimeout, "QUOTA_RESET_UNKNOWN")
	gotCalls := calls.Load()
	if gotCalls != 1 {
		t.Fatalf("timeout reset calls = %d, want exactly one", gotCalls)
	}
	select {
	case unexpected := <-fixture.upstream.syncs:
		t.Fatalf("unknown reset unexpectedly performed Sync(%q)", unexpected)
	default:
	}
}

func TestSingleQuotaResetRefreshFailureKeepsConfirmedResetSucceeded(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.KeySnapshot{
		UpstreamKeyID: 7051, Name: "refresh-failure-key", Mask: "sk-…7051", Status: "active",
		RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20,
	}
	user, err := fixture.store.CreateUser(context.Background(), "refresh-failure-user", "Refresh Failure User", "hash", &snapshot, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.upstream.mu.Lock()
	fixture.upstream.syncErr = fmt.Errorf("snapshot refresh failed")
	fixture.upstream.resetFn = func(context.Context, int64) (QuotaResetResult, error) {
		now := time.Now().Unix()
		if err := fixture.store.ApplyQuotaResetSnapshot(context.Background(), 7051, 0, 0, nil, nil, &now, now); err != nil {
			return QuotaResetResult{UpstreamKeyID: 7051, Applied: true}, err
		}
		return QuotaResetResult{UpstreamKeyID: 7051, Applied: true, SnapshotUpdated: true}, nil
	}
	fixture.upstream.mu.Unlock()

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/quota-reset", user.ID), nil, fixture.server.URL, csrf)
	if status != http.StatusOK {
		t.Fatalf("single reset with refresh failure = %d, body = %s", status, body)
	}
	var response struct {
		Data quotaResetResponse `json:"data"`
	}
	decodeResponse(t, body, &response)
	if response.Data.Status != store.QuotaResetItemSucceeded || response.Data.SnapshotUpdated ||
		response.Data.WarningCode != "SNAPSHOT_REFRESH_FAILED" || !response.Data.Snapshot.Stale {
		t.Fatalf("single reset refresh-failure response = %#v", response.Data)
	}
	binding, err := fixture.store.BindingByUser(context.Background(), user.ID)
	if err != nil || binding.Usage5h != 0 || binding.Usage7d != 0 || binding.Reset5hAt != nil || binding.Reset7dAt != nil {
		t.Fatalf("confirmed reset snapshot was not preserved after full sync failure: %+v, err=%v", binding, err)
	}
	fixture.upstream.waitForSync(t, "keys")
}

func TestBindingMutationWaitsForValidatedQuotaReset(t *testing.T) {
	fixture := newHTTPFixture(t)
	initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	original := store.KeySnapshot{
		UpstreamKeyID: 7061, Name: "original-key", Mask: "sk-…7061", Status: "active",
		RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20,
	}
	replacement := store.KeySnapshot{
		UpstreamKeyID: 7062, Name: "replacement-key", Mask: "sk-…7062", Status: "active",
		RateLimit5h: 20, RateLimit7d: 200,
	}
	user, err := fixture.store.CreateUser(context.Background(), "linear-user", "Linear User", "hash", &original, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.upstream.mu.Lock()
	fixture.upstream.keys = append(fixture.upstream.keys, replacement)
	resetStarted := make(chan struct{})
	releaseReset := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseReset) }) }
	defer release()
	fixture.upstream.resetFn = func(ctx context.Context, keyID int64) (QuotaResetResult, error) {
		close(resetStarted)
		select {
		case <-releaseReset:
			return QuotaResetResult{UpstreamKeyID: keyID, Applied: true}, nil
		case <-ctx.Done():
			return QuotaResetResult{UpstreamKeyID: keyID}, ctx.Err()
		}
	}
	fixture.upstream.mu.Unlock()

	adminSession := store.Session{User: store.User{ID: admin.ID, Role: store.RoleAdmin}}
	resetDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/api/admin/users/quota-reset", nil)
		request.SetPathValue("id", fmt.Sprint(user.ID))
		request = request.WithContext(context.WithValue(request.Context(), sessionKey, adminSession))
		recorder := httptest.NewRecorder()
		fixture.api.resetUserQuota(recorder, request)
		resetDone <- recorder
	}()
	select {
	case <-resetStarted:
	case <-time.After(time.Second):
		t.Fatal("quota reset did not reach the upstream call")
	}

	bindingDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPut, "/api/admin/users/binding", strings.NewReader(`{"upstream_key_id":7062}`))
		request.Header.Set("Content-Type", "application/json")
		request.SetPathValue("id", fmt.Sprint(user.ID))
		request = request.WithContext(context.WithValue(request.Context(), sessionKey, adminSession))
		recorder := httptest.NewRecorder()
		fixture.api.setBinding(recorder, request)
		bindingDone <- recorder
	}()
	select {
	case recorder := <-bindingDone:
		t.Fatalf("binding mutation completed during validated reset: %d %s", recorder.Code, recorder.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	release()

	select {
	case recorder := <-resetDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("quota reset response = %d %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("quota reset did not finish")
	}
	select {
	case recorder := <-bindingDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("binding response = %d %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("binding mutation remained blocked after reset")
	}
	binding, err := fixture.store.BindingByUser(context.Background(), user.ID)
	if err != nil || binding.UpstreamKeyID != replacement.UpstreamKeyID {
		t.Fatalf("binding after reset = %+v, err=%v", binding, err)
	}
}

func TestConnectionRotationPreventsBindingFromOldKeyCache(t *testing.T) {
	fixture := newHTTPFixture(t)
	initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	rotationStarted := make(chan struct{})
	releaseRotation := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseRotation) }) }
	defer release()
	fixture.upstream.mu.Lock()
	fixture.upstream.rotationStarted = rotationStarted
	fixture.upstream.rotationRelease = releaseRotation
	fixture.upstream.clearKeysOnRotate = true
	fixture.upstream.mu.Unlock()
	adminSession := store.Session{User: store.User{ID: admin.ID, Role: store.RoleAdmin}}

	rotationDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPut, "/api/admin/settings", strings.NewReader(`{
			"base_url":"https://replacement-sub2api.example.com",
			"owner_user_id":42,
			"confirm_non_simple":true
		}`))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(context.WithValue(request.Context(), sessionKey, adminSession))
		recorder := httptest.NewRecorder()
		fixture.api.putSettings(recorder, request)
		rotationDone <- recorder
	}()
	select {
	case <-rotationStarted:
	case <-time.After(time.Second):
		t.Fatal("connection rotation did not acquire its binding barrier")
	}

	createDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(`{
			"username":"old-cache-user",
			"display_name":"Old Cache User",
			"password":"Alice password 123!",
			"upstream_key_id":1001
		}`))
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(context.WithValue(request.Context(), sessionKey, adminSession))
		recorder := httptest.NewRecorder()
		fixture.api.createUser(recorder, request)
		createDone <- recorder
	}()
	select {
	case recorder := <-createDone:
		t.Fatalf("user binding completed inside connection rotation: %d %s", recorder.Code, recorder.Body.String())
	case <-time.After(50 * time.Millisecond):
	}
	release()

	select {
	case recorder := <-rotationDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("connection rotation = %d %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("connection rotation did not finish")
	}
	select {
	case recorder := <-createDone:
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"INVALID_UPSTREAM_KEY"`) {
			t.Fatalf("old cache create response = %d %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("old cache create remained blocked")
	}
	if _, err := fixture.store.UserByUsername(context.Background(), "old-cache-user"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old cache user was persisted: %v", err)
	}
}

func TestSettingsRequestsSerializeConnectionIdentityDecisions(t *testing.T) {
	fixture := newHTTPFixture(t)
	initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := fixture.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstProbeStarted := make(chan struct{})
	releaseFirstProbe := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFirstProbe) }) }
	defer release()
	var probeMu sync.Mutex
	probeCalls := 0
	fixture.upstream.mu.Lock()
	fixture.upstream.probeFn = func(ctx context.Context, settings store.Settings, _ bool) (ProbeResult, error) {
		probeMu.Lock()
		probeCalls++
		call := probeCalls
		probeMu.Unlock()
		if call == 1 {
			close(firstProbeStarted)
			select {
			case <-releaseFirstProbe:
			case <-ctx.Done():
				return ProbeResult{}, ctx.Err()
			}
		}
		return ProbeResult{Version: "v0.1.183", Owners: []Owner{{ID: 42, Username: "distribution-owner"}}}, nil
	}
	fixture.upstream.mu.Unlock()
	adminSession := store.Session{User: store.User{ID: admin.ID, Role: store.RoleAdmin}}

	callSettings := func(baseURL string) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			body := fmt.Sprintf(`{"base_url":%q,"owner_user_id":42,"confirm_non_simple":true}`, baseURL)
			request := httptest.NewRequest(http.MethodPut, "/api/admin/settings", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(context.WithValue(request.Context(), sessionKey, adminSession))
			recorder := httptest.NewRecorder()
			fixture.api.putSettings(recorder, request)
			done <- recorder
		}()
		return done
	}
	unchangedDone := callSettings(initial.BaseURL)
	select {
	case <-firstProbeStarted:
	case <-time.After(time.Second):
		t.Fatal("first settings probe did not start")
	}
	rotatedURL := "https://serialized-sub2api.example.com"
	rotationDone := callSettings(rotatedURL)
	time.Sleep(50 * time.Millisecond)
	probeMu.Lock()
	callsWhileBlocked := probeCalls
	probeMu.Unlock()
	if callsWhileBlocked != 1 {
		t.Fatalf("concurrent settings request bypassed serialization: probe calls=%d", callsWhileBlocked)
	}
	release()
	for name, done := range map[string]<-chan *httptest.ResponseRecorder{"unchanged": unchangedDone, "rotation": rotationDone} {
		select {
		case recorder := <-done:
			if recorder.Code != http.StatusOK {
				t.Fatalf("%s settings response = %d %s", name, recorder.Code, recorder.Body.String())
			}
		case <-time.After(time.Second):
			t.Fatalf("%s settings request did not finish", name)
		}
	}
	finalSettings, err := fixture.store.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if finalSettings.BaseURL != rotatedURL || finalSettings.ConnectionUUID == initial.ConnectionUUID {
		t.Fatalf("final connection identity is inconsistent: initial=%+v final=%+v", initial, finalSettings)
	}
	fixture.upstream.mu.Lock()
	rotations := fixture.upstream.clears
	fixture.upstream.mu.Unlock()
	if rotations != 1 {
		t.Fatalf("connection rotation barriers = %d, want 1", rotations)
	}
}

func TestBatchQuotaResetRejectsActiveProgramUpdate(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	fixture.api.SetUpdateManager(&fakeUpdateManager{view: releasecheck.View{Operation: &releasecheck.OperationStatus{
		OperationID: "active-update", TargetVersion: "v0.2.1", State: "running", Phase: "downloading",
	}}})

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "all_non_deleted",
	}, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusConflict, "UPDATE_IN_PROGRESS")
	if _, err := fixture.store.CurrentQuotaResetJob(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("batch job was created during program update: %v", err)
	}
}

func TestBatchQuotaResetRejectsPendingRollbackRecovery(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	fixture.api.SetUpdateManager(&fakeUpdateManager{view: releasecheck.View{Operation: &releasecheck.OperationStatus{
		OperationID: "failed-update", TargetVersion: "v0.2.1", State: "failed", Phase: "rollback_failed", ErrorCode: "ROLLBACK_FAILED",
	}}})

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/quota-resets", map[string]any{
		"scope": "all_non_deleted",
	}, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusConflict, "UPDATE_IN_PROGRESS")
	if _, err := fixture.store.CurrentQuotaResetJob(context.Background()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("batch job was created while rollback recovery was pending: %v", err)
	}
}

func TestSingleQuotaResetRejectsActiveProgramUpdate(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.KeySnapshot{
		UpstreamKeyID: 7071, Name: "blocked-key", Mask: "sk-…7071", Status: "active",
		RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20,
	}
	user, err := fixture.store.CreateUser(context.Background(), "blocked-user", "Blocked User", "hash", &snapshot, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.SetUpdateManager(&fakeUpdateManager{view: releasecheck.View{Operation: &releasecheck.OperationStatus{
		OperationID: "queued-update", TargetVersion: "v0.2.1", State: "queued", Phase: "queued",
	}}})

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/quota-reset", user.ID), nil, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusConflict, "UPDATE_IN_PROGRESS")
	fixture.upstream.mu.Lock()
	resetCalls := len(fixture.upstream.resets)
	fixture.upstream.mu.Unlock()
	if resetCalls != 0 {
		t.Fatalf("single reset called upstream %d times during update", resetCalls)
	}
}

func TestSingleQuotaResetRejectsPendingRollbackRecovery(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.KeySnapshot{
		UpstreamKeyID: 7072, Name: "recovery-blocked-key", Mask: "sk-…7072", Status: "active",
		RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20,
	}
	user, err := fixture.store.CreateUser(context.Background(), "recovery-blocked", "Recovery Blocked", "hash", &snapshot, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.SetUpdateManager(&fakeUpdateManager{view: releasecheck.View{Operation: &releasecheck.OperationStatus{
		OperationID: "failed-update", TargetVersion: "v0.2.1", State: "failed", Phase: "rollback_failed", ErrorCode: "ROLLBACK_FAILED",
	}}})

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, fmt.Sprintf("/api/admin/users/%d/quota-reset", user.ID), nil, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusConflict, "UPDATE_IN_PROGRESS")
	fixture.upstream.mu.Lock()
	resetCalls := len(fixture.upstream.resets)
	fixture.upstream.mu.Unlock()
	if resetCalls != 0 {
		t.Fatalf("single reset called upstream %d times while rollback recovery was pending", resetCalls)
	}
}

func TestNewRecoversInterruptedQuotaResetJobWithoutCallingUpstream(t *testing.T) {
	fixture := newHTTPFixture(t)
	initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.KeySnapshot{
		UpstreamKeyID: 7101, Name: "restart-key", Mask: "sk-…7101", Status: "active",
		RateLimit5h: 10, Usage5h: 2, RateLimit7d: 100, Usage7d: 20,
	}
	if _, err := fixture.store.CreateUser(context.Background(), "restart-user", "Restart User", "hash", &snapshot, admin.ID); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.store.CreateQuotaResetJob(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	job, err = fixture.store.QuotaResetJobWithItems(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.MarkQuotaResetJobRunning(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.MarkQuotaResetItemRunning(context.Background(), job.ID, job.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	var resetCalls atomic.Int64
	fixture.upstream.mu.Lock()
	fixture.upstream.resetFn = func(context.Context, int64) (QuotaResetResult, error) {
		resetCalls.Add(1)
		return QuotaResetResult{Applied: true}, nil
	}
	fixture.upstream.mu.Unlock()

	if _, err := New(fixture.store, fixture.upstream, fixture.api.log, false); err != nil {
		t.Fatalf("New() recovery error = %v", err)
	}
	recovered, err := fixture.store.QuotaResetJobWithItems(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != store.QuotaResetJobCompleted || recovered.UnknownCount != 1 || recovered.RunningCount != 0 {
		t.Fatalf("recovered job = %#v", recovered)
	}
	if item := recovered.Items[0]; item.Status != store.QuotaResetItemUnknown || item.ErrorCode != "PROCESS_RESTARTED" {
		t.Fatalf("recovered item = %#v", item)
	}
	if resetCalls.Load() != 0 {
		t.Fatalf("startup recovery called upstream %d times", resetCalls.Load())
	}
}

func TestQuotaResetRunnerAbortsWhenPreflightStateWriteFails(t *testing.T) {
	fixture := newHTTPFixture(t)
	initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.KeySnapshot{
		UpstreamKeyID: 7201, Name: "abort-key", Mask: "sk-…7201", Status: "active",
		RateLimit5h: 10, RateLimit7d: 100,
	}
	if _, err := fixture.store.CreateUser(context.Background(), "abort-user", "Abort User", "hash", &snapshot, admin.ID); err != nil {
		t.Fatal(err)
	}
	job, err := fixture.store.CreateQuotaResetJob(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.MarkQuotaResetJobRunning(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}

	fixture.api.runQuotaResetJob(job.ID, admin.ID)
	aborted, err := fixture.store.QuotaResetJobWithItems(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aborted.Status != store.QuotaResetJobCompleted || aborted.UnknownCount != 1 || aborted.PendingCount != 0 || aborted.RunningCount != 0 {
		t.Fatalf("aborted runner job = %#v", aborted)
	}
	if item := aborted.Items[0]; item.Status != store.QuotaResetItemUnknown || item.ErrorCode != "STORE_ERROR" {
		t.Fatalf("aborted runner item = %#v", item)
	}
	fixture.upstream.mu.Lock()
	resetCalls := len(fixture.upstream.resets)
	fixture.upstream.mu.Unlock()
	if resetCalls != 0 {
		t.Fatalf("preflight failure called upstream %d times", resetCalls)
	}
	next, err := fixture.store.CreateQuotaResetJob(context.Background(), admin.ID)
	if err != nil {
		t.Fatalf("active job lock remained after runner abort: %v", err)
	}
	if err := fixture.store.AbortQuotaResetJob(context.Background(), next.ID, admin.ID, "TEST_CLEANUP"); err != nil {
		t.Fatal(err)
	}
}

func TestResetTerminalStateTreatsTimeoutAsUnknownWithoutRetry(t *testing.T) {
	t.Parallel()
	status, code := resetTerminalState(QuotaResetResult{UpstreamKeyID: 7}, context.DeadlineExceeded)
	if status != store.QuotaResetItemUnknown || code != "UPSTREAM_TIMEOUT" {
		t.Fatalf("timeout terminal state = %q %q", status, code)
	}
}

func TestResetTerminalStateTreatsCancellationAsUnknownWithoutRetry(t *testing.T) {
	t.Parallel()
	status, code := resetTerminalState(QuotaResetResult{UpstreamKeyID: 7}, context.Canceled)
	if status != store.QuotaResetItemUnknown || code != "UPSTREAM_CANCELED" {
		t.Fatalf("canceled terminal state = %q %q", status, code)
	}
}

func TestResetTerminalStateMapsUpstreamHTTPFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		status int
		code   string
	}{
		{status: http.StatusUnauthorized, code: "UPSTREAM_AUTH"},
		{status: http.StatusTooManyRequests, code: "UPSTREAM_RATE_LIMITED"},
		{status: http.StatusBadGateway, code: "UPSTREAM_UNAVAILABLE"},
	} {
		status, code := resetTerminalState(QuotaResetResult{UpstreamKeyID: 7}, &sub2api.UpstreamError{StatusCode: test.status})
		if status != store.QuotaResetItemFailed || code != test.code {
			t.Errorf("HTTP %d terminal state = %q %q, want failed %q", test.status, status, code, test.code)
		}
	}
}

func TestResetTerminalStateTreatsConfirmedMutationAsSucceededBeforeRefresh(t *testing.T) {
	t.Parallel()
	status, code := resetTerminalState(QuotaResetResult{UpstreamKeyID: 7, Applied: true}, nil)
	if status != store.QuotaResetItemSucceeded || code != "" {
		t.Fatalf("confirmed reset terminal state = %q %q", status, code)
	}
}

func TestUserResettableTracksUpstreamKeyExistence(t *testing.T) {
	t.Parallel()
	base := store.User{
		Role:   store.RoleUser,
		Status: store.StatusActive,
		Binding: &store.KeyBinding{
			UpstreamKeyID: 42,
			BindingState:  "healthy",
		},
	}
	for _, state := range []string{"healthy", "invalid_limits", "upstream_inactive"} {
		user := base
		binding := *base.Binding
		binding.BindingState = state
		user.Binding = &binding
		if !userResettable(user) {
			t.Fatalf("existing upstream key in state %q was not resettable", state)
		}
	}
	missing := base
	missingBinding := *base.Binding
	missingBinding.BindingState = "missing"
	missing.Binding = &missingBinding
	if userResettable(missing) {
		t.Fatal("missing upstream key was resettable")
	}
	disabled := base
	disabled.Status = store.StatusDisabled
	if !userResettable(disabled) {
		t.Fatal("disabled user with an existing upstream key was not resettable")
	}
}

func TestAdminUserViewMapsQuotaWindowsAndStaleSnapshot(t *testing.T) {
	t.Parallel()
	now := int64(1_788_134_400)
	lastSuccess := now - 10
	user := store.User{
		ID: 9, Role: store.RoleUser, Status: store.StatusActive,
		Binding: &store.KeyBinding{
			UpstreamKeyID: 81, BindingState: "healthy",
			RateLimit5h: 20, Usage5h: 5, RateLimit7d: 100, Usage7d: 25,
			LastSuccessAt: &lastSuccess,
		},
	}
	view := newAdminUserView(user, now)
	if view.FiveHour == nil || view.FiveHour.Percent != 25 || view.FiveHour.RemainingUSD != 15 || view.FiveHour.ResetAt != nil {
		t.Fatalf("5h admin window = %#v", view.FiveHour)
	}
	if view.SevenDay == nil || view.SevenDay.Percent != 25 || view.SevenDay.RemainingUSD != 75 || view.Snapshot.Stale {
		t.Fatalf("7d admin window/snapshot = %#v %#v", view.SevenDay, view.Snapshot)
	}
	user.Binding.LastErrorCode = "UPSTREAM_TIMEOUT"
	if fresh := newAdminUserView(user, now); !fresh.Snapshot.Stale {
		t.Fatal("binding with a failed last sync was presented as fresh")
	}
	user.Binding = nil
	unbound := newAdminUserView(user, now)
	if unbound.FiveHour != nil || unbound.SevenDay != nil || unbound.Resettable || !unbound.Snapshot.Stale {
		t.Fatalf("unbound admin view = %#v", unbound)
	}
}

func initializeAndLoginAdmin(t *testing.T, fixture *httpFixture) (*http.Client, string) {
	t.Helper()
	client := newCookieClient(t)
	setup := map[string]any{
		"token": fixture.api.SetupToken(), "username": "admin", "display_name": "Administrator",
		"password": testAdminPassword, "non_simple_ack": true,
		"base_url": "https://sub2api.example.com", "admin_api_key": testAdminAPIKey,
		"allow_private_http": false, "owner_user_id": 42,
	}
	status, body, _ := fixture.request(t, client, http.MethodPost, "/api/setup/complete", setup, fixture.server.URL, "")
	if status != http.StatusCreated {
		t.Fatalf("setup complete = %d, body = %s", status, body)
	}
	fixture.upstream.waitForSync(t, "all")
	status, body, _ = fixture.request(t, client, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "admin", "password": testAdminPassword,
	}, fixture.server.URL, "")
	if status != http.StatusOK {
		t.Fatalf("admin login = %d, body = %s", status, body)
	}
	return client, loginCSRF(t, body)
}
