package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRestoreTransactionFilesResumesInReverseOrder(t *testing.T) {
	t.Parallel()
	entries := restoreTestFiles(t, 3)
	writeTestFile(t, entries[2].Original, "already restored", 0600)
	journal := transactionJournal{Files: transactionFiles(entries), RestoreCursor: 1}
	var checkpoints []int
	if err := restoreTransactionFilesWithCheckpoint(context.Background(), &journal, func(updated transactionJournal) error {
		checkpoints = append(checkpoints, updated.RestoreCursor)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoints, []int{2, 3}) {
		t.Fatalf("restore checkpoints = %v, want [2 3]", checkpoints)
	}
	assertFileContents(t, entries[0].Original, "old-0")
	assertFileContents(t, entries[1].Original, "old-1")
	assertFileContents(t, entries[2].Original, "already restored")
}

func TestRestoreTransactionFilesReplaysAfterCheckpointFailure(t *testing.T) {
	t.Parallel()
	entries := restoreTestFiles(t, 2)
	persisted := transactionJournal{Files: transactionFiles(entries)}
	working := persisted
	checkpointFailure := errors.New("checkpoint failed")
	err := restoreTransactionFilesWithCheckpoint(context.Background(), &working, func(transactionJournal) error {
		return checkpointFailure
	})
	if !errors.Is(err, checkpointFailure) || working.RestoreCursor != 1 || persisted.RestoreCursor != 0 {
		t.Fatalf("first restore = cursor %d persisted %d error %v", working.RestoreCursor, persisted.RestoreCursor, err)
	}
	assertFileContents(t, entries[0].Original, "new-0")
	assertFileContents(t, entries[1].Original, "old-1")

	var checkpoints []int
	if err := restoreTransactionFilesWithCheckpoint(context.Background(), &persisted, func(updated transactionJournal) error {
		checkpoints = append(checkpoints, updated.RestoreCursor)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(checkpoints, []int{1, 2}) {
		t.Fatalf("replayed checkpoints = %v, want [1 2]", checkpoints)
	}
	assertFileContents(t, entries[0].Original, "old-0")
	assertFileContents(t, entries[1].Original, "old-1")
}

func TestCompleteRestoreCursorStillStartsAndChecksPortal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	portalPath := filepath.Join(root, "portal")
	database := filepath.Join(root, "app.db")
	envPath := filepath.Join(root, "portal.env")
	backupPath := filepath.Join(root, "portal.backup")
	writeTestFile(t, portalPath, "0.1.0", 0755)
	writeTestFile(t, database, "database", 0600)
	writeTestFile(t, backupPath, "must not be replayed", 0600)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	writeTestFile(t, envPath, "SUB2API_LIMIT_LISTEN="+strings.TrimPrefix(server.URL, "http://")+"\n", 0600)
	runner := &transactionRunner{active: true, portalPath: portalPath, database: database}
	entry := savedFile{Original: portalPath, Backup: backupPath, Existed: true, Mode: 0755}
	journal := transactionJournal{
		Phase: "backed_up", OldVersion: "0.1.0", WasActive: true,
		Files: transactionFiles([]savedFile{entry}), RestoreCursor: 1,
	}
	cfg := updaterConfig{
		PortalPath: portalPath, Database: database, EnvPath: envPath, ServiceUnit: portalServiceUnit,
		HTTPClient: server.Client(), Runner: runner, Wait: func(context.Context, time.Duration) error { return nil },
		GOARCH: "amd64", ReadyAttempts: 1,
	}
	if err := restoreInterruptedTransaction(context.Background(), cfg, journal); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, portalPath, "0.1.0")
	if !runner.active {
		t.Fatal("portal was not started after a complete restore cursor")
	}
}

func restoreTestFiles(t *testing.T, count int) []savedFile {
	t.Helper()
	directory := t.TempDir()
	entries := make([]savedFile, 0, count)
	for index := 0; index < count; index++ {
		suffix := strconv.Itoa(index)
		original := filepath.Join(directory, "original-"+suffix)
		backup := filepath.Join(directory, "backup-"+suffix)
		writeTestFile(t, original, "new-"+suffix, 0600)
		writeTestFile(t, backup, "old-"+suffix, 0600)
		entries = append(entries, savedFile{Original: original, Backup: backup, Existed: true, Mode: 0600})
	}
	return entries
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || string(body) != want {
		t.Fatalf("%s = %q, %v; want %q", path, body, err, want)
	}
}
