// Package codexforecast 拉取第三方站点公开的 Codex 配额重置预测数值并缓存。
//
// 数据来自 willcodexquotareset.com 的 /api/forecast，属于非官方预测，不代表 OpenAI。
// 由后端代抓的原因有三：门户 CSP 是 connect-src 'self'，浏览器无法直连；
// 集中缓存可避免每个用户各刷一次；用户浏览器也不必暴露给第三方。
package codexforecast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

const (
	// 上游自身约每 30 分钟刷新一次，取 15 分钟足够跟上又不至于打扰它
	defaultInterval = 15 * time.Minute
	defaultEndpoint = "https://www.willcodexquotareset.com/api/forecast"
	// SourceURL 展示给用户，用于标注数据来源
	SourceURL  = "https://www.willcodexquotareset.com/"
	maxBodyLen = 4 << 20
)

type Config struct {
	Endpoint   string
	Interval   time.Duration
	HTTPClient *http.Client
}

type Manager struct {
	store    *store.Store
	log      *slog.Logger
	endpoint string
	interval time.Duration
	client   *http.Client
}

// 只解析需要的字段；上游 JSON 有 100KB 的原始信号，全部忽略
type forecastDTO struct {
	FetchedAt     string `json:"fetchedAt"`
	NextRefreshAt string `json:"nextRefreshAt"`
	Forecast      *struct {
		Score           int      `json:"score"`
		HorizonHours    int      `json:"horizonHours"`
		DaysSinceReset  *int64   `json:"daysSinceReset"`
		HoursSinceReset *float64 `json:"hoursSinceReset"`
		LatestResetAt   string   `json:"latestResetAt"`
		ResetAnnounced  bool     `json:"resetAnnounced"`
		State           string   `json:"state"`
		EvidenceTier    string   `json:"evidenceTier"`
		ModelVersion    string   `json:"modelVersion"`
		Breakdown       []struct {
			Label  string `json:"label"`
			Points int    `json:"points"`
		} `json:"breakdown"`
	} `json:"forecast"`
}

func New(data *store.Store, logger *slog.Logger, cfg Config) (*Manager, error) {
	if data == nil {
		return nil, errors.New("store is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultEndpoint
	}
	if cfg.Interval <= 0 {
		cfg.Interval = defaultInterval
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	} else if client.Timeout == 0 {
		client.Timeout = 12 * time.Second
	}
	return &Manager{store: data, log: logger, endpoint: cfg.Endpoint, interval: cfg.Interval, client: client}, nil
}

func (m *Manager) Run(ctx context.Context) {
	m.refreshAndLog(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshAndLog(ctx)
		}
	}
}

func (m *Manager) refreshAndLog(ctx context.Context) {
	if err := m.Refresh(ctx); err != nil {
		// 抓不到不是门户故障，保留旧值并降级为 warn
		m.log.Warn("codex forecast refresh failed", "error", err)
	}
}

// View 返回缓存中的预测数值，附带来源地址。
func (m *Manager) View(ctx context.Context) (store.CodexForecastState, error) {
	return m.store.CodexForecast(ctx)
}

func (m *Manager) Refresh(ctx context.Context) error {
	state, err := m.fetch(ctx)
	if err != nil {
		code := "FETCH_FAILED"
		var codeErr errorCode
		if errors.As(err, &codeErr) {
			code = codeErr.code
		}
		if markErr := m.store.MarkCodexForecastError(ctx, code); markErr != nil {
			return markErr
		}
		return err
	}
	return m.store.SaveCodexForecast(ctx, state)
}

type errorCode struct {
	code string
	err  error
}

func (e errorCode) Error() string { return e.err.Error() }
func (e errorCode) Unwrap() error { return e.err }

func (m *Manager) fetch(ctx context.Context) (store.CodexForecastState, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.endpoint, nil)
	if err != nil {
		return store.CodexForecastState{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return store.CodexForecastState{}, errorCode{code: "UNREACHABLE", err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return store.CodexForecastState{}, errorCode{
			code: fmt.Sprintf("HTTP_%d", response.StatusCode),
			err:  fmt.Errorf("unexpected status %d", response.StatusCode),
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyLen))
	if err != nil {
		return store.CodexForecastState{}, errorCode{code: "READ_FAILED", err: err}
	}
	var dto forecastDTO
	if err := json.Unmarshal(body, &dto); err != nil {
		return store.CodexForecastState{}, errorCode{code: "INVALID_JSON", err: err}
	}
	if dto.Forecast == nil {
		return store.CodexForecastState{}, errorCode{code: "NO_FORECAST", err: errors.New("payload has no forecast object")}
	}

	out := store.CodexForecastState{
		// 上游是 0-100 的整数分值，越界就夹住，免得写库时触发 CHECK
		Score:           clamp(dto.Forecast.Score, 0, 100),
		HorizonHours:    dto.Forecast.HorizonHours,
		DaysSinceReset:  dto.Forecast.DaysSinceReset,
		ResetAnnounced:  dto.Forecast.ResetAnnounced,
		ForecastState:   truncate(dto.Forecast.State, 32),
		EvidenceTier:    truncate(dto.Forecast.EvidenceTier, 32),
		ModelVersion:    truncate(dto.Forecast.ModelVersion, 32),
		SourceFetchedAt: parseUnix(dto.FetchedAt),
		NextRefreshAt:   parseUnix(dto.NextRefreshAt),
		LatestResetAt:   parseUnix(dto.Forecast.LatestResetAt),
	}
	if h := dto.Forecast.HoursSinceReset; h != nil && !math.IsNaN(*h) && !math.IsInf(*h, 0) {
		rounded := math.Round(*h*100) / 100
		out.HoursSinceReset = &rounded
	}
	out.Breakdown = make([]store.CodexForecastBreakdown, 0, len(dto.Forecast.Breakdown))
	for _, item := range dto.Forecast.Breakdown {
		if item.Label == "" {
			continue
		}
		out.Breakdown = append(out.Breakdown, store.CodexForecastBreakdown{
			Label: truncate(item.Label, 64), Points: item.Points,
		})
	}
	return out, nil
}

func parseUnix(value string) *int64 {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	unix := parsed.Unix()
	return &unix
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
