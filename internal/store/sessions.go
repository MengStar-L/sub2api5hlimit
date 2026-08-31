package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) CreateSession(ctx context.Context, tokenHash, csrfHash string, userID, now, idleExpires, absoluteExpires int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, csrf_hash, user_id, created_at, last_seen_at, idle_expires_at, absolute_expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash, csrfHash, userID, now, now, idleExpires, absoluteExpires)
	return err
}

func (s *Store) Session(ctx context.Context, tokenHash string, now, nextIdle int64) (Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback()
	var session Session
	var lastLogin, deleted sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT s.token_hash, s.csrf_hash, s.created_at, s.last_seen_at, s.idle_expires_at, s.absolute_expires_at,
        u.id, u.username, u.display_name, u.password_hash, u.role, u.status, u.last_login_at, u.deleted_at, u.created_at, u.updated_at
      FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, tokenHash).Scan(
		&session.TokenHash, &session.CSRFHash, &session.CreatedAt, &session.LastSeenAt, &session.IdleExpiresAt, &session.AbsoluteExpiresAt,
		&session.User.ID, &session.User.Username, &session.User.DisplayName, &session.User.PasswordHash, &session.User.Role,
		&session.User.Status, &lastLogin, &deleted, &session.User.CreatedAt, &session.User.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	session.User.LastLoginAt = intPtr(lastLogin)
	session.User.DeletedAt = intPtr(deleted)
	if session.IdleExpiresAt <= now || session.AbsoluteExpiresAt <= now || session.User.Status != StatusActive {
		_, _ = tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
		_ = tx.Commit()
		return Session{}, ErrNotFound
	}
	if nextIdle > session.AbsoluteExpiresAt {
		nextIdle = session.AbsoluteExpiresAt
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen_at=?, idle_expires_at=? WHERE token_hash=?`, now, nextIdle, tokenHash); err != nil {
		return Session{}, err
	}
	session.LastSeenAt = now
	session.IdleExpiresAt = nextIdle
	if err := tx.Commit(); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

func (s *Store) RecordLogin(ctx context.Context, userID int64) error {
	now := nowUnix()
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_login_at=?, updated_at=? WHERE id=?`, now, now, userID)
	return err
}
