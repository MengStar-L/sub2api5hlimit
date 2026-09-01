package releasecheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/sub2api"
)

const (
	defaultReleaseAPI = "https://api.github.com/repos/MengStar-L/sub2api5hlimit/releases/latest"
	manifestName      = "update-manifest.json"
	maxReleaseBytes   = 256 << 10
	maxManifestBytes  = 16 << 10
)

var (
	ErrUpdateUnavailable = errors.New("automatic update is unavailable")
	ErrInvalidTarget     = errors.New("target is not the checked latest stable release")
	ErrUpdatePending     = errors.New("an update is already pending")
)

type Config struct {
	CurrentVersion   string
	DataDir          string
	StatusPath       string
	UpdaterPath      string
	ReleaseAPI       string
	CheckInterval    time.Duration
	HTTPClient       *http.Client
	UpdaterAvailable func() bool
	UpdatePathActive func() bool
	OS               string
	Arch             string
}

type Manager struct {
	store            *store.Store
	log              *slog.Logger
	currentVersion   string
	dataDir          string
	statusPath       string
	updaterPath      string
	releaseAPI       string
	releaseHost      string
	checkInterval    time.Duration
	httpClient       *http.Client
	updaterAvailable func() bool
	updatePathActive func() bool
	os               string
	arch             string
	mu               sync.Mutex
}

type VersionInfo struct {
	Version string `json:"version"`
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
}

type LatestRelease struct {
	Version           string `json:"version"`
	ReleaseURL        string `json:"release_url"`
	PublishedAt       *int64 `json:"published_at,omitempty"`
	Mode              string `json:"mode"`
	MinUpdaterVersion string `json:"min_updater_version"`
}

type OperationStatus struct {
	Schema        int    `json:"schema"`
	OperationID   string `json:"operation_id"`
	TargetVersion string `json:"target_version"`
	State         string `json:"state"`
	Phase         string `json:"phase"`
	ErrorCode     string `json:"error_code,omitempty"`
	StartedAt     *int64 `json:"started_at,omitempty"`
	UpdatedAt     *int64 `json:"updated_at,omitempty"`
	FinishedAt    *int64 `json:"finished_at,omitempty"`
	RolledBack    bool   `json:"rolled_back"`
}

func (o *OperationStatus) active() bool {
	if o == nil {
		return false
	}
	if o.Phase == "rollback_failed" {
		return true
	}
	switch o.State {
	case "queued", "running":
		return true
	default:
		return false
	}
}

func (o *OperationStatus) MaintenancePending() bool {
	return o.active()
}

type View struct {
	Current          VersionInfo      `json:"current"`
	Latest           *LatestRelease   `json:"latest,omitempty"`
	Status           string           `json:"status"`
	CheckedAt        *int64           `json:"checked_at,omitempty"`
	LastSuccessAt    *int64           `json:"last_success_at,omitempty"`
	LastErrorCode    string           `json:"last_error_code,omitempty"`
	UpdateAvailable  bool             `json:"update_available"`
	Compatible       bool             `json:"compatible"`
	UpdaterAvailable bool             `json:"updater_available"`
	Operation        *OperationStatus `json:"operation,omitempty"`
}

type ApplyResult struct {
	OperationID   string `json:"operation_id"`
	TargetVersion string `json:"target_version"`
	State         string `json:"state"`
}

type releaseDTO struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type manifestDTO struct {
	Schema            int    `json:"schema"`
	Version           string `json:"version"`
	MinUpdaterVersion string `json:"min_updater_version"`
	Mode              string `json:"mode"`
}

type updateRequest struct {
	Schema        int    `json:"schema"`
	OperationID   string `json:"operation_id"`
	TargetVersion string `json:"target_version"`
	RequestedAt   int64  `json:"requested_at"`
}

func New(data *store.Store, logger *slog.Logger, cfg Config) (*Manager, error) {
	if data == nil {
		return nil, errors.New("update checker store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("update request data directory is required")
	}
	if cfg.ReleaseAPI == "" {
		cfg.ReleaseAPI = defaultReleaseAPI
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = 6 * time.Hour
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	} else {
		copy := *client
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 12 * time.Second
		}
	}
	originalRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || !allowedGitHubHost(req.URL.Hostname()) {
			return errors.New("release redirect rejected")
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		return nil
	}
	if cfg.OS == "" {
		cfg.OS = runtime.GOOS
	}
	if cfg.Arch == "" {
		cfg.Arch = runtime.GOARCH
	}
	if cfg.StatusPath == "" {
		cfg.StatusPath = filepath.Join(cfg.DataDir, "update-status.json")
	}
	if cfg.UpdaterPath == "" {
		cfg.UpdaterPath = "/opt/sub2api5hlimit/bin/sub2api-limit-updater"
	}
	currentVersion := canonicalVersion(cfg.CurrentVersion)
	if currentVersion == "" {
		currentVersion = strings.TrimSpace(cfg.CurrentVersion)
		if currentVersion == "" {
			currentVersion = "dev"
		}
	}
	releaseURL, err := url.Parse(cfg.ReleaseAPI)
	if err != nil || releaseURL.Scheme != "https" || releaseURL.Hostname() == "" {
		return nil, errors.New("release API must be an absolute HTTPS URL")
	}
	m := &Manager{store: data, log: logger, currentVersion: currentVersion, dataDir: cfg.DataDir,
		statusPath: cfg.StatusPath, updaterPath: cfg.UpdaterPath, releaseAPI: cfg.ReleaseAPI,
		releaseHost:   releaseURL.Hostname(),
		checkInterval: cfg.CheckInterval, httpClient: client, updaterAvailable: cfg.UpdaterAvailable,
		updatePathActive: cfg.UpdatePathActive,
		os:               cfg.OS, arch: cfg.Arch}
	return m, nil
}

func (m *Manager) Run(ctx context.Context) {
	m.checkAndLog(ctx)
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkAndLog(ctx)
		}
	}
}

func (m *Manager) checkAndLog(ctx context.Context) {
	if _, err := m.Check(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.log.Warn("release check failed", "error", err)
	}
}

func (m *Manager) View(ctx context.Context) (View, error) {
	state, err := m.store.UpdateCheckState(ctx)
	if err != nil {
		return View{}, err
	}
	return m.viewFromState(state), nil
}

// Check persists failures as a code while preserving the previous successful
// release. Network failures are represented in the returned view, not as an
// HTTP transport error to the administrator.
func (m *Manager) Check(ctx context.Context) (View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().Unix()
	release, err := m.fetchLatest(ctx)
	if err != nil {
		code := checkErrorCode(err)
		if saveErr := m.store.SaveUpdateCheckFailure(ctx, code, now); saveErr != nil {
			return View{}, saveErr
		}
		state, loadErr := m.store.UpdateCheckState(ctx)
		if loadErr != nil {
			return View{}, loadErr
		}
		return m.viewFromState(state), nil
	}
	state := store.UpdateCheckState{LatestVersion: release.Version, ReleaseURL: release.ReleaseURL,
		PublishedAt: release.PublishedAt, Mode: release.Mode, MinUpdaterVersion: release.MinUpdaterVersion}
	if err := m.store.SaveUpdateCheckSuccess(ctx, state, now); err != nil {
		return View{}, err
	}
	state.CheckedAt, state.LastSuccessAt = &now, &now
	return m.viewFromState(state), nil
}

func (m *Manager) Apply(ctx context.Context, targetVersion string, actorID int64) (ApplyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	view, err := m.View(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if !view.UpdaterAvailable || !view.UpdateAvailable || !view.Compatible || view.Latest == nil {
		return ApplyResult{}, ErrUpdateUnavailable
	}
	if canonicalVersion(targetVersion) == "" || canonicalVersion(targetVersion) != view.Latest.Version {
		return ApplyResult{}, ErrInvalidTarget
	}
	if view.Operation.active() {
		return ApplyResult{}, ErrUpdatePending
	}
	requestPath := filepath.Join(m.dataDir, "update.request")
	if _, err := os.Lstat(requestPath); err == nil {
		return ApplyResult{}, ErrUpdatePending
	} else if !errors.Is(err, os.ErrNotExist) {
		return ApplyResult{}, err
	}
	operationID, err := randomID()
	if err != nil {
		return ApplyResult{}, err
	}
	now := time.Now().Unix()
	request := updateRequest{Schema: 1, OperationID: operationID, TargetVersion: view.Latest.Version, RequestedAt: now}
	metadata, _ := json.Marshal(map[string]string{"target_version": view.Latest.Version, "operation_id": operationID})
	if err := m.store.RecordAudit(ctx, actorID, "update.request", "release", view.Latest.Version, string(metadata)); err != nil {
		return ApplyResult{}, err
	}
	if err := writeAtomicJSON(requestPath, request, 0o600); err != nil {
		// The durable audit records a failed publication attempt too; the marker
		// is the sole trigger, so no update can start before this succeeds.
		failureMetadata, _ := json.Marshal(map[string]string{"target_version": view.Latest.Version, "operation_id": operationID, "error_code": "REQUEST_PUBLISH_FAILED"})
		_ = m.store.RecordAudit(context.WithoutCancel(ctx), actorID, "update.request_failed", "release", view.Latest.Version, string(failureMetadata))
		return ApplyResult{}, err
	}
	return ApplyResult{OperationID: operationID, TargetVersion: view.Latest.Version, State: "queued"}, nil
}

func (m *Manager) viewFromState(state store.UpdateCheckState) View {
	view := View{Current: VersionInfo{Version: m.currentVersion, OS: m.os, Arch: m.arch}, CheckedAt: state.CheckedAt,
		LastSuccessAt: state.LastSuccessAt, LastErrorCode: state.LastErrorCode, UpdaterAvailable: m.canApply(), Compatible: false}
	if state.LatestVersion != "" {
		view.Latest = &LatestRelease{Version: canonicalVersion(state.LatestVersion), ReleaseURL: state.ReleaseURL,
			PublishedAt: state.PublishedAt, Mode: state.Mode, MinUpdaterVersion: canonicalVersion(state.MinUpdaterVersion)}
	}
	view.Operation = m.readOperationStatus()
	current, currentErr := sub2api.ParseVersion(m.currentVersion)
	if currentErr != nil {
		view.Status = "development"
		return view
	}
	if view.Latest != nil {
		latest, latestErr := sub2api.ParseVersion(view.Latest.Version)
		minimum, minimumErr := sub2api.ParseVersion(view.Latest.MinUpdaterVersion)
		if latestErr == nil && minimumErr == nil {
			view.UpdateAvailable = latest.AtLeast(current) && !current.AtLeast(latest)
			view.Compatible = view.Latest.Mode == "binary" && current.AtLeast(minimum)
		}
	}
	switch {
	case state.LastErrorCode != "":
		view.Status = "check_failed"
	case view.UpdateAvailable && !view.Compatible:
		view.Status = "manual_required"
	case view.UpdateAvailable:
		view.Status = "update_available"
	default:
		view.Status = "up_to_date"
	}
	return view
}

func (m *Manager) canApply() bool {
	if m.os != "linux" {
		return false
	}
	binaryAvailable := false
	if m.updaterAvailable != nil {
		binaryAvailable = m.updaterAvailable()
	} else {
		info, err := os.Stat(m.updaterPath)
		binaryAvailable = err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0
	}
	if !binaryAvailable {
		return false
	}
	if m.updatePathActive != nil {
		return m.updatePathActive()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "sub2api-limit-portal-update.path").Run() == nil
}

func (m *Manager) readOperationStatus() *OperationStatus {
	pending := m.readPendingRequest()
	status := m.readUpdaterStatus()
	if pending != nil {
		if status != nil && status.OperationID == pending.OperationID && status.TargetVersion == pending.TargetVersion {
			return status
		}
		requestedAt := pending.RequestedAt
		return &OperationStatus{Schema: 1, OperationID: pending.OperationID, TargetVersion: pending.TargetVersion,
			State: "queued", Phase: "queued", StartedAt: &requestedAt, UpdatedAt: &requestedAt}
	}
	return status
}

func (m *Manager) readUpdaterStatus() *OperationStatus {
	file, err := os.Open(m.statusPath)
	if err != nil {
		return nil
	}
	defer file.Close()
	var status OperationStatus
	decoder := json.NewDecoder(io.LimitReader(file, 32<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil || status.Schema != 1 || status.OperationID == "" ||
		canonicalVersion(status.TargetVersion) == "" {
		return nil
	}
	switch status.State {
	case "running", "succeeded", "failed":
	default:
		return nil
	}
	return &status
}

func (m *Manager) readPendingRequest() *updateRequest {
	file, err := os.Open(filepath.Join(m.dataDir, "update.request"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var request updateRequest
	decoder := json.NewDecoder(io.LimitReader(file, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.Schema != 1 || request.OperationID == "" ||
		canonicalVersion(request.TargetVersion) == "" || request.RequestedAt <= 0 {
		return nil
	}
	return &request
}

func (m *Manager) fetchLatest(ctx context.Context) (LatestRelease, error) {
	var release releaseDTO
	if err := m.getJSON(ctx, m.releaseAPI, maxReleaseBytes, false, &release); err != nil {
		return LatestRelease{}, fmt.Errorf("release: %w", err)
	}
	version, err := stableReleaseVersion(release.TagName)
	if err != nil || release.Draft || release.Prerelease {
		return LatestRelease{}, errors.New("invalid stable release")
	}
	if !validReleasePage(release.HTMLURL, release.TagName) {
		return LatestRelease{}, errors.New("invalid release page")
	}
	var manifestURL string
	for _, asset := range release.Assets {
		if asset.Name == manifestName {
			if manifestURL != "" {
				return LatestRelease{}, errors.New("duplicate update manifest")
			}
			manifestURL = asset.BrowserDownloadURL
		}
	}
	if manifestURL == "" {
		return LatestRelease{}, errors.New("update manifest missing")
	}
	var manifest manifestDTO
	if err := m.getJSON(ctx, manifestURL, maxManifestBytes, true, &manifest); err != nil {
		return LatestRelease{}, fmt.Errorf("manifest: %w", err)
	}
	if manifest.Schema != 1 || (manifest.Mode != "binary" && manifest.Mode != "manual") {
		return LatestRelease{}, errors.New("invalid update manifest")
	}
	manifestVersion := canonicalVersion(manifest.Version)
	minimumVersion := canonicalVersion(manifest.MinUpdaterVersion)
	if manifestVersion == "" || !manifestMatchesRelease(manifest.Version, version) || minimumVersion == "" {
		return LatestRelease{}, errors.New("update manifest version mismatch")
	}
	var publishedAt *int64
	if release.PublishedAt != "" {
		value, parseErr := time.Parse(time.RFC3339, release.PublishedAt)
		if parseErr != nil {
			return LatestRelease{}, errors.New("invalid release publication time")
		}
		unix := value.Unix()
		publishedAt = &unix
	}
	return LatestRelease{Version: version, ReleaseURL: release.HTMLURL, PublishedAt: publishedAt,
		Mode: manifest.Mode, MinUpdaterVersion: minimumVersion}, nil
}

func (m *Manager) getJSON(ctx context.Context, target string, limit int64, strict bool, output any) error {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || !m.allowedHost(parsed.Hostname()) {
		return errors.New("release URL rejected")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "sub2api-limit-portal/"+m.currentVersion)
	response, err := m.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > limit {
		return errors.New("response too large")
	}
	limited := io.LimitReader(response.Body, limit+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return errors.New("response too large")
	}
	defer clear(body)
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(output); err != nil {
		return errors.New("invalid JSON response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid trailing JSON data")
	}
	return nil
}

func (m *Manager) allowedHost(host string) bool {
	return allowedGitHubHost(host) || strings.EqualFold(strings.TrimSpace(host), m.releaseHost)
}

func stableReleaseVersion(raw string) (string, error) {
	if !strings.HasPrefix(raw, "v") {
		return "", errors.New("release tag must start with v")
	}
	if strings.Contains(raw, "+") {
		return "", errors.New("release tag build metadata is not supported")
	}
	parsed, err := sub2api.ParseVersion(raw)
	if err != nil || parsed.PreRelease != "" {
		return "", errors.New("release tag must be stable semantic version")
	}
	return canonicalVersion(raw), nil
}

func manifestMatchesRelease(manifestVersion, releaseVersion string) bool {
	return manifestVersion == strings.TrimPrefix(releaseVersion, "v") && canonicalVersion(manifestVersion) == releaseVersion
}

func canonicalVersion(raw string) string {
	parsed, err := sub2api.ParseVersion(raw)
	if err != nil {
		return ""
	}
	value := fmt.Sprintf("v%d.%d.%d", parsed.Major, parsed.Minor, parsed.Patch)
	if parsed.PreRelease != "" {
		value += "-" + parsed.PreRelease
	}
	return value
}

func allowedGitHubHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "api.github.com" || host == "github.com" || host == "objects.githubusercontent.com" ||
		host == "github-releases.githubusercontent.com" || host == "release-assets.githubusercontent.com"
}

func validReleasePage(raw, tag string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Hostname(), "github.com") &&
		parsed.Path == "/MengStar-L/sub2api5hlimit/releases/tag/"+tag && parsed.RawQuery == "" && parsed.Fragment == ""
}

func checkErrorCode(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "manifest"):
		return "INVALID_UPDATE_MANIFEST"
	case strings.Contains(message, "invalid stable"), strings.Contains(message, "release page"), strings.Contains(message, "release tag"):
		return "INVALID_RELEASE"
	default:
		return "RELEASE_CHECK_FAILED"
	}
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func writeAtomicJSON(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".update-request-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(body, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
