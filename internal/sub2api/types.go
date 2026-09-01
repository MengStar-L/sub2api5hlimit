package sub2api

import (
	"net/http"
	"strings"
	"time"
)

const (
	// MinimumVersion is the oldest Sub2API version supported by this portal.
	MinimumVersion = "0.1.183"

	defaultTimeout          = 10 * time.Second
	defaultMaxResponseBytes = int64(2 << 20)
	maximumResponseBytes    = int64(16 << 20)
	defaultPageSize         = 100
	maximumPageSize         = 1000
)

// Config controls access to the Sub2API admin API. HTTP is intentionally
// restricted to explicitly approved loopback/private literal addresses.
type Config struct {
	BaseURL          string
	APIKey           string
	AllowPrivateHTTP bool
	Timeout          time.Duration
	MaxResponseBytes int64
	PageSize         int
	HTTPClient       *http.Client
}

// User is the allowlisted subset of an upstream admin user used to select the
// owner whose API keys are distributed by the portal.
type User struct {
	ID        int64      `json:"id"`
	Email     string     `json:"email"`
	Username  string     `json:"username"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// APIKey is safe to persist or return from the portal. It deliberately cannot
// represent the raw upstream key or the last-used IP address.
type APIKey struct {
	ID              int64      `json:"id"`
	UserID          int64      `json:"user_id"`
	MaskedKey       string     `json:"masked_key"`
	Name            string     `json:"name"`
	GroupID         *int64     `json:"group_id"`
	Status          string     `json:"status"`
	Quota           float64    `json:"quota"`
	QuotaUsed       float64    `json:"quota_used"`
	RateLimit5h     float64    `json:"rate_limit_5h"`
	RateLimit7d     float64    `json:"rate_limit_7d"`
	Usage5h         float64    `json:"usage_5h"`
	Usage7d         float64    `json:"usage_7d"`
	Window5hStart   *time.Time `json:"window_5h_start"`
	Window7dStart   *time.Time `json:"window_7d_start"`
	Reset5hAt       *time.Time `json:"reset_5h_at"`
	Reset7dAt       *time.Time `json:"reset_7d_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	ExpiresAt       *time.Time `json:"expires_at"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
}

// APIKeyReset is the allowlisted result of an upstream rate-limit reset. The
// upstream response also contains the raw key and last-used IP, neither of
// which can be represented by this type.
type APIKeyReset struct {
	ID        int64      `json:"id"`
	Usage5h   float64    `json:"usage_5h"`
	Usage7d   float64    `json:"usage_7d"`
	Reset5hAt *time.Time `json:"reset_5h_at"`
	Reset7dAt *time.Time `json:"reset_7d_at"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// HasRequiredLimits reports whether both portal-required windows are enabled.
func (k APIKey) HasRequiredLimits() bool {
	return k.RateLimit5h > 0 && k.RateLimit7d > 0
}

// MaskAPIKey returns a display-only representation. For normal Sub2API keys it
// produces the form "sk-…abcd" while never exposing short key material.
func MaskAPIKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	prefix := ""
	if strings.HasPrefix(raw, "sk-") {
		prefix = "sk-"
		raw = strings.TrimPrefix(raw, prefix)
	}
	if len(raw) <= 4 {
		return prefix + "…"
	}
	return prefix + "…" + raw[len(raw)-4:]
}

// Account is a safe account-pool record. Arbitrary upstream credentials and
// extra metadata are intentionally absent from this type.
type Account struct {
	ID                   int64      `json:"id"`
	Name                 string     `json:"name"`
	Email                string     `json:"email,omitempty"`
	Platform             string     `json:"platform"`
	Type                 string     `json:"type"`
	Status               string     `json:"status"`
	Schedulable          bool       `json:"schedulable"`
	PlanType             string     `json:"plan_type,omitempty"`
	LastUsedAt           *time.Time `json:"last_used_at"`
	RateLimitedAt        *time.Time `json:"rate_limited_at"`
	RateLimitResetAt     *time.Time `json:"rate_limit_reset_at"`
	TemporaryUnavailable *time.Time `json:"temp_unschedulable_until"`
	CreatedAt            *time.Time `json:"created_at,omitempty"`
	UpdatedAt            *time.Time `json:"updated_at,omitempty"`
}

// UsageWindow is one provider-reported usage window. Utilization is a percent,
// as returned by Sub2API (for example, 42 means 42%).
type UsageWindow struct {
	Utilization      float64    `json:"utilization"`
	ResetsAt         *time.Time `json:"resets_at"`
	RemainingSeconds int        `json:"remaining_seconds"`
}

// AccountUsage contains only the windows displayed by the portal. A nil
// window means the provider did not supply it; it must not be rendered as zero.
type AccountUsage struct {
	Source    string       `json:"source,omitempty"`
	UpdatedAt *time.Time   `json:"updated_at,omitempty"`
	FiveHour  *UsageWindow `json:"five_hour"`
	SevenDay  *UsageWindow `json:"seven_day"`
	ErrorCode string       `json:"error_code,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// BatchUsageResult preserves partial success: Errors is keyed by upstream
// account ID and does not invalidate successful Usage entries.
type BatchUsageResult struct {
	Usage  map[int64]AccountUsage `json:"usage"`
	Errors map[int64]string       `json:"errors"`
}
