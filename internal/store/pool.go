package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
)

func normalizeStatus(status string, schedulable bool, missing bool, five *float64, seven *float64, errorCode string) string {
	if missing || status == "disabled" || status == "error" {
		return "unavailable"
	}
	if errorCode != "" || !schedulable {
		return "degraded"
	}
	if (five != nil && *five >= 100) || (seven != nil && *seven >= 100) {
		return "rate_limited"
	}
	return "healthy"
}

func newAlias() (string, error) {
	plain, _, err := secure.GenerateToken(5)
	if err != nil {
		return "", err
	}
	return "Pool-" + strings.ToUpper(plain[:6]), nil
}

func (s *Store) ApplyPoolInventory(ctx context.Context, items []PoolInventory, fetchedAt int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE pool_accounts SET missing=1, normalized_status='unavailable', updated_at=?`, fetchedAt); err != nil {
		return err
	}
	for _, item := range items {
		alias, err := newAlias()
		if err != nil {
			return err
		}
		status := normalizeStatus(item.Status, item.Schedulable, false, nil, nil, "")
		_, err = tx.ExecContext(ctx, `INSERT INTO pool_accounts (
          upstream_account_id, public_alias, published, missing, name, email, platform, account_type,
          plan_type, upstream_status, schedulable, normalized_status, last_seen_at, created_at, updated_at
        ) VALUES (?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(upstream_account_id) DO UPDATE SET missing=0, name=excluded.name, email=excluded.email,
          platform=excluded.platform, account_type=excluded.account_type, plan_type=excluded.plan_type,
          upstream_status=excluded.upstream_status, schedulable=excluded.schedulable,
          normalized_status=CASE WHEN pool_accounts.last_error_code!='' THEN 'degraded' ELSE excluded.normalized_status END,
          last_seen_at=excluded.last_seen_at, updated_at=excluded.updated_at`, item.UpstreamAccountID, alias,
			item.Name, item.Email, item.Platform, item.AccountType, item.PlanType, item.Status, boolInt(item.Schedulable),
			status, fetchedAt, fetchedAt, fetchedAt)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PublishedAccountIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT upstream_account_id FROM pool_accounts WHERE published=1 AND missing=0 ORDER BY upstream_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) ApplyPoolUsage(ctx context.Context, items []PoolUsage, fetchedAt int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		var upstreamStatus string
		var schedulable, missing int
		if err := tx.QueryRowContext(ctx, `SELECT upstream_status, schedulable, missing FROM pool_accounts WHERE upstream_account_id=?`, item.UpstreamAccountID).Scan(&upstreamStatus, &schedulable, &missing); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return err
		}
		if item.ErrorCode != "" {
			state := normalizeStatus(upstreamStatus, schedulable != 0, missing != 0, nil, nil, item.ErrorCode)
			if _, err := tx.ExecContext(ctx, `UPDATE pool_accounts SET normalized_status=?, last_error_code=?, updated_at=? WHERE upstream_account_id=?`,
				state, item.ErrorCode, fetchedAt, item.UpstreamAccountID); err != nil {
				return err
			}
			continue
		}
		state := normalizeStatus(upstreamStatus, schedulable != 0, missing != 0, item.FiveUtilization, item.SevenUtilization, item.ErrorCode)
		_, err := tx.ExecContext(ctx, `UPDATE pool_accounts SET normalized_status=?, five_supported=?, five_utilization=?, five_reset_at=?,
          seven_supported=?, seven_utilization=?, seven_reset_at=?, usage_source=?, source_updated_at=?,
          last_success_at=CASE WHEN ?='' THEN ? ELSE last_success_at END, last_error_code=?, updated_at=? WHERE upstream_account_id=?`,
			state, boolInt(item.FiveSupported), nullableFloat(item.FiveUtilization), nullInt(item.FiveResetAt),
			boolInt(item.SevenSupported), nullableFloat(item.SevenUtilization), nullInt(item.SevenResetAt), item.Source,
			nullInt(item.SourceUpdatedAt), item.ErrorCode, fetchedAt, item.ErrorCode, fetchedAt, item.UpstreamAccountID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullableFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func scanPool(scanner interface{ Scan(...any) error }) (PoolAccount, error) {
	var out PoolAccount
	var published, missing, schedulable, fiveSupported, sevenSupported int
	var fiveValue, sevenValue sql.NullFloat64
	var fiveReset, sevenReset, sourceAt, success sql.NullInt64
	err := scanner.Scan(&out.UpstreamAccountID, &out.PublicAlias, &published, &missing, &out.Name, &out.Email,
		&out.Platform, &out.AccountType, &out.PlanType, &out.UpstreamStatus, &schedulable, &out.NormalizedStatus,
		&fiveSupported, &fiveValue, &fiveReset, &sevenSupported, &sevenValue, &sevenReset, &out.UsageSource,
		&sourceAt, &success, &out.LastErrorCode, &out.LastSeenAt, &out.CreatedAt, &out.UpdatedAt)
	out.Published = published != 0
	out.Missing = missing != 0
	out.Schedulable = schedulable != 0
	out.FiveSupported = fiveSupported != 0
	out.FiveUtilization = floatPtr(fiveValue)
	out.FiveResetAt = intPtr(fiveReset)
	out.SevenSupported = sevenSupported != 0
	out.SevenUtilization = floatPtr(sevenValue)
	out.SevenResetAt = intPtr(sevenReset)
	out.SourceUpdatedAt = intPtr(sourceAt)
	out.LastSuccessAt = intPtr(success)
	return out, err
}

const poolColumns = `upstream_account_id, public_alias, published, missing, name, email, platform, account_type,
 plan_type, upstream_status, schedulable, normalized_status, five_supported, five_utilization, five_reset_at,
 seven_supported, seven_utilization, seven_reset_at, usage_source, source_updated_at, last_success_at,
 last_error_code, last_seen_at, created_at, updated_at`

func (s *Store) ListPool(ctx context.Context, publishedOnly bool) ([]PoolAccount, error) {
	query := `SELECT ` + poolColumns + ` FROM pool_accounts`
	if publishedOnly {
		query += ` WHERE published=1`
	}
	query += ` ORDER BY published DESC, normalized_status, name COLLATE NOCASE, upstream_account_id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PoolAccount, 0)
	for rows.Next() {
		item, err := scanPool(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetPoolPublished(ctx context.Context, ids []int64, published bool, actorID int64) error {
	if len(ids) == 0 {
		return errors.New("at least one account is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		query := `UPDATE pool_accounts SET published=?, updated_at=? WHERE upstream_account_id=?`
		if published {
			query += ` AND missing=0`
		}
		result, err := tx.ExecContext(ctx, query, boolInt(published), nowUnix(), id)
		if err != nil {
			return err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return fmt.Errorf("account %d: %w", id, ErrNotFound)
		}
	}
	if err := addAudit(ctx, tx, actorID, "pool.publish", "account", "batch", `{}`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkPoolUsageFailed(ctx context.Context, code string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE pool_accounts SET last_error_code=?, normalized_status=CASE WHEN missing=1 THEN 'unavailable' ELSE 'degraded' END, updated_at=? WHERE published=1`, code, nowUnix())
	return err
}
