package store

import (
	"context"
	"database/sql"
)

// UpdateCheckState stores only public GitHub release metadata. A failed check
// keeps the last successful release so the admin page remains useful offline.
type UpdateCheckState struct {
	LatestVersion     string `json:"latest_version"`
	ReleaseURL        string `json:"release_url"`
	PublishedAt       *int64 `json:"published_at,omitempty"`
	Mode              string `json:"mode"`
	MinUpdaterVersion string `json:"min_updater_version"`
	CheckedAt         *int64 `json:"checked_at,omitempty"`
	LastSuccessAt     *int64 `json:"last_success_at,omitempty"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
}

func (s *Store) UpdateCheckState(ctx context.Context) (UpdateCheckState, error) {
	var state UpdateCheckState
	var publishedAt, checkedAt, lastSuccessAt sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT latest_version, release_url, published_at, mode, min_updater_version,
		       checked_at, last_success_at, last_error_code
		FROM update_check_state WHERE id = 1
	`).Scan(&state.LatestVersion, &state.ReleaseURL, &publishedAt, &state.Mode,
		&state.MinUpdaterVersion, &checkedAt, &lastSuccessAt, &state.LastErrorCode)
	if err != nil {
		return UpdateCheckState{}, err
	}
	state.PublishedAt = intPtr(publishedAt)
	state.CheckedAt = intPtr(checkedAt)
	state.LastSuccessAt = intPtr(lastSuccessAt)
	return state, nil
}

func (s *Store) SaveUpdateCheckSuccess(ctx context.Context, state UpdateCheckState, checkedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE update_check_state
		SET latest_version = ?, release_url = ?, published_at = ?, mode = ?,
		    min_updater_version = ?, checked_at = ?, last_success_at = ?,
		    last_error_code = '', updated_at = ?
		WHERE id = 1
	`, state.LatestVersion, state.ReleaseURL, nullInt(state.PublishedAt), state.Mode,
		state.MinUpdaterVersion, checkedAt, checkedAt, checkedAt)
	return err
}

func (s *Store) SaveUpdateCheckFailure(ctx context.Context, code string, checkedAt int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE update_check_state
		SET checked_at = ?, last_error_code = ?, updated_at = ?
		WHERE id = 1
	`, checkedAt, code, checkedAt)
	return err
}
