package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	_ "modernc.org/sqlite"
)

const settingsAAD = "settings:sub2api-admin-key:v1"

var (
	ErrNotFound             = errors.New("not found")
	ErrSetupComplete        = errors.New("setup already complete")
	ErrInvalidToken         = errors.New("invalid or expired setup token")
	ErrUsernameExists       = errors.New("username already exists")
	ErrKeyBound             = errors.New("upstream key is already bound")
	ErrQuotaResetJobActive  = errors.New("a quota reset job is already active")
	ErrQuotaResetTransition = errors.New("invalid quota reset state transition")
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db  *sql.DB
	box *secure.Box
}

func Open(ctx context.Context, path string, box *secure.Box) (*Store, error) {
	if box == nil {
		return nil, errors.New("encryption box is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(abs)+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	store := &Store{db: db, box: box}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		contents, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, string(contents)); err != nil {
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func nowUnix() int64 { return time.Now().Unix() }

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func intPtr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func floatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	return &value.Float64
}
