package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transactionRunner struct {
	active     bool
	portalPath string
	database   string
	state      string
}

type stopTransitionRunner struct {
	states []string
	index  int
}

func (runner *stopTransitionRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	if name != "systemctl" {
		return "", fmt.Errorf("unexpected command %s", name)
	}
	switch args[0] {
	case "stop":
		return "", errors.New("stop command connection closed")
	case "is-active":
		state := runner.states[len(runner.states)-1]
		if runner.index < len(runner.states) {
			state = runner.states[runner.index]
			runner.index++
		}
		return state + "\n", errors.New(state)
	default:
		return "", fmt.Errorf("unexpected systemctl action %s", args[0])
	}
}

func (runner *transactionRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	if name == "systemctl" {
		switch args[0] {
		case "is-active":
			if runner.state != "" {
				return runner.state + "\n", errors.New(runner.state)
			}
			if runner.active {
				return "active\n", nil
			}
			return "inactive\n", errors.New("inactive")
		case "stop":
			runner.active = false
			return "", nil
		case "start":
			runner.active = true
			version, _ := os.ReadFile(runner.portalPath)
			if strings.TrimSpace(string(version)) == "0.2.0" {
				_ = os.WriteFile(runner.database, []byte("database changed by failed release"), 0600)
			}
			return "", nil
		}
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	kind := "portal"
	if strings.Contains(filepath.Base(name), "updater") {
		kind = "updater"
	}
	return fmt.Sprintf("sub2api-limit-%s %s (linux/amd64)\n", kind, strings.TrimSpace(string(body))), nil
}

func TestApplyTransactionRollsBackBinariesAndSQLite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	dataDir := filepath.Join(root, "data")
	backupDir := filepath.Join(root, "backups")
	for _, directory := range []string{binDir, dataDir, backupDir} {
		if err := os.Mkdir(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	portalPath := filepath.Join(binDir, "sub2api-limit-portal")
	updaterPath := filepath.Join(binDir, "sub2api-limit-updater")
	database := filepath.Join(dataDir, "app.db")
	writeTestFile(t, portalPath, "0.1.0", 0755)
	writeTestFile(t, updaterPath, "0.1.0", 0755)
	writeTestFile(t, database, "old database", 0600)
	writeTestFile(t, database+"-wal", "old wal", 0600)
	writeTestFile(t, database+"-shm", "old shm", 0600)
	candidateDir := filepath.Join(root, "candidates")
	if err := os.Mkdir(candidateDir, 0700); err != nil {
		t.Fatal(err)
	}
	candidates := extractedCandidates{Portal: filepath.Join(candidateDir, "portal"), Updater: filepath.Join(candidateDir, "updater")}
	writeTestFile(t, candidates.Portal, "0.2.0", 0700)
	writeTestFile(t, candidates.Updater, "0.2.0", 0700)

	runner := &transactionRunner{active: true, portalPath: portalPath, database: database}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		installed, _ := os.ReadFile(portalPath)
		if strings.TrimSpace(string(installed)) == "0.2.0" {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	envPath := filepath.Join(root, "portal.env")
	listen := strings.TrimPrefix(server.URL, "http://")
	writeTestFile(t, envPath, "SUB2API_LIMIT_LISTEN="+listen+"\n", 0600)
	cfg := updaterConfig{
		PortalPath:    portalPath,
		UpdaterPath:   updaterPath,
		Database:      database,
		BackupDir:     backupDir,
		EnvPath:       envPath,
		ServiceUnit:   portalServiceUnit,
		HTTPClient:    server.Client(),
		Runner:        runner,
		Now:           func() time.Time { return time.Unix(1788134400, 0) },
		Wait:          func(context.Context, time.Duration) error { return nil },
		GOARCH:        "amd64",
		ReadyAttempts: 1,
	}
	err := applyTransaction(context.Background(), cfg, candidates, "0.1.0", "0.2.0", testUpdateRequest(), func(string) error { return nil })
	var transaction *transactionError
	if !errors.As(err, &transaction) || !transaction.RolledBack || transaction.RollbackFailed {
		t.Fatalf("transaction error = %#v", err)
	}
	for path, want := range map[string]string{
		portalPath:        "0.1.0",
		updaterPath:       "0.1.0",
		database:          "old database",
		database + "-wal": "old wal",
		database + "-shm": "old shm",
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Fatalf("restored %s = %q, %v; want %q", path, body, readErr, want)
		}
	}
	if !runner.active {
		t.Fatal("previously active service was not restarted")
	}
}

func TestApplyTransactionRollsBackAfterContextCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	dataDir := filepath.Join(root, "data")
	backupDir := filepath.Join(root, "backups")
	for _, directory := range []string{binDir, dataDir, backupDir} {
		if err := os.Mkdir(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	portalPath := filepath.Join(binDir, "sub2api-limit-portal")
	updaterPath := filepath.Join(binDir, "sub2api-limit-updater")
	database := filepath.Join(dataDir, "app.db")
	writeTestFile(t, portalPath, "0.1.0", 0755)
	writeTestFile(t, updaterPath, "0.1.0", 0755)
	writeTestFile(t, database, "old database", 0600)
	candidateDir := filepath.Join(root, "candidates")
	if err := os.Mkdir(candidateDir, 0700); err != nil {
		t.Fatal(err)
	}
	candidates := extractedCandidates{Portal: filepath.Join(candidateDir, "portal"), Updater: filepath.Join(candidateDir, "updater")}
	writeTestFile(t, candidates.Portal, "0.2.0", 0700)
	writeTestFile(t, candidates.Updater, "0.2.0", 0700)

	runner := &transactionRunner{active: true, portalPath: portalPath, database: database}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	defer server.Close()
	envPath := filepath.Join(root, "portal.env")
	writeTestFile(t, envPath, "SUB2API_LIMIT_LISTEN="+strings.TrimPrefix(server.URL, "http://")+"\n", 0600)
	cfg := updaterConfig{
		PortalPath: portalPath, UpdaterPath: updaterPath, Database: database, BackupDir: backupDir,
		EnvPath: envPath, ServiceUnit: portalServiceUnit, HTTPClient: server.Client(), Runner: runner,
		Now:  func() time.Time { return time.Unix(1788134400, 0) },
		Wait: func(context.Context, time.Duration) error { return nil }, GOARCH: "amd64", ReadyAttempts: 1,
		RecoveryStopTimeout: time.Second, RecoveryFileTimeout: time.Second, RecoveryStartTimeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := applyTransaction(ctx, cfg, candidates, "0.1.0", "0.2.0", testUpdateRequest(), func(phase string) error {
		if phase == "restarting" {
			cancel()
			return ctx.Err()
		}
		return nil
	})
	var transaction *transactionError
	if !errors.As(err, &transaction) || !transaction.RolledBack || transaction.RollbackFailed {
		t.Fatalf("canceled transaction error = %#v", err)
	}
	for path, want := range map[string]string{portalPath: "0.1.0", updaterPath: "0.1.0", database: "old database"} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Fatalf("restored %s = %q, %v; want %q", path, body, readErr, want)
		}
	}
	if !runner.active {
		t.Fatal("previous service was not restarted with the uncanceled rollback context")
	}
}

func TestApplyTransactionRejectsADeactivatingPortalBeforeTouchingFiles(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	dataDir := filepath.Join(root, "data")
	backupDir := filepath.Join(root, "backups")
	for _, directory := range []string{binDir, dataDir, backupDir} {
		if err := os.Mkdir(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	portalPath := filepath.Join(binDir, "sub2api-limit-portal")
	updaterPath := filepath.Join(binDir, "sub2api-limit-updater")
	database := filepath.Join(dataDir, "app.db")
	writeTestFile(t, portalPath, "0.1.0", 0755)
	writeTestFile(t, updaterPath, "0.1.0", 0755)
	writeTestFile(t, database, "old database", 0600)
	candidateDir := filepath.Join(root, "candidates")
	if err := os.Mkdir(candidateDir, 0700); err != nil {
		t.Fatal(err)
	}
	candidates := extractedCandidates{Portal: filepath.Join(candidateDir, "portal"), Updater: filepath.Join(candidateDir, "updater")}
	writeTestFile(t, candidates.Portal, "0.2.0", 0700)
	writeTestFile(t, candidates.Updater, "0.2.0", 0700)
	runner := &transactionRunner{state: "deactivating", portalPath: portalPath, database: database}
	err := applyTransaction(context.Background(), updaterConfig{
		PortalPath: portalPath, UpdaterPath: updaterPath, Database: database, BackupDir: backupDir,
		Runner: runner, Now: time.Now, GOARCH: "amd64",
	}, candidates, "0.1.0", "0.2.0", testUpdateRequest(), func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "deactivating") {
		t.Fatalf("deactivating transaction error = %v", err)
	}
	for path, want := range map[string]string{portalPath: "0.1.0", updaterPath: "0.1.0", database: "old database"} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Fatalf("deactivating transaction changed %s = %q, %v", path, body, readErr)
		}
	}
	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("deactivating transaction created backups: %v, err=%v", entries, readErr)
	}
}

func TestStopPortalServiceWaitsForAmbiguousStopToSettle(t *testing.T) {
	t.Parallel()
	runner := &stopTransitionRunner{states: []string{"deactivating", "inactive"}}
	stopped, err := stopPortalService(context.Background(), updaterConfig{
		Runner: runner, ServiceUnit: portalServiceUnit,
		Wait: func(context.Context, time.Duration) error { return nil },
	})
	if !stopped || err == nil || !strings.Contains(err.Error(), "connection closed") {
		t.Fatalf("stopPortalService = %t, %v", stopped, err)
	}
}

func TestRecoverInterruptedTransactionRestoresBinariesAndDatabase(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	dataDir := filepath.Join(root, "data")
	backupRoot := filepath.Join(root, "backups")
	updateDir := filepath.Join(root, "update")
	backupPath := filepath.Join(backupRoot, "update-v0.2.0-crashed")
	for _, directory := range []string{binDir, dataDir, backupRoot, updateDir, backupPath} {
		if err := os.Mkdir(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	portalPath := filepath.Join(binDir, "sub2api-limit-portal")
	updaterPath := filepath.Join(binDir, "sub2api-limit-updater")
	database := filepath.Join(dataDir, "app.db")
	writeTestFile(t, portalPath, "0.1.0", 0755)
	writeTestFile(t, updaterPath, "0.1.0", 0755)
	writeTestFile(t, database, "old database", 0600)
	writeTestFile(t, database+"-wal", "old wal", 0600)

	backupSources := []struct{ source, name string }{
		{portalPath, "sub2api-limit-portal"},
		{updaterPath, "sub2api-limit-updater"},
		{database, "app.db"},
		{database + "-wal", "app.db-wal"},
		{database + "-shm", "app.db-shm"},
	}
	backups := make([]savedFile, 0, len(backupSources))
	for _, source := range backupSources {
		entry, err := saveFile(context.Background(), source.source, filepath.Join(backupPath, source.name))
		if err != nil {
			t.Fatal(err)
		}
		backups = append(backups, entry)
	}
	writeTestFile(t, portalPath, "0.2.0", 0755)
	writeTestFile(t, updaterPath, "0.1.0", 0755)
	writeTestFile(t, database, "database changed before crash", 0600)
	writeTestFile(t, database+"-shm", "new shm", 0600)

	request := testUpdateRequest()
	requestPath := filepath.Join(dataDir, "update.request")
	writeTestFile(t, requestPath, `{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.0","requested_at":1788134400}`, 0640)
	runner := &transactionRunner{portalPath: portalPath, database: database}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusOK) }))
	defer server.Close()
	envPath := filepath.Join(root, "portal.env")
	writeTestFile(t, envPath, "SUB2API_LIMIT_LISTEN="+strings.TrimPrefix(server.URL, "http://")+"\n", 0600)
	cfg := updaterConfig{
		InstallRoot: root, BinDir: binDir, PortalPath: portalPath, UpdaterPath: updaterPath,
		DataDir: dataDir, Database: database, BackupDir: backupRoot, UpdateDir: updateDir,
		RequestPath: requestPath, StatusPath: filepath.Join(updateDir, "status.json"),
		JournalPath: filepath.Join(updateDir, "transaction.json"), EnvPath: envPath,
		ServiceUnit: portalServiceUnit, HTTPClient: server.Client(), Runner: runner,
		Now:  func() time.Time { return time.Unix(1788134500, 0) },
		Wait: func(context.Context, time.Duration) error { return nil }, GOARCH: "amd64", ReadyAttempts: 1,
	}
	journal := transactionJournal{
		Schema: 1, Request: request, Phase: "backed_up", OldVersion: "0.1.0",
		WasActive: true, BackupPath: backupPath, Files: transactionFiles(backups),
	}
	if err := writeTransactionJournal(cfg, journal); err != nil {
		t.Fatal(err)
	}
	recovered, err := recoverInterruptedTransaction(context.Background(), cfg)
	if err != nil || !recovered {
		t.Fatalf("recoverInterruptedTransaction = %t, %v", recovered, err)
	}
	for path, want := range map[string]string{
		portalPath: "0.1.0", updaterPath: "0.1.0", database: "old database", database + "-wal": "old wal",
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil || string(body) != want {
			t.Fatalf("recovered %s = %q, %v; want %q", path, body, readErr, want)
		}
	}
	if _, err := os.Stat(database + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("new SHM file survived recovery: %v", err)
	}
	if !runner.active {
		t.Fatal("previously active portal was not restarted")
	}
	for _, path := range []string{cfg.JournalPath, cfg.RequestPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recovery marker %s still exists: %v", path, err)
		}
	}
	status, exists, err := readUpdateStatus(cfg.StatusPath)
	if err != nil || !exists || status.State != "failed" || status.Phase != "rolled_back" || !status.RolledBack || status.ErrorCode != "UPDATE_INTERRUPTED" {
		t.Fatalf("recovery status = %+v, exists=%t, err=%v", status, exists, err)
	}
}

func TestLocalReadyURLUsesLoopbackForWildcard(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "portal.env")
	writeTestFile(t, path, "SUB2API_LIMIT_LISTEN=0.0.0.0:2556\n", 0600)
	got, err := localReadyURL(path)
	if err != nil || got != "http://127.0.0.1:2556/readyz" {
		t.Fatalf("localReadyURL = %q, %v", got, err)
	}
}

func TestBeginUpdateOperationPersistsStatusAndKeepsRecoveryTrigger(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	updateDir := filepath.Join(root, "update")
	requestPath := filepath.Join(dataDir, "update.request")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(updateDir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := updaterConfig{
		DataDir: dataDir, RequestPath: requestPath, StatusPath: filepath.Join(updateDir, "status.json"),
		Now: func() time.Time { return time.Unix(1788134400, 0) },
	}
	request := updateRequest{Schema: 1, OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v0.2.1", RequestedAt: 1788134399}
	writeTestFile(t, requestPath, `{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.1","requested_at":1788134399}`, 0640)
	if _, err := beginUpdateOperation(cfg, request); err != nil {
		t.Fatalf("begin update operation: %v", err)
	}
	body, err := os.ReadFile(cfg.StatusPath)
	if err != nil {
		t.Fatalf("running status was not persisted: %v", err)
	}
	var status updateStatus
	if err := json.Unmarshal(body, &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "running" || status.Phase != "checking" || status.FinishedAt != nil {
		t.Fatalf("unexpected running status: %+v", status)
	}
	if _, err := os.Stat(requestPath); err != nil {
		t.Fatalf("request trigger was removed before the transaction completed: %v", err)
	}
}

func TestRunApplyReconcilesInstalledTargetWithoutGitHub(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	dataDir := filepath.Join(root, "data")
	backupDir := filepath.Join(root, "backups")
	updateDir := filepath.Join(root, "update")
	configDir := filepath.Join(root, "config")
	for _, directory := range []string{binDir, dataDir, backupDir, updateDir, configDir} {
		if err := os.Mkdir(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	portalPath := filepath.Join(binDir, "sub2api-limit-portal")
	updaterPath := filepath.Join(binDir, "sub2api-limit-updater")
	envPath := filepath.Join(configDir, "portal.env")
	requestPath := filepath.Join(dataDir, "update.request")
	writeTestFile(t, portalPath, "0.2.0", 0755)
	writeTestFile(t, updaterPath, "0.2.0", 0755)
	writeTestFile(t, envPath, "SUB2API_LIMIT_LISTEN=127.0.0.1:2556\n", 0640)
	writeTestFile(t, requestPath, `{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.0","requested_at":1788134400}`, 0640)
	runner := &transactionRunner{portalPath: portalPath}
	cfg := updaterConfig{
		InstallRoot: root, BinDir: binDir, PortalPath: portalPath, UpdaterPath: updaterPath,
		DataDir: dataDir, Database: filepath.Join(dataDir, "app.db"), BackupDir: backupDir, UpdateDir: updateDir,
		RequestPath: requestPath, StatusPath: filepath.Join(updateDir, "status.json"), JournalPath: filepath.Join(updateDir, "transaction.json"),
		LockPath: filepath.Join(updateDir, "apply.lock"), EnvPath: envPath, ServiceUnit: portalServiceUnit,
		LatestURL: "http://127.0.0.1:1/unreachable", HTTPClient: http.DefaultClient, Runner: runner,
		Now: func() time.Time { return time.Unix(1788134500, 0) }, GOOS: "linux", GOARCH: "amd64",
		RequireRoot: func() error { return nil },
	}
	if err := runApply(context.Background(), cfg, "0.2.0"); err != nil {
		t.Fatalf("runApply reconciliation: %v", err)
	}
	if _, err := os.Stat(requestPath); !os.IsNotExist(err) {
		t.Fatalf("reconciled request still exists: %v", err)
	}
	status, exists, err := readUpdateStatus(cfg.StatusPath)
	if err != nil || !exists || status.State != "succeeded" || status.Phase != "completed" {
		t.Fatalf("reconciled status = %+v, exists=%t, err=%v", status, exists, err)
	}
}

func writeTestFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func testUpdateRequest() updateRequest {
	return updateRequest{Schema: 1, OperationID: "0123456789abcdef0123456789abcdef", TargetVersion: "v0.2.0", RequestedAt: 1788134400}
}
