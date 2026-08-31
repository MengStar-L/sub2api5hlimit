package httpapi

import (
	"math"
	"net/http"
	"strings"
	"time"
)

type snapshotMeta struct {
	AsOf            int64  `json:"as_of"`
	SourceUpdatedAt *int64 `json:"source_updated_at,omitempty"`
	LastSuccessAt   *int64 `json:"last_success_at,omitempty"`
	Stale           bool   `json:"stale"`
}

type keyWindowView struct {
	LimitUSD     float64 `json:"limit_usd"`
	UsedUSD      float64 `json:"used_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	Percent      float64 `json:"percent"`
	ResetAt      *int64  `json:"reset_at,omitempty"`
}

type keyView struct {
	Name           string        `json:"name"`
	Mask           string        `json:"masked_key"`
	Status         string        `json:"status"`
	UpstreamStatus string        `json:"upstream_status"`
	FiveHour       keyWindowView `json:"window_5h"`
	SevenDay       keyWindowView `json:"window_7d"`
	Snapshot       snapshotMeta  `json:"snapshot"`
}

type poolWindowView struct {
	Supported   bool     `json:"supported"`
	Utilization *float64 `json:"utilization,omitempty"`
	ResetAt     *int64   `json:"reset_at,omitempty"`
}

type publicPoolView struct {
	ID       string         `json:"id"`
	Alias    string         `json:"alias"`
	Account  string         `json:"account"`
	Provider string         `json:"provider"`
	PlanType string         `json:"plan_type"`
	Status   string         `json:"status"`
	FiveHour poolWindowView `json:"window_5h"`
	SevenDay poolWindowView `json:"window_7d"`
	Snapshot snapshotMeta   `json:"snapshot"`
}

func windowView(limit, used float64, reset *int64) keyWindowView {
	remaining := math.Max(0, limit-used)
	percent := float64(0)
	if limit > 0 {
		percent = math.Min(100, math.Max(0, used/limit*100))
	}
	return keyWindowView{LimitUSD: limit, UsedUSD: used, RemainingUSD: remaining, Percent: percent, ResetAt: reset}
}

func maskEmail(value, fallback string) string {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fallback
	}
	runes := []rune(parts[0])
	return string(runes[0]) + "***@" + parts[1]
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	session := currentSession(r)
	binding, bindErr := s.store.BindingByUser(r.Context(), session.User.ID)
	pool, err := s.store.ListPool(r.Context(), true)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	now := time.Now().Unix()
	var key any
	if bindErr == nil {
		key = keyView{Name: binding.KeyName, Mask: binding.KeyMask, Status: binding.BindingState,
			UpstreamStatus: binding.UpstreamStatus, FiveHour: windowView(binding.RateLimit5h, binding.Usage5h, binding.Reset5hAt),
			SevenDay: windowView(binding.RateLimit7d, binding.Usage7d, binding.Reset7dAt),
			Snapshot: snapshotMeta{AsOf: now, SourceUpdatedAt: binding.SourceUpdatedAt, LastSuccessAt: binding.LastSuccessAt,
				Stale: binding.LastSuccessAt == nil || now-*binding.LastSuccessAt > 45}}
	}
	accounts := make([]publicPoolView, 0, len(pool))
	for _, account := range pool {
		accounts = append(accounts, publicPoolView{ID: account.PublicAlias, Alias: account.PublicAlias, Account: maskEmail(account.Email, account.PublicAlias),
			Provider: account.Platform, PlanType: account.PlanType, Status: account.NormalizedStatus,
			FiveHour: poolWindowView{Supported: account.FiveSupported, Utilization: account.FiveUtilization, ResetAt: account.FiveResetAt},
			SevenDay: poolWindowView{Supported: account.SevenSupported, Utilization: account.SevenUtilization, ResetAt: account.SevenResetAt},
			Snapshot: snapshotMeta{AsOf: now, SourceUpdatedAt: account.SourceUpdatedAt, LastSuccessAt: account.LastSuccessAt,
				Stale: account.LastSuccessAt == nil || now-*account.LastSuccessAt > 180}})
	}
	writeData(w, 200, map[string]any{"user": safeUser(session.User), "key": key, "pool": accounts, "snapshot": snapshotMeta{AsOf: now}})
}
