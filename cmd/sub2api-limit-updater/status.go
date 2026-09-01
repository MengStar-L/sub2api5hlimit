package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxRequestBytes = 8 << 10

var operationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type updateRequest struct {
	Schema        int    `json:"schema"`
	OperationID   string `json:"operation_id"`
	TargetVersion string `json:"target_version"`
	RequestedAt   int64  `json:"requested_at"`
}

type updateStatus struct {
	Schema        int    `json:"schema"`
	OperationID   string `json:"operation_id"`
	TargetVersion string `json:"target_version"`
	State         string `json:"state"`
	Phase         string `json:"phase"`
	ErrorCode     string `json:"error_code"`
	StartedAt     int64  `json:"started_at"`
	UpdatedAt     int64  `json:"updated_at"`
	FinishedAt    *int64 `json:"finished_at"`
	RolledBack    bool   `json:"rolled_back"`
}

type statusWriter struct {
	path   string
	now    func() time.Time
	status updateStatus
}

func readUpdateRequest(path string) (updateRequest, error) {
	file, err := openReadNoFollow(path)
	if err != nil {
		return updateRequest{}, fmt.Errorf("open update request: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxRequestBytes+1))
	if err != nil {
		return updateRequest{}, fmt.Errorf("read update request: %w", err)
	}
	if len(body) > maxRequestBytes {
		return updateRequest{}, fmt.Errorf("update request exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request updateRequest
	if err := decoder.Decode(&request); err != nil {
		return updateRequest{}, fmt.Errorf("decode update request: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return updateRequest{}, err
	}
	if request.Schema != 1 || !operationIDPattern.MatchString(request.OperationID) || request.RequestedAt <= 0 {
		return updateRequest{}, fmt.Errorf("update request contains invalid metadata")
	}
	if !strings.HasPrefix(request.TargetVersion, "v") {
		return updateRequest{}, fmt.Errorf("update request target must be a v-prefixed semantic version")
	}
	if strings.Contains(request.TargetVersion, "+") {
		return updateRequest{}, fmt.Errorf("update request target must not contain build metadata")
	}
	parsed, err := parseSemVersion(strings.TrimPrefix(request.TargetVersion, "v"))
	if err != nil || len(parsed.pre) != 0 {
		return updateRequest{}, fmt.Errorf("update request target must be a stable semantic version")
	}
	return request, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("JSON document contains trailing data")
	}
	return nil
}

func newStatusWriter(path string, request updateRequest, now func() time.Time) *statusWriter {
	started := now().UTC().Unix()
	return &statusWriter{
		path: path,
		now:  now,
		status: updateStatus{
			Schema:        1,
			OperationID:   request.OperationID,
			TargetVersion: request.TargetVersion,
			State:         "running",
			Phase:         "checking",
			StartedAt:     started,
			UpdatedAt:     started,
		},
	}
}

func readUpdateStatus(path string) (updateStatus, bool, error) {
	file, err := openReadNoFollow(path)
	if os.IsNotExist(err) {
		return updateStatus{}, false, nil
	}
	if err != nil {
		return updateStatus{}, false, fmt.Errorf("open update status: %w", err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxTransactionJournalBytes+1))
	if err != nil {
		return updateStatus{}, false, fmt.Errorf("read update status: %w", err)
	}
	if len(body) > maxTransactionJournalBytes {
		return updateStatus{}, false, fmt.Errorf("update status exceeds the size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var status updateStatus
	if err := decoder.Decode(&status); err != nil {
		return updateStatus{}, false, fmt.Errorf("decode update status: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return updateStatus{}, false, err
	}
	if status.Schema != 1 || !operationIDPattern.MatchString(status.OperationID) {
		return updateStatus{}, false, fmt.Errorf("update status contains invalid metadata")
	}
	return status, true, nil
}

func consumeFinishedRequest(cfg updaterConfig) (bool, error) {
	request, err := readUpdateRequest(cfg.RequestPath)
	if err != nil {
		return false, nil
	}
	status, exists, err := readUpdateStatus(cfg.StatusPath)
	if err != nil || !exists {
		return false, err
	}
	if status.OperationID != request.OperationID || status.FinishedAt == nil || (status.State != "succeeded" && status.State != "failed") {
		return false, nil
	}
	if err := consumeRequestForOperation(cfg, request.OperationID); err != nil {
		return true, err
	}
	return true, nil
}

func consumeRequestForOperation(cfg updaterConfig, operationID string) error {
	request, err := readUpdateRequest(cfg.RequestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify update request before consuming it: %w", err)
	}
	if request.OperationID != operationID {
		return nil
	}
	return consumeUpdateRequest(cfg)
}

func (writer *statusWriter) running(phase string) error {
	writer.status.State = "running"
	writer.status.Phase = phase
	writer.status.ErrorCode = ""
	writer.status.UpdatedAt = writer.now().UTC().Unix()
	writer.status.FinishedAt = nil
	return writeJSONAtomic(writer.path, writer.status)
}

func (writer *statusWriter) finish(state, phase, errorCode string, rolledBack bool) error {
	now := writer.now().UTC().Unix()
	writer.status.State = state
	writer.status.Phase = phase
	writer.status.ErrorCode = errorCode
	writer.status.UpdatedAt = now
	writer.status.FinishedAt = &now
	writer.status.RolledBack = rolledBack
	return writeJSONAtomic(writer.path, writer.status)
}

func writeJSONAtomic(path string, value any) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".status-*.tmp")
	if err != nil {
		return fmt.Errorf("create status file: %w", err)
	}
	temporaryPath := temporary.Name()
	succeeded := false
	defer func() {
		_ = temporary.Close()
		if !succeeded {
			_ = os.Remove(temporaryPath)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode status file: %w", err)
	}
	if err := temporary.Chmod(0640); err != nil {
		return fmt.Errorf("set status file mode: %w", err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		return fmt.Errorf("inspect status directory: %w", err)
	}
	if _, gid, ok := fileOwner(directoryInfo); ok {
		if err := setFileOwner(temporaryPath, 0, gid); err != nil {
			return fmt.Errorf("set status file owner: %w", err)
		}
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync status file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close status file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace status file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	succeeded = true
	return nil
}
