package codexforecast

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	box, err := secure.NewBox(bytes.Repeat([]byte{0x31}, 32))
	if err != nil {
		t.Fatalf("secure.NewBox() error = %v", err)
	}
	data, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "portal.db"), box)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := data.Close(); err != nil {
			t.Errorf("store.Close() error = %v", err)
		}
	})
	return data
}

func newTestManager(t *testing.T, data *store.Store, endpoint string) *Manager {
	t.Helper()
	manager, err := New(data, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Endpoint: endpoint})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return manager
}

const upstreamPayload = `{
  "fetchedAt": "2026-08-30T10:15:00Z",
  "nextRefreshAt": "2026-08-30T10:45:00Z",
  "forecast": {
    "score": 74,
    "horizonHours": 24,
    "daysSinceReset": 6,
    "hoursSinceReset": 151.5551,
    "latestResetAt": "2026-08-24T02:40:00Z",
    "resetAnnounced": false,
    "state": "likely",
    "evidenceTier": "moderate",
    "modelVersion": "v3",
    "breakdown": [
      {"label": "社区报告增多", "points": 18},
      {"label": "", "points": 4},
      {"label": "距上次重置已久", "points": 12}
    ],
    "rawSignals": {"ignored": true}
  },
  "extraTopLevel": [1, 2, 3]
}`

func TestRefreshStoresUpstreamForecast(t *testing.T) {
	data := openTestStore(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept header = %q, want application/json", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(upstreamPayload))
	}))
	defer server.Close()

	manager := newTestManager(t, data, server.URL)
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	state, err := manager.View(context.Background())
	if err != nil {
		t.Fatalf("View() error = %v", err)
	}
	if state.Score != 74 || state.HorizonHours != 24 {
		t.Fatalf("score/horizon = %d/%d, want 74/24", state.Score, state.HorizonHours)
	}
	if state.ForecastState != "likely" || state.EvidenceTier != "moderate" || state.ModelVersion != "v3" {
		t.Fatalf("labels = %#v", state)
	}
	if state.DaysSinceReset == nil || *state.DaysSinceReset != 6 {
		t.Fatalf("days since reset = %v, want 6", state.DaysSinceReset)
	}
	// 保留两位小数，避免把上游的浮点噪声原样展示
	if state.HoursSinceReset == nil || *state.HoursSinceReset != 151.56 {
		t.Fatalf("hours since reset = %v, want 151.56", state.HoursSinceReset)
	}
	if state.SourceFetchedAt == nil || *state.SourceFetchedAt != time.Date(2026, 8, 30, 10, 15, 0, 0, time.UTC).Unix() {
		t.Fatalf("source fetched at = %v", state.SourceFetchedAt)
	}
	if state.NextRefreshAt == nil || state.LatestResetAt == nil {
		t.Fatalf("timestamps = %v / %v, want both parsed", state.NextRefreshAt, state.LatestResetAt)
	}
	// 空 label 的加分项被丢掉，前端不会渲染出空行
	if len(state.Breakdown) != 2 {
		t.Fatalf("breakdown = %#v, want the two labelled entries", state.Breakdown)
	}
	if state.Breakdown[0].Label != "社区报告增多" || state.Breakdown[0].Points != 18 {
		t.Fatalf("first breakdown entry = %#v", state.Breakdown[0])
	}
	if state.LastErrorCode != "" || state.LastSuccessAt == nil {
		t.Fatalf("success bookkeeping = %q / %v", state.LastErrorCode, state.LastSuccessAt)
	}
}

func TestRefreshClampsAndTruncatesHostileValues(t *testing.T) {
	data := openTestStore(t)
	payload := `{"forecast":{"score":413,"state":"` + strings.Repeat("状", 80) +
		`","evidenceTier":"x","modelVersion":"y","hoursSinceReset":null,"breakdown":[{"label":"` +
		strings.Repeat("标", 200) + `","points":3}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	manager := newTestManager(t, data, server.URL)
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	state, err := manager.View(context.Background())
	if err != nil {
		t.Fatalf("View() error = %v", err)
	}
	// 迁移里的 CHECK 要求 0-100，越界值必须先夹住再写库
	if state.Score != 100 {
		t.Fatalf("score = %d, want clamped to 100", state.Score)
	}
	if runes := []rune(state.ForecastState); len(runes) != 32 {
		t.Fatalf("state length = %d runes, want truncated to 32", len(runes))
	}
	if runes := []rune(state.Breakdown[0].Label); len(runes) != 64 {
		t.Fatalf("breakdown label length = %d runes, want truncated to 64", len(runes))
	}
	if state.HoursSinceReset != nil {
		t.Fatalf("hours since reset = %v, want nil for a null upstream value", state.HoursSinceReset)
	}
}

func TestRefreshFailuresKeepLastGoodValue(t *testing.T) {
	data := openTestStore(t)
	mode := "ok"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch mode {
		case "ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(upstreamPayload))
		case "status":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{}`))
		case "garbage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json at all`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"fetchedAt":"2026-08-30T10:15:00Z"}`))
		}
	}))
	defer server.Close()

	manager := newTestManager(t, data, server.URL)
	if err := manager.Refresh(context.Background()); err != nil {
		t.Fatalf("initial Refresh() error = %v", err)
	}

	for _, testCase := range []struct{ mode, wantCode string }{
		{"status", "HTTP_502"},
		{"garbage", "INVALID_JSON"},
		{"missing", "NO_FORECAST"},
	} {
		mode = testCase.mode
		if err := manager.Refresh(context.Background()); err == nil {
			t.Fatalf("Refresh(%s) error = nil, want a failure", testCase.mode)
		}
		state, err := manager.View(context.Background())
		if err != nil {
			t.Fatalf("View() after %s error = %v", testCase.mode, err)
		}
		if state.LastErrorCode != testCase.wantCode {
			t.Fatalf("last error code = %q, want %q", state.LastErrorCode, testCase.wantCode)
		}
		// 失败不能把上一次成功的数值清掉，否则界面会闪成空白
		if state.Score != 74 {
			t.Fatalf("score after %s = %d, want the last good 74", testCase.mode, state.Score)
		}
		if state.LastSuccessAt == nil {
			t.Fatalf("last success timestamp was dropped after %s", testCase.mode)
		}
	}
}

func TestViewBeforeFirstFetchIsEmptyNotAnError(t *testing.T) {
	data := openTestStore(t)
	manager := newTestManager(t, data, "http://127.0.0.1:1")
	state, err := manager.View(context.Background())
	if err != nil {
		t.Fatalf("View() error = %v", err)
	}
	if state.Score != 0 || state.LastSuccessAt != nil {
		t.Fatalf("seeded state = %#v, want an empty row", state)
	}

	// 连不上时记录错误码，界面据此提示降级
	if err := manager.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh() to a dead endpoint error = nil, want a failure")
	}
	state, err = manager.View(context.Background())
	if err != nil {
		t.Fatalf("View() after failure error = %v", err)
	}
	if state.LastErrorCode != "UNREACHABLE" {
		t.Fatalf("last error code = %q, want UNREACHABLE", state.LastErrorCode)
	}
}
