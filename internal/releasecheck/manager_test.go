package releasecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

func TestCheckPersistsLatestAndApplyWritesBoundedRequest(t *testing.T) {
	var fail atomic.Bool
	server := newReleaseServer(t, &fail, false)
	data := newTestStore(t)
	dataDir := t.TempDir()
	manager, err := New(data, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{
		CurrentVersion: "0.1.0", DataDir: dataDir, StatusPath: filepath.Join(dataDir, "status.json"),
		ReleaseAPI: server.URL + "/latest", HTTPClient: server.Client(), UpdaterAvailable: func() bool { return true }, UpdatePathActive: func() bool { return true },
		OS: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	view, err := manager.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if view.Status != "update_available" || !view.UpdateAvailable || !view.Compatible || view.Latest == nil || view.Latest.Version != "v0.2.0" {
		t.Fatalf("Check() view = %#v", view)
	}

	result, err := manager.Apply(context.Background(), "v0.2.0", 0)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.OperationID == "" || result.TargetVersion != "v0.2.0" || result.State != "queued" {
		t.Fatalf("Apply() = %#v", result)
	}
	requestBody, err := os.ReadFile(filepath.Join(dataDir, "update.request"))
	if err != nil {
		t.Fatal(err)
	}
	var request updateRequest
	if err := json.Unmarshal(requestBody, &request); err != nil {
		t.Fatal(err)
	}
	if request.Schema != 1 || request.OperationID != result.OperationID || request.TargetVersion != "v0.2.0" || request.RequestedAt <= 0 {
		t.Fatalf("request = %#v", request)
	}
	queuedView, err := manager.View(context.Background())
	if err != nil || queuedView.Operation == nil || queuedView.Operation.State != "queued" || queuedView.Operation.OperationID != result.OperationID {
		t.Fatalf("queued View() = %#v, %v", queuedView, err)
	}
	if _, err := manager.Apply(context.Background(), "v0.2.0", 0); err != ErrUpdatePending {
		t.Fatalf("second Apply() error = %v, want ErrUpdatePending", err)
	}

	fail.Store(true)
	view, err = manager.Check(context.Background())
	if err != nil {
		t.Fatalf("failed Check() storage error = %v", err)
	}
	if view.Status != "check_failed" || view.Latest == nil || view.Latest.Version != "v0.2.0" || view.LastErrorCode != "RELEASE_CHECK_FAILED" {
		t.Fatalf("cached failed view = %#v", view)
	}
}

func TestCheckRejectsUnknownManifestFields(t *testing.T) {
	var fail atomic.Bool
	server := newReleaseServer(t, &fail, true)
	data := newTestStore(t)
	manager, err := New(data, nil, Config{CurrentVersion: "v0.1.0", DataDir: t.TempDir(),
		ReleaseAPI: server.URL + "/latest", HTTPClient: server.Client(), UpdaterAvailable: func() bool { return true }, UpdatePathActive: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.Check(context.Background())
	if err != nil {
		t.Fatalf("Check() storage error = %v", err)
	}
	if view.Status != "check_failed" || view.Latest != nil || view.LastErrorCode != "INVALID_UPDATE_MANIFEST" {
		t.Fatalf("view = %#v", view)
	}
}

func TestDevelopmentBuildCannotApply(t *testing.T) {
	data := newTestStore(t)
	manager, err := New(data, nil, Config{CurrentVersion: "dev", DataDir: t.TempDir(), ReleaseAPI: defaultReleaseAPI,
		UpdaterAvailable: func() bool { return true }, UpdatePathActive: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	view, err := manager.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "development" || view.Current.Version != "dev" || view.UpdateAvailable {
		t.Fatalf("view = %#v", view)
	}
}

func TestCanApplyRequiresUpdaterAndActiveSystemdPath(t *testing.T) {
	data := newTestStore(t)
	manager, err := New(data, nil, Config{
		CurrentVersion: "v0.2.0", DataDir: t.TempDir(), ReleaseAPI: defaultReleaseAPI,
		OS: "linux", Arch: "amd64", UpdaterAvailable: func() bool { return true },
		UpdatePathActive: func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.canApply() {
		t.Fatal("automatic updates were reported available while the systemd path watcher was inactive")
	}
	manager.updatePathActive = func() bool { return true }
	if !manager.canApply() {
		t.Fatal("automatic updates were unavailable with both binaries and watcher ready")
	}
}

func TestStableReleaseVersionAndDownloadHosts(t *testing.T) {
	for _, valid := range []string{"v0.2.0", "v1.12.3"} {
		if _, err := stableReleaseVersion(valid); err != nil {
			t.Errorf("stableReleaseVersion(%q) error = %v", valid, err)
		}
	}
	for _, invalid := range []string{"0.2.0", "v0.2.0-rc.1", "v1.12.3+build.4", "v01.2.3", "latest"} {
		if _, err := stableReleaseVersion(invalid); err == nil {
			t.Errorf("stableReleaseVersion(%q) unexpectedly succeeded", invalid)
		}
	}
	for _, host := range []string{"api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"} {
		if !allowedGitHubHost(host) {
			t.Errorf("allowedGitHubHost(%q) = false", host)
		}
	}
	if allowedGitHubHost("github.example.com") {
		t.Fatal("allowedGitHubHost accepted an unrelated host")
	}
	if !manifestMatchesRelease("0.2.0", "v0.2.0") {
		t.Fatal("canonical manifest version did not match its release")
	}
	for _, invalid := range []string{"v0.2.0", "0.2.0+rebuilt", "0.2.0-rc.1", "0.2.1"} {
		if manifestMatchesRelease(invalid, "v0.2.0") {
			t.Errorf("manifestMatchesRelease(%q) unexpectedly succeeded", invalid)
		}
	}
}

func TestOperationStatusPrefersMatchingUpdaterStateOverRetainedRequest(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	statusPath := filepath.Join(dataDir, "status.json")
	requestPath := filepath.Join(dataDir, "update.request")
	manager := &Manager{dataDir: dataDir, statusPath: statusPath}
	writeLocalJSON := func(path string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}

	request := updateRequest{Schema: 1, OperationID: "11111111111111111111111111111111", TargetVersion: "v0.2.1", RequestedAt: 1788134400}
	writeLocalJSON(requestPath, request)
	writeLocalJSON(statusPath, OperationStatus{
		Schema: 1, OperationID: request.OperationID, TargetVersion: request.TargetVersion,
		State: "running", Phase: "verifying",
	})
	if status := manager.readOperationStatus(); status == nil || status.State != "running" || status.Phase != "verifying" {
		t.Fatalf("matching running status = %#v", status)
	}

	writeLocalJSON(statusPath, OperationStatus{
		Schema: 1, OperationID: request.OperationID, TargetVersion: request.TargetVersion,
		State: "failed", Phase: "rollback_failed", ErrorCode: "ROLLBACK_FAILED",
	})
	if status := manager.readOperationStatus(); status == nil || status.State != "failed" || status.Phase != "rollback_failed" {
		t.Fatalf("matching rollback status = %#v", status)
	}

	writeLocalJSON(statusPath, OperationStatus{
		Schema: 1, OperationID: "22222222222222222222222222222222", TargetVersion: "v0.2.0",
		State: "succeeded", Phase: "completed",
	})
	if status := manager.readOperationStatus(); status == nil || status.State != "queued" || status.OperationID != request.OperationID {
		t.Fatalf("new request with stale terminal status = %#v", status)
	}
}

func newReleaseServer(t *testing.T, fail *atomic.Bool, unknownManifestField bool) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/latest":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"tag_name":"v0.2.0","html_url":"https://github.com/MengStar-L/sub2api5hlimit/releases/tag/v0.2.0","draft":false,"prerelease":false,"published_at":"2026-08-31T08:00:00Z","assets":[{"name":"update-manifest.json","browser_download_url":%q}]}`, server.URL+"/manifest")
		case "/manifest":
			w.Header().Set("Content-Type", "application/json")
			if unknownManifestField {
				io.WriteString(w, `{"schema":1,"version":"0.2.0","min_updater_version":"0.1.0","mode":"binary","unexpected":true}`)
				return
			}
			io.WriteString(w, `{"schema":1,"version":"0.2.0","min_updater_version":"0.1.0","mode":"binary"}`)
		default:
			http.NotFound(w, r)
		}
	})
	server = httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	box, err := secure.NewBox(bytes.Repeat([]byte{0x37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	data, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "portal.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	return data
}
