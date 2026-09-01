package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxTransactionJournalBytes = 64 << 10

type transactionJournal struct {
	Schema        int               `json:"schema"`
	Request       updateRequest     `json:"request"`
	Phase         string            `json:"phase"`
	OldVersion    string            `json:"old_version"`
	WasActive     bool              `json:"was_active"`
	BackupPath    string            `json:"backup_path"`
	Files         []transactionFile `json:"files"`
	RestoreCursor int               `json:"restore_cursor,omitempty"`
}

type transactionFile struct {
	Original string `json:"original"`
	Backup   string `json:"backup"`
	Existed  bool   `json:"existed"`
	Mode     uint32 `json:"mode"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	HasOwner bool   `json:"has_owner"`
}

func transactionJournalPath(cfg updaterConfig) string {
	if cfg.JournalPath != "" {
		return cfg.JournalPath
	}
	return filepath.Join(cfg.BackupDir, ".update-transaction.json")
}

func transactionFiles(entries []savedFile) []transactionFile {
	files := make([]transactionFile, 0, len(entries))
	for _, entry := range entries {
		files = append(files, transactionFile{
			Original: entry.Original,
			Backup:   entry.Backup,
			Existed:  entry.Existed,
			Mode:     uint32(entry.Mode.Perm()),
			UID:      entry.UID,
			GID:      entry.GID,
			HasOwner: entry.HasOwner,
		})
	}
	return files
}

func (journal transactionJournal) savedFiles() []savedFile {
	files := make([]savedFile, 0, len(journal.Files))
	for _, entry := range journal.Files {
		files = append(files, savedFile{
			Original: entry.Original,
			Backup:   entry.Backup,
			Existed:  entry.Existed,
			Mode:     os.FileMode(entry.Mode),
			UID:      entry.UID,
			GID:      entry.GID,
			HasOwner: entry.HasOwner,
		})
	}
	return files
}

func writeTransactionJournal(cfg updaterConfig, journal transactionJournal) error {
	if err := validateTransactionJournal(cfg, journal); err != nil {
		return err
	}
	if err := writeJSONAtomic(transactionJournalPath(cfg), journal); err != nil {
		return fmt.Errorf("persist update transaction: %w", err)
	}
	return nil
}

func readTransactionJournal(cfg updaterConfig) (transactionJournal, bool, error) {
	path := transactionJournalPath(cfg)
	file, err := openReadNoFollow(path)
	if os.IsNotExist(err) {
		return transactionJournal{}, false, nil
	}
	if err != nil {
		return transactionJournal{}, false, fmt.Errorf("open update transaction: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxTransactionJournalBytes+1))
	if err != nil {
		return transactionJournal{}, false, fmt.Errorf("read update transaction: %w", err)
	}
	if len(body) > maxTransactionJournalBytes {
		return transactionJournal{}, false, fmt.Errorf("update transaction exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var journal transactionJournal
	if err := decoder.Decode(&journal); err != nil {
		return transactionJournal{}, false, fmt.Errorf("decode update transaction: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return transactionJournal{}, false, err
	}
	if err := validateTransactionJournal(cfg, journal); err != nil {
		return transactionJournal{}, false, err
	}
	return journal, true, nil
}

func validateTransactionJournal(cfg updaterConfig, journal transactionJournal) error {
	if journal.Schema != 1 || !operationIDPattern.MatchString(journal.Request.OperationID) || journal.Request.Schema != 1 || journal.Request.RequestedAt <= 0 {
		return fmt.Errorf("update transaction contains invalid metadata")
	}
	if !strings.HasPrefix(journal.Request.TargetVersion, "v") || strings.Contains(journal.Request.TargetVersion, "+") {
		return fmt.Errorf("update transaction target is invalid")
	}
	target, targetErr := parseSemVersion(strings.TrimPrefix(journal.Request.TargetVersion, "v"))
	oldVersion, oldErr := parseSemVersion(strings.TrimPrefix(journal.OldVersion, "v"))
	if targetErr != nil || len(target.pre) != 0 || oldErr != nil || len(oldVersion.pre) != 0 {
		return fmt.Errorf("update transaction versions are invalid")
	}
	backupRoot := filepath.Clean(cfg.BackupDir)
	backupPath := filepath.Clean(journal.BackupPath)
	if filepath.Dir(backupPath) != backupRoot || backupPath == backupRoot {
		return fmt.Errorf("update transaction backup path is invalid")
	}
	backupInfo, err := os.Lstat(backupPath)
	if err != nil || !backupInfo.IsDir() || backupInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("update transaction backup directory is invalid")
	}
	switch journal.Phase {
	case "prepared":
		if len(journal.Files) != 0 || journal.RestoreCursor != 0 {
			return fmt.Errorf("prepared update transaction unexpectedly contains backups")
		}
	case "backed_up":
		expectedOriginals := []string{cfg.PortalPath, cfg.UpdaterPath, cfg.Database, cfg.Database + "-wal", cfg.Database + "-shm"}
		expectedNames := []string{"sub2api-limit-portal", "sub2api-limit-updater", "app.db", "app.db-wal", "app.db-shm"}
		if len(journal.Files) != len(expectedOriginals) {
			return fmt.Errorf("update transaction backup list is incomplete")
		}
		if journal.RestoreCursor < 0 || journal.RestoreCursor > len(journal.Files) {
			return fmt.Errorf("update transaction restore cursor is invalid")
		}
		for index, entry := range journal.Files {
			if entry.Original != expectedOriginals[index] || filepath.Clean(entry.Backup) != filepath.Join(backupPath, expectedNames[index]) || entry.Mode&^0777 != 0 {
				return fmt.Errorf("update transaction backup entry is invalid")
			}
			if entry.HasOwner && (entry.UID < 0 || entry.GID < 0) {
				return fmt.Errorf("update transaction backup owner is invalid")
			}
			if entry.Existed {
				if err := validateRegularFile(entry.Backup, true); err != nil {
					return fmt.Errorf("validate update transaction backup: %w", err)
				}
			}
		}
	default:
		return fmt.Errorf("update transaction phase is invalid")
	}
	return nil
}

func removeTransactionJournal(cfg updaterConfig) error {
	if err := os.Remove(transactionJournalPath(cfg)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove update transaction: %w", err)
	}
	if err := syncDirectory(filepath.Dir(transactionJournalPath(cfg))); err != nil {
		return err
	}
	return nil
}

func consumeUpdateRequest(cfg updaterConfig) error {
	if err := os.Remove(cfg.RequestPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("consume update request: %w", err)
	}
	if err := syncDirectory(cfg.DataDir); err != nil {
		return err
	}
	return nil
}
