package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUpdateRequestAndStatusWireFormat(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	requestPath := filepath.Join(directory, "update.request")
	requestBody := []byte(`{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.1","requested_at":1788134400}`)
	if err := os.WriteFile(requestPath, requestBody, 0600); err != nil {
		t.Fatal(err)
	}
	request, err := readUpdateRequest(requestPath)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	now := func() time.Time { return time.Unix(1788134401, 0) }
	statusPath := filepath.Join(directory, "status.json")
	writer := newStatusWriter(statusPath, request, now)
	if err := writer.running("verifying"); err != nil {
		t.Fatalf("write running status: %v", err)
	}
	body, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"error_code", "finished_at", "operation_id", "phase", "rolled_back", "schema", "started_at", "state", "target_version", "updated_at"}
	gotFields := make([]string, 0, len(fields))
	for field := range fields {
		gotFields = append(gotFields, field)
	}
	sortStrings(gotFields)
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Fatalf("status fields = %#v, want %#v", gotFields, wantFields)
	}
	if fields["state"] != "running" || fields["phase"] != "verifying" || fields["finished_at"] != nil {
		t.Fatalf("unexpected running status: %s", body)
	}
}

func TestUpdateRequestRejectsUnknownField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "update.request")
	body := []byte(`{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.1","requested_at":1,"url":"https://example.test"}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUpdateRequest(path); err == nil {
		t.Fatal("request with trusted-looking URL unexpectedly accepted")
	}
}

func TestUpdateRequestRejectsBuildMetadata(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "update.request")
	body := []byte(`{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.1+rebuilt","requested_at":1}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readUpdateRequest(path); err == nil {
		t.Fatal("request with build metadata unexpectedly succeeded")
	}
}

func TestConsumeFinishedRequestIsIdempotent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	requestPath := filepath.Join(root, "update.request")
	statusPath := filepath.Join(root, "status.json")
	request := testUpdateRequest()
	writeTestFile(t, requestPath, `{"schema":1,"operation_id":"0123456789abcdef0123456789abcdef","target_version":"v0.2.0","requested_at":1788134400}`, 0640)
	writer := newStatusWriter(statusPath, request, func() time.Time { return time.Unix(1788134500, 0) })
	if err := writer.finish("succeeded", "completed", "", false); err != nil {
		t.Fatal(err)
	}
	cfg := updaterConfig{DataDir: root, RequestPath: requestPath, StatusPath: statusPath}
	consumed, err := consumeFinishedRequest(cfg)
	if err != nil || !consumed {
		t.Fatalf("consumeFinishedRequest = %t, %v", consumed, err)
	}
	if _, err := os.Stat(requestPath); !os.IsNotExist(err) {
		t.Fatalf("finished request still exists: %v", err)
	}
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
