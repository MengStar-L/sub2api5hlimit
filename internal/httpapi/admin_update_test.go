package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MengStar-L/sub2api5hlimit/internal/releasecheck"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type fakeUpdateManager struct {
	view        releasecheck.View
	applyResult releasecheck.ApplyResult
	applyErr    error
	target      string
	actorID     int64
	checks      int
}

func (f *fakeUpdateManager) View(context.Context) (releasecheck.View, error) {
	return f.view, nil
}

func (f *fakeUpdateManager) Check(context.Context) (releasecheck.View, error) {
	f.checks++
	return f.view, nil
}

func (f *fakeUpdateManager) Apply(_ context.Context, target string, actorID int64) (releasecheck.ApplyResult, error) {
	f.target, f.actorID = target, actorID
	return f.applyResult, f.applyErr
}

func TestAdminUpdateHandlers(t *testing.T) {
	manager := &fakeUpdateManager{
		view:        releasecheck.View{Current: releasecheck.VersionInfo{Version: "v0.2.0", OS: "linux", Arch: "amd64"}, Status: "up_to_date"},
		applyResult: releasecheck.ApplyResult{OperationID: "operation-1", TargetVersion: "v0.2.1", State: "queued"},
	}
	server := &Server{updates: manager}

	recorder := httptest.NewRecorder()
	server.updateStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/admin/update", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"version":"v0.2.0"`) {
		t.Fatalf("update status = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	server.checkUpdate(recorder, httptest.NewRequest(http.MethodPost, "/api/admin/update/check", nil))
	if recorder.Code != http.StatusOK || manager.checks != 1 {
		t.Fatalf("check update = %d checks=%d body=%s", recorder.Code, manager.checks, recorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/update/apply", strings.NewReader(`{"target_version":"v0.2.1"}`))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), sessionKey, store.Session{User: store.User{ID: 77, Role: store.RoleAdmin}}))
	recorder = httptest.NewRecorder()
	server.applyUpdate(recorder, request)
	if recorder.Code != http.StatusAccepted || manager.target != "v0.2.1" || manager.actorID != 77 {
		t.Fatalf("apply update = %d target=%q actor=%d body=%s", recorder.Code, manager.target, manager.actorID, recorder.Body.String())
	}
	var response struct {
		Data releasecheck.ApplyResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Data.OperationID != "operation-1" {
		t.Fatalf("apply response = %#v, %v", response, err)
	}
}

func TestApplyUpdateErrorMapping(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
		api  string
	}{
		{name: "target changed", err: releasecheck.ErrInvalidTarget, code: http.StatusConflict, api: "UPDATE_TARGET_CHANGED"},
		{name: "pending", err: releasecheck.ErrUpdatePending, code: http.StatusConflict, api: "UPDATE_IN_PROGRESS"},
		{name: "manual", err: releasecheck.ErrUpdateUnavailable, code: http.StatusConflict, api: "AUTOMATIC_UPDATE_UNAVAILABLE"},
		{name: "internal", err: errors.New("disk error"), code: http.StatusInternalServerError, api: "UPDATE_REQUEST_FAILED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{updates: &fakeUpdateManager{applyErr: test.err}}
			request := httptest.NewRequest(http.MethodPost, "/api/admin/update/apply", strings.NewReader(`{"target_version":"v0.2.1"}`))
			request.Header.Set("Content-Type", "application/json")
			request = request.WithContext(context.WithValue(request.Context(), sessionKey, store.Session{User: store.User{ID: 1}}))
			recorder := httptest.NewRecorder()
			server.applyUpdate(recorder, request)
			if recorder.Code != test.code || !strings.Contains(recorder.Body.String(), `"code":"`+test.api+`"`) {
				t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestApplyUpdateRejectsActiveQuotaResetJob(t *testing.T) {
	fixture := newHTTPFixture(t)
	adminClient, csrf := initializeAndLoginAdmin(t, fixture)
	admin, err := fixture.store.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	job, err := fixture.store.CreateQuotaResetJob(context.Background(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager := &fakeUpdateManager{applyResult: releasecheck.ApplyResult{OperationID: "must-not-run"}}
	fixture.api.SetUpdateManager(manager)

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/update/apply", map[string]any{
		"target_version": "v0.2.1",
	}, fixture.server.URL, csrf)
	assertAPIError(t, status, body, http.StatusConflict, "QUOTA_RESET_BATCH_ACTIVE")
	if manager.target != "" {
		t.Fatalf("update manager was called while batch reset job %d was active", job.ID)
	}
	if err := fixture.store.AbortQuotaResetJob(context.Background(), job.ID, admin.ID, "TEST_CLEANUP"); err != nil {
		t.Fatal(err)
	}
}
