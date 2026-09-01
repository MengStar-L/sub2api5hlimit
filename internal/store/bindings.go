package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type sqlExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func bindingState(snapshot KeySnapshot) string {
	if !snapshot.Compliant() {
		return "invalid_limits"
	}
	if snapshot.Status != "active" {
		return "upstream_inactive"
	}
	return "healthy"
}

func insertBinding(ctx context.Context, tx sqlExecutor, userID int64, snapshot KeySnapshot, now int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO key_bindings (
        user_id, upstream_key_id, key_name, key_mask, upstream_status, binding_state,
        rate_limit_5h, usage_5h, reset_5h_at, rate_limit_7d, usage_7d, reset_7d_at,
        source_updated_at, last_success_at, created_at, updated_at
      ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, snapshot.UpstreamKeyID, snapshot.Name, snapshot.Mask, snapshot.Status, bindingState(snapshot),
		snapshot.RateLimit5h, snapshot.Usage5h, nullInt(snapshot.Reset5hAt), snapshot.RateLimit7d, snapshot.Usage7d,
		nullInt(snapshot.Reset7dAt), nullInt(snapshot.SourceUpdatedAt), now, now, now)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		return ErrKeyBound
	}
	return err
}

func scanBinding(scanner interface{ Scan(...any) error }) (KeyBinding, error) {
	var out KeyBinding
	var reset5, reset7, source, success sql.NullInt64
	err := scanner.Scan(&out.UserID, &out.UpstreamKeyID, &out.KeyName, &out.KeyMask, &out.UpstreamStatus,
		&out.BindingState, &out.RateLimit5h, &out.Usage5h, &reset5, &out.RateLimit7d, &out.Usage7d,
		&reset7, &source, &success, &out.LastErrorCode, &out.CreatedAt, &out.UpdatedAt)
	out.Reset5hAt = intPtr(reset5)
	out.Reset7dAt = intPtr(reset7)
	out.SourceUpdatedAt = intPtr(source)
	out.LastSuccessAt = intPtr(success)
	return out, err
}

const bindingColumns = `user_id, upstream_key_id, key_name, key_mask, upstream_status, binding_state,
 rate_limit_5h, usage_5h, reset_5h_at, rate_limit_7d, usage_7d, reset_7d_at,
 source_updated_at, last_success_at, last_error_code, created_at, updated_at`

func (s *Store) BindingByUser(ctx context.Context, userID int64) (KeyBinding, error) {
	out, err := scanBinding(s.db.QueryRowContext(ctx, `SELECT `+bindingColumns+` FROM key_bindings WHERE user_id=?`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return KeyBinding{}, ErrNotFound
	}
	return out, err
}

func (s *Store) ListBindings(ctx context.Context) ([]KeyBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+bindingColumns+` FROM key_bindings ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]KeyBinding, 0)
	for rows.Next() {
		item, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetBinding(ctx context.Context, userID int64, snapshot KeySnapshot, actorID int64) error {
	if !snapshot.Compliant() {
		return errors.New("upstream key must have positive 5h and 7d limits")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, role string
	if err := tx.QueryRowContext(ctx, `SELECT status, role FROM users WHERE id=?`, userID).Scan(&status, &role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status == StatusDeleted || role != RoleUser {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM key_bindings WHERE user_id=?`, userID); err != nil {
		return err
	}
	if err := insertBinding(ctx, tx, userID, snapshot, nowUnix()); err != nil {
		return err
	}
	if err := addAudit(ctx, tx, actorID, "binding.set", "user", fmt.Sprint(userID), `{}`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteBinding(ctx context.Context, userID, actorID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM key_bindings WHERE user_id=?`, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if err := addAudit(ctx, tx, actorID, "binding.delete", "user", fmt.Sprint(userID), `{}`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ApplyKeySnapshots(ctx context.Context, snapshots []KeySnapshot, fetchedAt int64) error {
	byID := make(map[int64]KeySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byID[snapshot.UpstreamKeyID] = snapshot
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT upstream_key_id FROM key_bindings`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		snapshot, ok := byID[id]
		if !ok {
			if _, err := tx.ExecContext(ctx, `UPDATE key_bindings SET binding_state='missing', last_error_code='UPSTREAM_KEY_MISSING', updated_at=? WHERE upstream_key_id=?`, fetchedAt, id); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE key_bindings SET key_name=?, key_mask=?, upstream_status=?, binding_state=?,
          rate_limit_5h=?, usage_5h=?, reset_5h_at=?, rate_limit_7d=?, usage_7d=?, reset_7d_at=?,
          source_updated_at=?, last_success_at=?, last_error_code='', updated_at=? WHERE upstream_key_id=?`,
			snapshot.Name, snapshot.Mask, snapshot.Status, bindingState(snapshot), snapshot.RateLimit5h, snapshot.Usage5h,
			nullInt(snapshot.Reset5hAt), snapshot.RateLimit7d, snapshot.Usage7d, nullInt(snapshot.Reset7dAt),
			nullInt(snapshot.SourceUpdatedAt), fetchedAt, fetchedAt, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkKeySyncFailed(ctx context.Context, code string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE key_bindings SET last_error_code=?, updated_at=?`, code, nowUnix())
	return err
}

// ApplyQuotaResetSnapshot stores only the safe usage fields returned by the
// official reset endpoint. Identity, limits, status and the masked key remain
// anchored to the existing binding until the next complete key-list sync.
func (s *Store) ApplyQuotaResetSnapshot(ctx context.Context, keyID int64, usage5h, usage7d float64, reset5h, reset7d, sourceUpdatedAt *int64, appliedAt int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE key_bindings SET
      usage_5h=?, reset_5h_at=?, usage_7d=?, reset_7d_at=?, source_updated_at=?,
      last_success_at=?, last_error_code='', updated_at=? WHERE upstream_key_id=?`,
		usage5h, nullInt(reset5h), usage7d, nullInt(reset7d), nullInt(sourceUpdatedAt), appliedAt, appliedAt, keyID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}
