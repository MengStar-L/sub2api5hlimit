package httpapi

import (
	"context"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/releasecheck"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type Owner struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
}

type ProbeResult struct {
	Version      string              `json:"version"`
	Owners       []Owner             `json:"owners"`
	Keys         []store.KeySnapshot `json:"-"`
	AccountCount int                 `json:"account_count"`
}

type KeyCache struct {
	Items     []store.KeySnapshot
	FetchedAt time.Time
	LastError string
}

type QuotaResetResult struct {
	UpstreamKeyID   int64   `json:"upstream_key_id"`
	Applied         bool    `json:"applied"`
	SnapshotUpdated bool    `json:"snapshot_updated"`
	Usage5h         float64 `json:"-"`
	Usage7d         float64 `json:"-"`
	Reset5hAt       *int64  `json:"-"`
	Reset7dAt       *int64  `json:"-"`
	SourceUpdatedAt *int64  `json:"-"`
}

type KeyLimitResult struct {
	UpstreamKeyID   int64   `json:"upstream_key_id"`
	Applied         bool    `json:"applied"`
	SnapshotUpdated bool    `json:"snapshot_updated"`
	RateLimit5h     float64 `json:"-"`
	RateLimit7d     float64 `json:"-"`
	Usage5h         float64 `json:"-"`
	Usage7d         float64 `json:"-"`
	Reset5hAt       *int64  `json:"-"`
	Reset7dAt       *int64  `json:"-"`
	SourceUpdatedAt *int64  `json:"-"`
}

type UpstreamManager interface {
	Probe(context.Context, store.Settings, bool) (ProbeResult, error)
	CachedKeys() KeyCache
	BeginConnectionRotation() func()
	Sync(context.Context, string) error
	ResetQuota(context.Context, int64) (QuotaResetResult, error)
	SetKeyLimits(ctx context.Context, keyID int64, limit5h, limit7d float64, actorUserID int64) (KeyLimitResult, error)
}

type UpdateManager interface {
	View(context.Context) (releasecheck.View, error)
	Check(context.Context) (releasecheck.View, error)
	Apply(context.Context, string, int64) (releasecheck.ApplyResult, error)
}
