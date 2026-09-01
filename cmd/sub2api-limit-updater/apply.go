package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var versionOutputPattern = regexp.MustCompile(`^sub2api-limit-(portal|updater) ([^ ]+) \(linux/(amd64|arm64)\)\s*$`)

type transactionError struct {
	Phase          string
	Cause          error
	RolledBack     bool
	RollbackFailed bool
}

func (err *transactionError) Error() string { return err.Cause.Error() }
func (err *transactionError) Unwrap() error { return err.Cause }

func runApply(ctx context.Context, cfg updaterConfig, currentVersion string) error {
	if cfg.RequireRoot == nil {
		return fmt.Errorf("root check is not configured")
	}
	if err := cfg.RequireRoot(); err != nil {
		return err
	}
	if err := validateManagedPaths(cfg); err != nil {
		return err
	}
	releaseLock, err := acquireProcessLock(cfg.LockPath)
	if err != nil {
		return err
	}
	defer releaseLock()

	if recovered, recoveryErr := recoverInterruptedTransaction(ctx, cfg); recovered || recoveryErr != nil {
		return recoveryErr
	}
	if reconciled, reconcileErr := consumeFinishedRequest(cfg); reconciled || reconcileErr != nil {
		return reconcileErr
	}

	request, requestErr := readUpdateRequest(cfg.RequestPath)
	if requestErr != nil {
		return requestErr
	}
	status, err := beginUpdateOperation(cfg, request)
	if err != nil {
		return err
	}
	if installedTarget, matchErr := installedPortalMatchesRequest(ctx, cfg, request, currentVersion); matchErr != nil {
		return finishOperationWithCause(cfg, status, "failed", "verifying", "VERSION_MISMATCH", false, matchErr)
	} else if installedTarget {
		return finishOperation(cfg, status, "succeeded", "completed", "", false)
	}
	prepared, err := prepareUpdate(ctx, cfg, currentVersion, request.TargetVersion, status.running)
	if err != nil {
		var upToDate upToDateError
		var manual manualRequiredError
		switch {
		case errors.As(err, &upToDate):
			return finishOperation(cfg, status, "succeeded", "up_to_date", "", false)
		case errors.As(err, &manual):
			return finishOperationWithCause(cfg, status, "failed", "manual_required", "MANUAL_UPDATE_REQUIRED", false, err)
		default:
			return finishOperationWithCause(cfg, status, "failed", status.status.Phase, errorCodeForPhase(status.status.Phase, err), false, err)
		}
	}
	defer os.Remove(prepared.ArchivePath)

	workspace, err := os.MkdirTemp(cfg.UpdateDir, ".extract-*")
	if err != nil {
		return finishOperationWithCause(cfg, status, "failed", "verifying", "VERIFICATION_FAILED", false, fmt.Errorf("create extraction workspace: %w", err))
	}
	defer os.RemoveAll(workspace)
	candidates, err := extractCandidates(prepared.ArchivePath, prepared.Tag, cfg.GOARCH, workspace, cfg.MaxExpanded)
	if err != nil {
		return finishOperationWithCause(cfg, status, "failed", "verifying", "VERIFICATION_FAILED", false, err)
	}
	if _, err := binaryVersion(ctx, cfg.Runner, candidates.Portal, "portal", prepared.Version, cfg.GOARCH); err != nil {
		return finishOperationWithCause(cfg, status, "failed", "verifying", "VERIFICATION_FAILED", false, err)
	}
	if _, err := binaryVersion(ctx, cfg.Runner, candidates.Updater, "updater", prepared.Version, cfg.GOARCH); err != nil {
		return finishOperationWithCause(cfg, status, "failed", "verifying", "VERIFICATION_FAILED", false, err)
	}
	oldPortalVersion, err := binaryVersion(ctx, cfg.Runner, cfg.PortalPath, "portal", currentVersion, cfg.GOARCH)
	if err != nil {
		return finishOperationWithCause(cfg, status, "failed", "verifying", "VERSION_MISMATCH", false, err)
	}

	err = applyTransaction(ctx, cfg, candidates, oldPortalVersion, prepared.Version, request, status.running)
	if err != nil {
		var transaction *transactionError
		if errors.As(err, &transaction) {
			switch {
			case transaction.RollbackFailed:
				if statusErr := status.finish("failed", "rollback_failed", "ROLLBACK_FAILED", false); statusErr != nil {
					return fmt.Errorf("%v; persist rollback failure: %w", err, statusErr)
				}
				return err
			case transaction.RolledBack:
				return finishRolledBackOperation(cfg, status, errorCodeForPhase(transaction.Phase, transaction.Cause), err)
			default:
				return finishOperationWithCause(cfg, status, "failed", transaction.Phase, errorCodeForPhase(transaction.Phase, transaction.Cause), false, err)
			}
		}
		return finishOperationWithCause(cfg, status, "failed", status.status.Phase, errorCodeForPhase(status.status.Phase, err), false, err)
	}
	return finishOperation(cfg, status, "succeeded", "completed", "", false)
}

func installedPortalMatchesRequest(ctx context.Context, cfg updaterConfig, request updateRequest, currentVersion string) (bool, error) {
	requested, err := parseSemVersion(strings.TrimPrefix(request.TargetVersion, "v"))
	if err != nil {
		return false, fmt.Errorf("requested update version is invalid")
	}
	current, err := parseSemVersion(strings.TrimPrefix(currentVersion, "v"))
	if err != nil || compareSemVersion(current, requested) != 0 {
		return false, nil
	}
	if _, err := binaryVersion(ctx, cfg.Runner, cfg.PortalPath, "portal", request.TargetVersion, cfg.GOARCH); err != nil {
		return false, fmt.Errorf("verify already-installed update target: %w", err)
	}
	return true, nil
}

func beginUpdateOperation(cfg updaterConfig, request updateRequest) (*statusWriter, error) {
	status := newStatusWriter(cfg.StatusPath, request, cfg.Now)
	if err := status.running("checking"); err != nil {
		return nil, err
	}
	return status, nil
}

func finishOperation(cfg updaterConfig, status *statusWriter, state, phase, errorCode string, rolledBack bool) error {
	if err := status.finish(state, phase, errorCode, rolledBack); err != nil {
		return err
	}
	return consumeUpdateRequest(cfg)
}

func finishOperationWithCause(cfg updaterConfig, status *statusWriter, state, phase, errorCode string, rolledBack bool, cause error) error {
	if err := finishOperation(cfg, status, state, phase, errorCode, rolledBack); err != nil {
		return fmt.Errorf("%v; finalize update operation: %w", cause, err)
	}
	return cause
}

func finishRolledBackOperation(cfg updaterConfig, status *statusWriter, errorCode string, cause error) error {
	if err := status.finish("failed", "rolled_back", errorCode, true); err != nil {
		return fmt.Errorf("%v; persist rollback result: %w", cause, err)
	}
	if err := consumeRequestForOperation(cfg, status.status.OperationID); err != nil {
		return fmt.Errorf("%v; consume rolled-back update request: %w", cause, err)
	}
	if err := removeTransactionJournal(cfg); err != nil {
		return fmt.Errorf("%v; clear completed rollback transaction: %w", cause, err)
	}
	return cause
}

func applyTransaction(
	ctx context.Context,
	cfg updaterConfig,
	candidates extractedCandidates,
	oldVersion string,
	targetVersion string,
	request updateRequest,
	progress func(string) error,
) error {
	wasActive, err := serviceIsActive(ctx, cfg.Runner, cfg.ServiceUnit)
	if err != nil {
		return &transactionError{Phase: "backing_up", Cause: err}
	}
	backupPath, err := os.MkdirTemp(cfg.BackupDir, fmt.Sprintf("update-v%s-%s-", targetVersion, cfg.Now().UTC().Format("20060102T150405Z")))
	if err != nil {
		return &transactionError{Phase: "backing_up", Cause: fmt.Errorf("create update backup: %w", err)}
	}
	if err := os.Chmod(backupPath, 0700); err != nil {
		return &transactionError{Phase: "backing_up", Cause: fmt.Errorf("set update backup mode: %w", err)}
	}
	if err := syncDirectory(cfg.BackupDir); err != nil {
		return &transactionError{Phase: "backing_up", Cause: err}
	}

	backupSources := []struct {
		path string
		name string
	}{
		{cfg.PortalPath, "sub2api-limit-portal"},
		{cfg.UpdaterPath, "sub2api-limit-updater"},
		{cfg.Database, "app.db"},
		{cfg.Database + "-wal", "app.db-wal"},
		{cfg.Database + "-shm", "app.db-shm"},
	}
	backups := make([]savedFile, 0, len(backupSources))
	changed := false
	journalWritten := false
	var journal transactionJournal

	rollback := func(phase string, cause error) error {
		if !journalWritten {
			return &transactionError{Phase: phase, Cause: cause}
		}
		if changed {
			stopCtx, cancelStop := recoveryPhaseContext(ctx, cfg.RecoveryStopTimeout, defaultRecoveryStopTimeout)
			didStop, stopErr := stopPortalService(stopCtx, cfg)
			cancelStop()
			if stopErr != nil && !didStop {
				return &transactionError{Phase: phase, Cause: fmt.Errorf("%v; rollback could not stop the updated portal: %w", cause, stopErr), RollbackFailed: true}
			}
			restoreCtx, cancelRestore := recoveryPhaseContext(ctx, cfg.RecoveryFileTimeout, defaultRecoveryFileTimeout)
			restoreErr := restoreTransactionFiles(restoreCtx, cfg, &journal)
			cancelRestore()
			if restoreErr != nil {
				return &transactionError{Phase: phase, Cause: fmt.Errorf("%v; restore previous files: %w", cause, restoreErr), RollbackFailed: true}
			}
		}
		startCtx, cancelStart := recoveryPhaseContext(ctx, cfg.RecoveryStartTimeout, defaultRecoveryStartTimeout)
		defer cancelStart()
		if wasActive {
			if _, err := cfg.Runner.Run(startCtx, "systemctl", "start", cfg.ServiceUnit); err != nil {
				return &transactionError{Phase: phase, Cause: fmt.Errorf("%v; restart previous portal: %w", cause, err), RollbackFailed: true}
			}
			if err := waitForHealthy(startCtx, cfg, oldVersion); err != nil {
				return &transactionError{Phase: phase, Cause: fmt.Errorf("%v; verify previous portal: %w", cause, err), RollbackFailed: true}
			}
		} else if changed {
			if _, err := binaryVersion(startCtx, cfg.Runner, cfg.PortalPath, "portal", oldVersion, cfg.GOARCH); err != nil {
				return &transactionError{Phase: phase, Cause: fmt.Errorf("%v; verify restored portal: %w", cause, err), RollbackFailed: true}
			}
		}
		return &transactionError{Phase: phase, Cause: fmt.Errorf("%v; previous binaries and database were restored", cause), RolledBack: true}
	}

	if err := progress("backing_up"); err != nil {
		return &transactionError{Phase: "backing_up", Cause: err}
	}
	journal = transactionJournal{
		Schema: 1, Request: request, Phase: "prepared", OldVersion: oldVersion,
		WasActive: wasActive, BackupPath: backupPath,
	}
	if err := writeTransactionJournal(cfg, journal); err != nil {
		return &transactionError{Phase: "backing_up", Cause: err}
	}
	journalWritten = true
	if wasActive {
		if err := ctx.Err(); err != nil {
			return rollback("backing_up", err)
		}
		controlCtx, cancelControl := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
		didStop, stopErr := stopPortalService(controlCtx, cfg)
		cancelControl()
		if stopErr != nil {
			if !didStop {
				return &transactionError{Phase: "backing_up", Cause: fmt.Errorf("stop portal service: %w", stopErr), RollbackFailed: true}
			}
			return rollback("backing_up", fmt.Errorf("stop portal service: %w", stopErr))
		}
		if err := ctx.Err(); err != nil {
			return rollback("backing_up", err)
		}
	}
	for _, source := range backupSources {
		entry, err := saveFile(ctx, source.path, filepath.Join(backupPath, source.name))
		if err != nil {
			return rollback("backing_up", err)
		}
		backups = append(backups, entry)
		if err := ctx.Err(); err != nil {
			return rollback("backing_up", err)
		}
	}
	if err := syncDirectory(backupPath); err != nil {
		return rollback("backing_up", err)
	}
	journal.Phase = "backed_up"
	journal.Files = transactionFiles(backups)
	if err := writeTransactionJournal(cfg, journal); err != nil {
		return rollback("backing_up", err)
	}
	if err := ctx.Err(); err != nil {
		return rollback("backing_up", err)
	}

	if err := progress("installing"); err != nil {
		return rollback("installing", err)
	}
	changed = true
	if err := atomicInstall(candidates.Portal, cfg.PortalPath, 0755); err != nil {
		return rollback("installing", err)
	}
	if err := ctx.Err(); err != nil {
		return rollback("installing", err)
	}
	if err := atomicInstall(candidates.Updater, cfg.UpdaterPath, 0755); err != nil {
		return rollback("installing", err)
	}
	if err := ctx.Err(); err != nil {
		return rollback("installing", err)
	}

	if wasActive {
		if err := progress("restarting"); err != nil {
			return rollback("restarting", err)
		}
		if err := ctx.Err(); err != nil {
			return rollback("restarting", err)
		}
		if _, err := cfg.Runner.Run(ctx, "systemctl", "start", cfg.ServiceUnit); err != nil {
			return rollback("restarting", fmt.Errorf("start updated portal: %w", err))
		}
		if err := progress("health_check"); err != nil {
			return rollback("health_check", err)
		}
		if err := waitForHealthy(ctx, cfg, targetVersion); err != nil {
			return rollback("health_check", err)
		}
	} else if _, err := binaryVersion(ctx, cfg.Runner, cfg.PortalPath, "portal", targetVersion, cfg.GOARCH); err != nil {
		return rollback("health_check", err)
	}
	if err := removeTransactionJournal(cfg); err != nil {
		return &transactionError{Phase: "health_check", Cause: fmt.Errorf("updated portal is healthy but the transaction journal could not be cleared: %w", err), RollbackFailed: true}
	}
	return nil
}

func recoverInterruptedTransaction(ctx context.Context, cfg updaterConfig) (bool, error) {
	journal, exists, err := readTransactionJournal(cfg)
	if err != nil || !exists {
		return exists, err
	}
	status := newStatusWriter(cfg.StatusPath, journal.Request, cfg.Now)
	if err := status.running("recovering"); err != nil {
		return true, err
	}
	if err := restoreInterruptedTransaction(ctx, cfg, journal); err != nil {
		statusErr := status.finish("failed", "rollback_failed", "ROLLBACK_FAILED", false)
		if statusErr != nil {
			return true, fmt.Errorf("recover interrupted update: %v; persist rollback failure: %w", err, statusErr)
		}
		return true, fmt.Errorf("recover interrupted update: %w", err)
	}
	if err := status.finish("failed", "rolled_back", "UPDATE_INTERRUPTED", true); err != nil {
		return true, err
	}
	if err := consumeRequestForOperation(cfg, journal.Request.OperationID); err != nil {
		return true, err
	}
	if err := removeTransactionJournal(cfg); err != nil {
		return true, err
	}
	return true, nil
}

func restoreInterruptedTransaction(ctx context.Context, cfg updaterConfig, journal transactionJournal) error {
	if journal.Phase == "backed_up" || journal.WasActive {
		stopCtx, cancelStop := recoveryPhaseContext(ctx, cfg.RecoveryStopTimeout, defaultRecoveryStopTimeout)
		stopped, stopErr := stopPortalService(stopCtx, cfg)
		cancelStop()
		if stopErr != nil && !stopped {
			return fmt.Errorf("stop portal before crash recovery: %w", stopErr)
		}
	}
	if journal.Phase == "backed_up" {
		restoreCtx, cancelRestore := recoveryPhaseContext(ctx, cfg.RecoveryFileTimeout, defaultRecoveryFileTimeout)
		restoreErr := restoreTransactionFiles(restoreCtx, cfg, &journal)
		cancelRestore()
		if restoreErr != nil {
			return fmt.Errorf("restore interrupted update files: %w", restoreErr)
		}
	}
	startCtx, cancelStart := recoveryPhaseContext(ctx, cfg.RecoveryStartTimeout, defaultRecoveryStartTimeout)
	defer cancelStart()
	if journal.WasActive {
		if _, err := cfg.Runner.Run(startCtx, "systemctl", "start", cfg.ServiceUnit); err != nil {
			return fmt.Errorf("restart portal after crash recovery: %w", err)
		}
		if err := waitForHealthy(startCtx, cfg, journal.OldVersion); err != nil {
			return fmt.Errorf("verify portal after crash recovery: %w", err)
		}
	} else if journal.Phase == "backed_up" {
		if _, err := binaryVersion(startCtx, cfg.Runner, cfg.PortalPath, "portal", journal.OldVersion, cfg.GOARCH); err != nil {
			return fmt.Errorf("verify restored portal after crash recovery: %w", err)
		}
	}
	return nil
}

func restoreTransactionFiles(ctx context.Context, cfg updaterConfig, journal *transactionJournal) error {
	return restoreTransactionFilesWithCheckpoint(ctx, journal, func(updated transactionJournal) error {
		return writeTransactionJournal(cfg, updated)
	})
}

func restoreTransactionFilesWithCheckpoint(ctx context.Context, journal *transactionJournal, checkpoint func(transactionJournal) error) error {
	files := journal.savedFiles()
	for journal.RestoreCursor < len(files) {
		index := len(files) - 1 - journal.RestoreCursor
		if err := restoreFile(ctx, files[index]); err != nil {
			return err
		}
		journal.RestoreCursor++
		if err := checkpoint(*journal); err != nil {
			return fmt.Errorf("checkpoint restored update files: %w", err)
		}
	}
	return nil
}

func recoveryPhaseContext(parent context.Context, configured, fallback time.Duration) (context.Context, context.CancelFunc) {
	if configured <= 0 {
		configured = fallback
	}
	return context.WithTimeout(context.WithoutCancel(parent), configured)
}

func stopPortalService(ctx context.Context, cfg updaterConfig) (bool, error) {
	commandCtx, cancelCommand := context.WithTimeout(ctx, 60*time.Second)
	_, stopErr := cfg.Runner.Run(commandCtx, "systemctl", "stop", cfg.ServiceUnit)
	cancelCommand()
	if stopErr == nil {
		return true, nil
	}
	for attempt := 0; attempt < 120; attempt++ {
		state, stateErr := serviceState(ctx, cfg.Runner, cfg.ServiceUnit)
		if stateErr == nil && (state == "inactive" || state == "failed") {
			return true, fmt.Errorf("systemctl stop reported an error after the portal stopped: %w", stopErr)
		}
		if stateErr != nil {
			return false, fmt.Errorf("%v; determine portal state: %w", stopErr, stateErr)
		}
		if err := waitWithContext(ctx, cfg.Wait, 250*time.Millisecond); err != nil {
			return false, fmt.Errorf("%v; portal did not reach a stopped state: %w", stopErr, err)
		}
	}
	return false, fmt.Errorf("%v; portal did not reach a stopped state before the verification limit", stopErr)
}

func serviceState(ctx context.Context, runner commandRunner, unit string) (string, error) {
	output, err := runner.Run(ctx, "systemctl", "is-active", unit)
	state := strings.TrimSpace(output)
	switch state {
	case "active", "activating", "deactivating", "reloading", "inactive", "failed":
		return state, nil
	default:
		if err != nil {
			return state, err
		}
		return state, fmt.Errorf("portal service returned unexpected state %q", state)
	}
}

func waitWithContext(ctx context.Context, wait func(context.Context, time.Duration) error, delay time.Duration) error {
	if wait != nil {
		return wait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func serviceIsActive(ctx context.Context, runner commandRunner, unit string) (bool, error) {
	state, err := serviceState(ctx, runner, unit)
	if err == nil && state == "active" {
		return true, nil
	}
	switch state {
	case "inactive", "failed":
		return false, nil
	case "deactivating":
		return false, fmt.Errorf("portal service is still deactivating")
	default:
		if err != nil {
			return false, fmt.Errorf("determine portal service state: %w", err)
		}
		return false, fmt.Errorf("portal service returned unexpected state %q", state)
	}
}

func binaryVersion(ctx context.Context, runner commandRunner, binary, kind, expectedVersion, expectedArch string) (string, error) {
	output, err := runner.Run(ctx, binary, "version")
	if err != nil {
		return "", fmt.Errorf("run %s version check: %w", kind, err)
	}
	matches := versionOutputPattern.FindStringSubmatch(output)
	if matches == nil || matches[1] != kind || matches[3] != expectedArch {
		return "", fmt.Errorf("%s returned unexpected version metadata", kind)
	}
	actual, err := parseSemVersion(matches[2])
	if err != nil {
		return "", fmt.Errorf("%s version: %w", kind, err)
	}
	expected, err := parseSemVersion(strings.TrimPrefix(expectedVersion, "v"))
	if err != nil || compareSemVersion(actual, expected) != 0 {
		return "", fmt.Errorf("%s version does not match the expected release", kind)
	}
	return matches[2], nil
}

func waitForHealthy(ctx context.Context, cfg updaterConfig, expectedVersion string) error {
	readyURL, err := localReadyURL(cfg.EnvPath)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < cfg.ReadyAttempts; attempt++ {
		active, stateErr := serviceIsActive(ctx, cfg.Runner, cfg.ServiceUnit)
		if stateErr != nil || !active {
			lastErr = fmt.Errorf("portal service is not active")
		} else if err := checkReady(ctx, cfg.HTTPClient, readyURL); err != nil {
			lastErr = err
		} else if _, err := binaryVersion(ctx, cfg.Runner, cfg.PortalPath, "portal", expectedVersion, cfg.GOARCH); err != nil {
			lastErr = err
		} else {
			return nil
		}
		if attempt+1 < cfg.ReadyAttempts {
			wait := cfg.Wait
			if wait == nil {
				wait = func(ctx context.Context, delay time.Duration) error {
					timer := time.NewTimer(delay)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-timer.C:
						return nil
					}
				}
			}
			if err := wait(ctx, cfg.ReadyDelay); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("portal did not become ready: %w", lastErr)
}

func checkReady(ctx context.Context, client *http.Client, endpoint string) error {
	requestContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create readiness request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request /readyz: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("/readyz returned HTTP %d", response.StatusCode)
	}
	return nil
}

func localReadyURL(envPath string) (string, error) {
	body, err := os.ReadFile(envPath)
	if err != nil {
		return "", fmt.Errorf("read portal environment: %w", err)
	}
	listen := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		const key = "SUB2API_LIMIT_LISTEN="
		if strings.HasPrefix(line, key) {
			listen = strings.TrimSpace(strings.TrimPrefix(line, key))
			if len(listen) >= 2 && ((listen[0] == '\'' && listen[len(listen)-1] == '\'') || (listen[0] == '"' && listen[len(listen)-1] == '"')) {
				listen = listen[1 : len(listen)-1]
			}
		}
	}
	if listen == "" {
		return "", fmt.Errorf("portal environment does not define SUB2API_LIMIT_LISTEN")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("parse SUB2API_LIMIT_LISTEN: %w", err)
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	endpoint := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/readyz"}
	return endpoint.String(), nil
}

func errorCodeForPhase(phase string, err error) string {
	if strings.Contains(err.Error(), "no longer the latest") {
		return "TARGET_VERSION_CHANGED"
	}
	switch phase {
	case "checking":
		return "RELEASE_CHECK_FAILED"
	case "downloading":
		return "DOWNLOAD_FAILED"
	case "verifying":
		return "VERIFICATION_FAILED"
	case "backing_up":
		return "BACKUP_FAILED"
	case "installing":
		return "INSTALL_FAILED"
	case "restarting":
		return "RESTART_FAILED"
	case "health_check":
		return "HEALTH_CHECK_FAILED"
	default:
		return "UPDATE_FAILED"
	}
}

func publicError(err error) string {
	if err == nil {
		return "unknown error"
	}
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > 500 {
		message = message[:500]
	}
	return message
}
