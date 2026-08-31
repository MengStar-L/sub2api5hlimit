package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) SetSetupToken(ctx context.Context, hash string, expiresAt int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE app_meta SET setup_token_hash=?, setup_token_expires_at=?, updated_at=? WHERE id=1 AND setup_complete=0`, hash, expiresAt, nowUnix())
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return ErrSetupComplete
	}
	return nil
}

func (s *Store) SetupStatus(ctx context.Context) (SetupStatus, error) {
	var complete int
	var expires sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT setup_complete, setup_token_expires_at FROM app_meta WHERE id=1`).Scan(&complete, &expires); err != nil {
		return SetupStatus{}, err
	}
	status := SetupStatus{Complete: complete != 0}
	if expires.Valid && !status.Complete {
		status.ExpiresAt = expires.Int64
	}
	return status, nil
}

func (s *Store) ValidateSetupToken(ctx context.Context, tokenHash string) error {
	var complete int
	var storedHash sql.NullString
	var expires sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT setup_complete, setup_token_hash, setup_token_expires_at FROM app_meta WHERE id=1`).Scan(&complete, &storedHash, &expires); err != nil {
		return err
	}
	if complete != 0 {
		return ErrSetupComplete
	}
	if !storedHash.Valid || !expires.Valid || expires.Int64 <= nowUnix() ||
		subtle.ConstantTimeCompare([]byte(storedHash.String), []byte(tokenHash)) != 1 {
		return ErrInvalidToken
	}
	return nil
}

func (s *Store) CompleteSetup(ctx context.Context, tokenHash string, admin User, passwordHash string, settings Settings) error {
	ciphertext, err := s.box.Seal([]byte(settings.AdminAPIKey), settingsAAD)
	if err != nil {
		return fmt.Errorf("encrypt administrator API key: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var complete int
	var storedHash sql.NullString
	var expires sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT setup_complete, setup_token_hash, setup_token_expires_at FROM app_meta WHERE id=1`).Scan(&complete, &storedHash, &expires); err != nil {
		return err
	}
	if complete != 0 {
		return ErrSetupComplete
	}
	if !storedHash.Valid || !expires.Valid || expires.Int64 <= nowUnix() ||
		subtle.ConstantTimeCompare([]byte(storedHash.String), []byte(tokenHash)) != 1 {
		return ErrInvalidToken
	}
	now := nowUnix()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (username, display_name, password_hash, role, status, created_at, updated_at) VALUES (?, ?, ?, 'admin', 'active', ?, ?)`,
		strings.TrimSpace(admin.Username), strings.TrimSpace(admin.DisplayName), passwordHash, now, now); err != nil {
		return fmt.Errorf("create administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (
        id, connection_uuid, base_url, admin_api_key_cipher, owner_user_id, owner_label,
        allow_private_http, updated_at
    ) VALUES (1, ?, ?, ?, ?, ?, ?, ?)`, settings.ConnectionUUID, settings.BaseURL, ciphertext,
		settings.OwnerUserID, settings.OwnerLabel, boolInt(settings.AllowPrivateHTTP), now); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE app_meta SET setup_complete=1, setup_token_hash=NULL, setup_token_expires_at=NULL, updated_at=? WHERE id=1`, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	var out Settings
	var cipher string
	var private int
	var keySync, accountSync, usageSync sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT connection_uuid, base_url, admin_api_key_cipher, owner_user_id, owner_label,
        allow_private_http, last_key_sync_at, last_account_sync_at, last_usage_sync_at, updated_at FROM settings WHERE id=1`).Scan(
		&out.ConnectionUUID, &out.BaseURL, &cipher, &out.OwnerUserID, &out.OwnerLabel, &private,
		&keySync, &accountSync, &usageSync, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, ErrNotFound
	}
	if err != nil {
		return Settings{}, err
	}
	plain, err := s.box.Open(cipher, settingsAAD)
	if err != nil {
		return Settings{}, fmt.Errorf("decrypt administrator API key: %w", err)
	}
	out.AdminAPIKey = string(plain)
	clear(plain)
	out.AllowPrivateHTTP = private != 0
	out.LastKeySyncAt = intPtr(keySync)
	out.LastAccountSyncAt = intPtr(accountSync)
	out.LastUsageSyncAt = intPtr(usageSync)
	return out, nil
}

func (s *Store) UpdateSettings(ctx context.Context, next Settings, replaceKey bool, rotateConnection bool) error {
	current, err := s.GetSettings(ctx)
	if err != nil {
		return err
	}
	if !replaceKey {
		next.AdminAPIKey = current.AdminAPIKey
	}
	if !rotateConnection {
		next.ConnectionUUID = current.ConnectionUUID
	}
	ciphertext, err := s.box.Seal([]byte(next.AdminAPIKey), settingsAAD)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if rotateConnection {
		var bindings, published int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM key_bindings), (SELECT COUNT(*) FROM pool_accounts WHERE published=1)`).Scan(&bindings, &published); err != nil {
			return err
		}
		if bindings > 0 || published > 0 {
			return errors.New("unbind all users and unpublish all accounts before changing the upstream identity")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM pool_accounts`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE settings SET connection_uuid=?, base_url=?, admin_api_key_cipher=?, owner_user_id=?, owner_label=?,
        allow_private_http=?, last_key_sync_at=NULL, last_account_sync_at=NULL, last_usage_sync_at=NULL, updated_at=? WHERE id=1`,
		next.ConnectionUUID, next.BaseURL, ciphertext, next.OwnerUserID, next.OwnerLabel, boolInt(next.AllowPrivateHTTP), nowUnix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkSync(ctx context.Context, scope string, at int64) error {
	column := map[string]string{"keys": "last_key_sync_at", "accounts": "last_account_sync_at", "usage": "last_usage_sync_at"}[scope]
	if column == "" {
		return errors.New("unknown sync scope")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE settings SET `+column+`=?, updated_at=? WHERE id=1`, at, nowUnix())
	return err
}
