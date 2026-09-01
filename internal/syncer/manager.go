package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/MengStar-L/sub2api5hlimit/internal/httpapi"
	"github.com/MengStar-L/sub2api5hlimit/internal/secure"
	"github.com/MengStar-L/sub2api5hlimit/internal/store"
	"github.com/MengStar-L/sub2api5hlimit/internal/sub2api"
	"golang.org/x/sync/singleflight"
)

const (
	keyInterval     = 15 * time.Second
	accountInterval = 5 * time.Minute
	usageInterval   = 60 * time.Second
	maxBackoff      = 5 * time.Minute
)

type Manager struct {
	store *store.Store
	log   *slog.Logger

	group singleflight.Group
	limit chan struct{}

	connectionMu         sync.Mutex
	connectionGeneration uint64
	keySnapshotMu        sync.Mutex
	keyGeneration        uint64

	cacheMu sync.RWMutex
	keys    httpapi.KeyCache
}

func New(data *store.Store, logger *slog.Logger) *Manager {
	return &Manager{store: data, log: logger, limit: make(chan struct{}, 4)}
}

func (m *Manager) CachedKeys() httpapi.KeyCache {
	m.cacheMu.RLock()
	defer m.cacheMu.RUnlock()
	copyItems := append([]store.KeySnapshot(nil), m.keys.Items...)
	return httpapi.KeyCache{Items: copyItems, FetchedAt: m.keys.FetchedAt, LastError: m.keys.LastError}
}

func (m *Manager) BeginConnectionRotation() func() {
	m.connectionMu.Lock()
	m.connectionGeneration++
	for _, scope := range []string{"keys", "accounts", "usage"} {
		m.group.Forget(scope)
	}
	m.cacheMu.Lock()
	m.keys = httpapi.KeyCache{}
	m.cacheMu.Unlock()
	var once sync.Once
	return func() { once.Do(m.connectionMu.Unlock) }
}

func (m *Manager) Probe(ctx context.Context, settings store.Settings, full bool) (httpapi.ProbeResult, error) {
	client, err := newClient(settings)
	if err != nil {
		return httpapi.ProbeResult{}, err
	}
	version, err := client.CheckVersion(ctx)
	if err != nil {
		return httpapi.ProbeResult{}, err
	}
	users, err := client.ListUsers(ctx)
	if err != nil {
		return httpapi.ProbeResult{}, err
	}
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return httpapi.ProbeResult{}, err
	}
	probeIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		probeIDs = append(probeIDs, account.ID)
	}
	if _, err := client.BatchAccountUsage(ctx, probeIDs); err != nil {
		return httpapi.ProbeResult{}, err
	}
	result := httpapi.ProbeResult{Version: version.String(), AccountCount: len(accounts), Owners: make([]httpapi.Owner, 0, len(users))}
	ownerFound := !full
	for _, user := range users {
		result.Owners = append(result.Owners, httpapi.Owner{ID: user.ID, Email: user.Email, Username: user.Username})
		if user.ID == settings.OwnerUserID {
			ownerFound = true
		}
	}
	if !ownerFound {
		return httpapi.ProbeResult{}, fmt.Errorf("configured Sub2API owner %d was not found", settings.OwnerUserID)
	}
	if full {
		keys, err := client.ListAPIKeys(ctx, settings.OwnerUserID)
		if err != nil {
			return httpapi.ProbeResult{}, err
		}
		result.Keys = keySnapshots(keys)
	}
	return result, nil
}

func (m *Manager) Sync(ctx context.Context, scope string) error {
	if scope == "all" {
		var joined error
		for _, part := range []string{"keys", "accounts", "usage"} {
			if err := m.Sync(ctx, part); err != nil {
				joined = errors.Join(joined, fmt.Errorf("%s: %w", part, err))
			}
		}
		return joined
	}
	if scope != "keys" && scope != "accounts" && scope != "usage" {
		return fmt.Errorf("unknown sync scope %q", scope)
	}
	result := m.group.DoChan(scope, func() (any, error) {
		select {
		case m.limit <- struct{}{}:
			defer func() { <-m.limit }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return nil, m.syncOne(ctx, scope)
	})
	select {
	case call := <-result:
		return call.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResetQuota applies the official all-window reset for one upstream key and
// immediately persists the endpoint's safe usage snapshot. The HTTP workflow
// still triggers a complete key-list sync (once for a batch) to reconcile all
// other key metadata.
func (m *Manager) ResetQuota(ctx context.Context, keyID int64) (httpapi.QuotaResetResult, error) {
	result := httpapi.QuotaResetResult{UpstreamKeyID: keyID}
	connectionGeneration, settings, err := m.connectionSettings(ctx)
	if err != nil {
		return result, err
	}
	client, err := newClient(settings)
	if err != nil {
		return result, err
	}
	reset, err := client.ResetAPIKeyRateLimitUsage(ctx, keyID)
	if err != nil {
		return result, err
	}
	result.Applied = true
	result.Usage5h = reset.Usage5h
	result.Usage7d = reset.Usage7d
	result.Reset5hAt = store.UnixPtr(reset.Reset5hAt)
	result.Reset7dAt = store.UnixPtr(reset.Reset7dAt)
	result.SourceUpdatedAt = store.UnixPtr(reset.UpdatedAt)
	appliedAt := time.Now().Unix()
	m.connectionMu.Lock()
	if connectionGeneration != m.connectionGeneration {
		m.connectionMu.Unlock()
		return result, fmt.Errorf("upstream connection changed after confirmed quota reset")
	}
	m.keySnapshotMu.Lock()
	m.keyGeneration++
	// A keys sync that started before this confirmed mutation must not publish
	// its old response, and the caller's reconciliation must start a new flight.
	m.group.Forget("keys")
	if err := m.store.ApplyQuotaResetSnapshot(ctx, keyID, result.Usage5h, result.Usage7d,
		result.Reset5hAt, result.Reset7dAt, result.SourceUpdatedAt, appliedAt); err != nil {
		m.keySnapshotMu.Unlock()
		m.connectionMu.Unlock()
		return result, fmt.Errorf("persist confirmed quota reset snapshot: %w", err)
	}
	m.keySnapshotMu.Unlock()
	m.connectionMu.Unlock()
	result.SnapshotUpdated = true
	return result, nil
}

func (m *Manager) syncOne(ctx context.Context, scope string) error {
	connectionGeneration, settings, err := m.connectionSettings(ctx)
	if err != nil {
		return err
	}
	client, err := newClient(settings)
	if err != nil {
		return err
	}
	switch scope {
	case "keys":
		return m.syncKeys(ctx, client, settings.OwnerUserID, connectionGeneration)
	case "accounts":
		return m.syncAccounts(ctx, client, connectionGeneration)
	case "usage":
		return m.syncUsage(ctx, client, connectionGeneration)
	default:
		return fmt.Errorf("unknown sync scope %q", scope)
	}
}

func (m *Manager) connectionSettings(ctx context.Context) (uint64, store.Settings, error) {
	m.connectionMu.Lock()
	defer m.connectionMu.Unlock()
	settings, err := m.store.GetSettings(ctx)
	return m.connectionGeneration, settings, err
}

func (m *Manager) withConnectionGeneration(generation uint64, apply func() error) (bool, error) {
	m.connectionMu.Lock()
	defer m.connectionMu.Unlock()
	if generation != m.connectionGeneration {
		return false, nil
	}
	return true, apply()
}

func (m *Manager) syncKeys(ctx context.Context, client *sub2api.Client, ownerID int64, connectionGeneration uint64) error {
	m.keySnapshotMu.Lock()
	generation := m.keyGeneration
	m.keySnapshotMu.Unlock()
	keys, err := client.ListAPIKeys(ctx, ownerID)
	if err != nil {
		current, applyErr := m.withConnectionGeneration(connectionGeneration, func() error {
			m.keySnapshotMu.Lock()
			defer m.keySnapshotMu.Unlock()
			if generation != m.keyGeneration {
				return nil
			}
			code := syncErrorCode(err)
			_ = m.store.MarkKeySyncFailed(context.WithoutCancel(ctx), code)
			m.cacheMu.Lock()
			m.keys.LastError = code
			m.cacheMu.Unlock()
			return nil
		})
		if applyErr != nil {
			return applyErr
		}
		if !current {
			return nil
		}
		return err
	}
	now := time.Now()
	snapshots := keySnapshots(keys)
	_, err = m.withConnectionGeneration(connectionGeneration, func() error {
		m.keySnapshotMu.Lock()
		defer m.keySnapshotMu.Unlock()
		if generation != m.keyGeneration {
			return nil
		}
		if err := m.store.ApplyKeySnapshots(ctx, snapshots, now.Unix()); err != nil {
			return err
		}
		if err := m.store.MarkSync(ctx, "keys", now.Unix()); err != nil {
			return err
		}
		m.cacheMu.Lock()
		m.keys = httpapi.KeyCache{Items: snapshots, FetchedAt: now}
		m.cacheMu.Unlock()
		return nil
	})
	return err
}

func (m *Manager) syncAccounts(ctx context.Context, client *sub2api.Client, connectionGeneration uint64) error {
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	items := make([]store.PoolInventory, 0, len(accounts))
	for _, account := range accounts {
		items = append(items, store.PoolInventory{
			UpstreamAccountID: account.ID, Name: account.Name, Email: account.Email, Platform: account.Platform,
			AccountType: account.Type, PlanType: account.PlanType, Status: account.Status, Schedulable: account.Schedulable,
		})
	}
	_, err = m.withConnectionGeneration(connectionGeneration, func() error {
		if err := m.store.ApplyPoolInventory(ctx, items, now); err != nil {
			return err
		}
		return m.store.MarkSync(ctx, "accounts", now)
	})
	return err
}

func (m *Manager) syncUsage(ctx context.Context, client *sub2api.Client, connectionGeneration uint64) error {
	ids, err := m.store.PublishedAccountIDs(ctx)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	if len(ids) == 0 {
		_, err := m.withConnectionGeneration(connectionGeneration, func() error { return m.store.MarkSync(ctx, "usage", now) })
		return err
	}
	result, err := client.BatchAccountUsage(ctx, ids)
	if err != nil {
		current, applyErr := m.withConnectionGeneration(connectionGeneration, func() error {
			return m.store.MarkPoolUsageFailed(context.WithoutCancel(ctx), syncErrorCode(err))
		})
		if applyErr != nil {
			return applyErr
		}
		if !current {
			return nil
		}
		return err
	}
	items := make([]store.PoolUsage, 0, len(ids))
	for _, id := range ids {
		item := store.PoolUsage{UpstreamAccountID: id}
		usage, ok := result.Usage[id]
		if ok {
			item.Source = safeUsageSource(usage.Source)
			item.SourceUpdatedAt = store.UnixPtr(usage.UpdatedAt)
			if usage.FiveHour != nil {
				item.FiveSupported = true
				value := usage.FiveHour.Utilization
				item.FiveUtilization = &value
				item.FiveResetAt = store.UnixPtr(usage.FiveHour.ResetsAt)
			}
			if usage.SevenDay != nil {
				item.SevenSupported = true
				value := usage.SevenDay.Utilization
				item.SevenUtilization = &value
				item.SevenResetAt = store.UnixPtr(usage.SevenDay.ResetsAt)
			}
			if usage.ErrorCode != "" {
				item.ErrorCode = "UPSTREAM_ACCOUNT_ERROR"
			}
		}
		if _, failed := result.Errors[id]; failed {
			item.ErrorCode = "UPSTREAM_ACCOUNT_ERROR"
		} else if !ok {
			item.ErrorCode = "UPSTREAM_USAGE_MISSING"
		}
		items = append(items, item)
	}
	_, err = m.withConnectionGeneration(connectionGeneration, func() error {
		if err := m.store.ApplyPoolUsage(ctx, items, now); err != nil {
			return err
		}
		return m.store.MarkSync(ctx, "usage", now)
	})
	return err
}

func safeUsageSource(value string) string {
	switch value {
	case "passive", "active":
		return value
	default:
		return "upstream"
	}
}

func newClient(settings store.Settings) (*sub2api.Client, error) {
	return sub2api.NewClient(sub2api.Config{
		BaseURL: settings.BaseURL, APIKey: settings.AdminAPIKey, AllowPrivateHTTP: settings.AllowPrivateHTTP,
		Timeout: 10 * time.Second, MaxResponseBytes: 2 << 20, PageSize: 100,
	})
}

func keySnapshots(keys []sub2api.APIKey) []store.KeySnapshot {
	items := make([]store.KeySnapshot, 0, len(keys))
	for _, key := range keys {
		items = append(items, store.KeySnapshot{
			UpstreamKeyID: key.ID, Name: key.Name, Mask: key.MaskedKey, Status: key.Status,
			RateLimit5h: key.RateLimit5h, Usage5h: key.Usage5h, Reset5hAt: store.UnixPtr(key.Reset5hAt),
			RateLimit7d: key.RateLimit7d, Usage7d: key.Usage7d, Reset7dAt: store.UnixPtr(key.Reset7dAt),
			SourceUpdatedAt: store.UnixPtr(key.SourceUpdatedAt),
		})
	}
	return items
}

func syncErrorCode(err error) string {
	var upstream *sub2api.UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "UPSTREAM_AUTH"
		case http.StatusTooManyRequests:
			return "UPSTREAM_RATE_LIMITED"
		default:
			if upstream.StatusCode >= 500 {
				return "UPSTREAM_UNAVAILABLE"
			}
		}
	}
	var schema *sub2api.SchemaError
	if errors.As(err, &schema) {
		return "UPSTREAM_SCHEMA"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "UPSTREAM_TIMEOUT"
	}
	return "UPSTREAM_ERROR"
}

func (m *Manager) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, job := range []struct {
		scope    string
		interval time.Duration
	}{{"keys", keyInterval}, {"accounts", accountInterval}, {"usage", usageInterval}} {
		wg.Add(1)
		go func(scope string, interval time.Duration) {
			defer wg.Done()
			m.runLoop(ctx, scope, interval)
		}(job.scope, job.interval)
	}
	wg.Wait()
}

func (m *Manager) runLoop(ctx context.Context, scope string, base time.Duration) {
	failures := 0
	timer := time.NewTimer(jitter(min(base, 3*time.Second)))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := m.Sync(ctx, scope)
			if err != nil {
				failures++
				if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, context.Canceled) {
					m.log.Warn("scheduled upstream sync failed", "scope", scope, "error", secure.Redact(err.Error()))
				}
			} else {
				failures = 0
			}
			timer.Reset(nextDelay(base, failures))
		}
	}
}

func nextDelay(base time.Duration, failures int) time.Duration {
	delay := base
	for range min(failures, 8) {
		if delay >= maxBackoff/2 {
			delay = maxBackoff
			break
		}
		delay *= 2
	}
	return jitter(min(delay, maxBackoff))
}

func jitter(value time.Duration) time.Duration {
	if value <= 0 {
		return time.Millisecond
	}
	span := max(int64(value/5), 1)
	return value - value/10 + time.Duration(rand.Int64N(span))
}
