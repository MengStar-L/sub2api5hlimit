package httpapi

import (
	"context"
	"time"

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

type UpstreamManager interface {
	Probe(context.Context, store.Settings, bool) (ProbeResult, error)
	CachedKeys() KeyCache
	ClearSnapshots()
	Sync(context.Context, string) error
}
