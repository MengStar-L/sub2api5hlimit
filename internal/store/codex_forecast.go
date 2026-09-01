package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

const codexForecastColumns = `score, breakdown, horizon_hours, days_since_reset, hours_since_reset,
 latest_reset_at, reset_announced, forecast_state, evidence_tier, model_version,
 source_fetched_at, next_refresh_at, checked_at, last_success_at, last_error_code, updated_at`

func (s *Store) CodexForecast(ctx context.Context) (CodexForecastState, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+codexForecastColumns+` FROM codex_forecast_state WHERE id = 1`)
	var out CodexForecastState
	var breakdown string
	var days, latestReset, sourceFetched, nextRefresh, checked, lastSuccess sql.NullInt64
	var hours sql.NullFloat64
	if err := row.Scan(&out.Score, &breakdown, &out.HorizonHours, &days, &hours,
		&latestReset, &out.ResetAnnounced, &out.ForecastState, &out.EvidenceTier, &out.ModelVersion,
		&sourceFetched, &nextRefresh, &checked, &lastSuccess, &out.LastErrorCode, &out.UpdatedAt); err != nil {
		return CodexForecastState{}, err
	}
	out.DaysSinceReset = intPtr(days)
	out.LatestResetAt = intPtr(latestReset)
	out.SourceFetchedAt = intPtr(sourceFetched)
	out.NextRefreshAt = intPtr(nextRefresh)
	out.CheckedAt = intPtr(checked)
	out.LastSuccessAt = intPtr(lastSuccess)
	if hours.Valid {
		value := hours.Float64
		out.HoursSinceReset = &value
	}
	out.Breakdown = make([]CodexForecastBreakdown, 0, 4)
	if breakdown != "" {
		// 缓存里的坏 JSON 不该让整个请求失败，退化成空明细
		_ = json.Unmarshal([]byte(breakdown), &out.Breakdown)
	}
	return out, nil
}

// SaveCodexForecast 覆盖单行缓存，并记录成功时间。
func (s *Store) SaveCodexForecast(ctx context.Context, state CodexForecastState) error {
	breakdown, err := json.Marshal(state.Breakdown)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx,
		`UPDATE codex_forecast_state SET
		   score = ?, breakdown = ?, horizon_hours = ?, days_since_reset = ?, hours_since_reset = ?,
		   latest_reset_at = ?, reset_announced = ?, forecast_state = ?, evidence_tier = ?, model_version = ?,
		   source_fetched_at = ?, next_refresh_at = ?, checked_at = ?, last_success_at = ?,
		   last_error_code = '', updated_at = ?
		 WHERE id = 1`,
		state.Score, string(breakdown), state.HorizonHours, state.DaysSinceReset, state.HoursSinceReset,
		state.LatestResetAt, state.ResetAnnounced, state.ForecastState, state.EvidenceTier, state.ModelVersion,
		state.SourceFetchedAt, state.NextRefreshAt, now, now, now)
	return err
}

// MarkCodexForecastError 只记录失败，保留上一次成功抓到的数值。
func (s *Store) MarkCodexForecastError(ctx context.Context, code string) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`UPDATE codex_forecast_state SET checked_at = ?, last_error_code = ?, updated_at = ? WHERE id = 1`,
		now, code, now)
	return err
}
