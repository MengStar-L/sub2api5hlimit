package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func scanUser(scanner interface{ Scan(...any) error }) (User, error) {
	var out User
	var lastLogin, deleted sql.NullInt64
	err := scanner.Scan(&out.ID, &out.Username, &out.DisplayName, &out.PasswordHash, &out.Role, &out.Status,
		&lastLogin, &deleted, &out.CreatedAt, &out.UpdatedAt)
	out.LastLoginAt = intPtr(lastLogin)
	out.DeletedAt = intPtr(deleted)
	return out, err
}

const userColumns = `id, username, display_name, password_hash, role, status, last_login_at, deleted_at, created_at, updated_at`

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	out, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username=? AND status!='deleted'`, strings.TrimSpace(username)))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return out, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	out, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err == nil {
		binding, bindErr := s.BindingByUser(ctx, id)
		if bindErr == nil {
			out.Binding = &binding
		} else if !errors.Is(bindErr, ErrNotFound) {
			return User{}, bindErr
		}
	}
	return out, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users WHERE role='user' AND status!='deleted' ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	bindings, err := s.ListBindings(ctx)
	if err != nil {
		return nil, err
	}
	byUser := make(map[int64]KeyBinding, len(bindings))
	for _, binding := range bindings {
		byUser[binding.UserID] = binding
	}
	for index := range users {
		if binding, ok := byUser[users[index].ID]; ok {
			copy := binding
			users[index].Binding = &copy
		}
	}
	return users, nil
}

func (s *Store) CreateUser(ctx context.Context, username, displayName, passwordHash string, snapshot *KeySnapshot, actorID int64) (User, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	now := nowUnix()
	result, err := tx.ExecContext(ctx, `INSERT INTO users (username, display_name, password_hash, role, status, created_at, updated_at) VALUES (?, ?, ?, 'user', 'active', ?, ?)`,
		strings.TrimSpace(username), strings.TrimSpace(displayName), passwordHash, now, now)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return User{}, ErrUsernameExists
		}
		return User{}, err
	}
	userID, _ := result.LastInsertId()
	if snapshot != nil {
		if !snapshot.Compliant() {
			return User{}, errors.New("upstream key must have positive 5h and 7d limits")
		}
		if err := insertBinding(ctx, tx, userID, *snapshot, now); err != nil {
			return User{}, err
		}
	}
	if err := addAudit(ctx, tx, actorID, "user.create", "user", fmt.Sprint(userID), `{}`); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, userID)
}

func (s *Store) UpdateUser(ctx context.Context, id int64, displayName, status string, actorID int64) error {
	if status != StatusActive && status != StatusDisabled {
		return errors.New("invalid user status")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET display_name=?, status=?, updated_at=? WHERE id=? AND role='user' AND status!='deleted'`,
		strings.TrimSpace(displayName), status, nowUnix(), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if status == StatusDisabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id); err != nil {
			return err
		}
	}
	if err := addAudit(ctx, tx, actorID, "user.update", "user", fmt.Sprint(id), `{}`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserPassword(ctx context.Context, id int64, passwordHash string, actorID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?, updated_at=? WHERE id=? AND status!='deleted'`, passwordHash, nowUnix(), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id); err != nil {
		return err
	}
	if err := addAudit(ctx, tx, actorID, "user.password_reset", "user", fmt.Sprint(id), `{}`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteUser(ctx context.Context, id, actorID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowUnix()
	result, err := tx.ExecContext(ctx, `UPDATE users SET status='deleted', deleted_at=?, updated_at=? WHERE id=? AND role='user' AND status!='deleted'`, now, now, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM key_bindings WHERE user_id=?`, id); err != nil {
		return err
	}
	if err := addAudit(ctx, tx, actorID, "user.delete", "user", fmt.Sprint(id), `{}`); err != nil {
		return err
	}
	return tx.Commit()
}
