package store

import "time"

const (
	RoleAdmin = "admin"
	RoleUser  = "user"

	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusDeleted  = "deleted"

	QuotaResetJobQueued    = "queued"
	QuotaResetJobRunning   = "running"
	QuotaResetJobCompleted = "completed"

	QuotaResetItemPending   = "pending"
	QuotaResetItemRunning   = "running"
	QuotaResetItemSucceeded = "succeeded"
	QuotaResetItemFailed    = "failed"
	QuotaResetItemUnknown   = "unknown"
	QuotaResetItemSkipped   = "skipped"
)

type User struct {
	ID           int64       `json:"id"`
	Username     string      `json:"username"`
	DisplayName  string      `json:"display_name"`
	PasswordHash string      `json:"-"`
	Role         string      `json:"role"`
	Status       string      `json:"status"`
	LastLoginAt  *int64      `json:"last_login_at,omitempty"`
	DeletedAt    *int64      `json:"deleted_at,omitempty"`
	CreatedAt    int64       `json:"created_at"`
	UpdatedAt    int64       `json:"updated_at"`
	Binding      *KeyBinding `json:"binding,omitempty"`
}

type Settings struct {
	ConnectionUUID    string `json:"connection_uuid"`
	BaseURL           string `json:"base_url"`
	AdminAPIKey       string `json:"-"`
	OwnerUserID       int64  `json:"owner_user_id"`
	OwnerLabel        string `json:"owner_label"`
	AllowPrivateHTTP  bool   `json:"allow_private_http"`
	LastKeySyncAt     *int64 `json:"last_key_sync_at,omitempty"`
	LastAccountSyncAt *int64 `json:"last_account_sync_at,omitempty"`
	LastUsageSyncAt   *int64 `json:"last_usage_sync_at,omitempty"`
	UpdatedAt         int64  `json:"updated_at"`
}

type Session struct {
	TokenHash         string
	CSRFHash          string
	User              User
	CreatedAt         int64
	LastSeenAt        int64
	IdleExpiresAt     int64
	AbsoluteExpiresAt int64
}

type KeyBinding struct {
	UserID          int64   `json:"user_id"`
	UpstreamKeyID   int64   `json:"upstream_key_id"`
	KeyName         string  `json:"key_name"`
	KeyMask         string  `json:"masked_key"`
	UpstreamStatus  string  `json:"upstream_status"`
	BindingState    string  `json:"binding_state"`
	RateLimit5h     float64 `json:"rate_limit_5h"`
	Usage5h         float64 `json:"usage_5h"`
	Reset5hAt       *int64  `json:"reset_5h_at,omitempty"`
	RateLimit7d     float64 `json:"rate_limit_7d"`
	Usage7d         float64 `json:"usage_7d"`
	Reset7dAt       *int64  `json:"reset_7d_at,omitempty"`
	SourceUpdatedAt *int64  `json:"source_updated_at,omitempty"`
	LastSuccessAt   *int64  `json:"last_success_at,omitempty"`
	LastErrorCode   string  `json:"last_error_code,omitempty"`
	CreatedAt       int64   `json:"created_at"`
	UpdatedAt       int64   `json:"updated_at"`
}

type KeySnapshot struct {
	UpstreamKeyID   int64   `json:"id"`
	Name            string  `json:"name"`
	Mask            string  `json:"masked_key"`
	Status          string  `json:"status"`
	RateLimit5h     float64 `json:"rate_limit_5h"`
	Usage5h         float64 `json:"usage_5h"`
	Reset5hAt       *int64  `json:"reset_5h_at,omitempty"`
	RateLimit7d     float64 `json:"rate_limit_7d"`
	Usage7d         float64 `json:"usage_7d"`
	Reset7dAt       *int64  `json:"reset_7d_at,omitempty"`
	SourceUpdatedAt *int64  `json:"source_updated_at,omitempty"`
}

func (k KeySnapshot) Compliant() bool { return k.RateLimit5h > 0 && k.RateLimit7d > 0 }

type PoolAccount struct {
	UpstreamAccountID int64    `json:"upstream_account_id"`
	PublicAlias       string   `json:"public_alias"`
	Published         bool     `json:"published"`
	Missing           bool     `json:"missing"`
	Name              string   `json:"name"`
	Email             string   `json:"email"`
	Platform          string   `json:"platform"`
	AccountType       string   `json:"account_type"`
	PlanType          string   `json:"plan_type"`
	UpstreamStatus    string   `json:"upstream_status"`
	Schedulable       bool     `json:"schedulable"`
	NormalizedStatus  string   `json:"normalized_status"`
	FiveSupported     bool     `json:"five_supported"`
	FiveUtilization   *float64 `json:"five_utilization,omitempty"`
	FiveResetAt       *int64   `json:"five_reset_at,omitempty"`
	SevenSupported    bool     `json:"seven_supported"`
	SevenUtilization  *float64 `json:"seven_utilization,omitempty"`
	SevenResetAt      *int64   `json:"seven_reset_at,omitempty"`
	UsageSource       string   `json:"usage_source"`
	SourceUpdatedAt   *int64   `json:"source_updated_at,omitempty"`
	LastSuccessAt     *int64   `json:"last_success_at,omitempty"`
	LastErrorCode     string   `json:"last_error_code,omitempty"`
	LastSeenAt        int64    `json:"last_seen_at"`
	CreatedAt         int64    `json:"created_at"`
	UpdatedAt         int64    `json:"updated_at"`
}

type PoolInventory struct {
	UpstreamAccountID int64
	Name              string
	Email             string
	Platform          string
	AccountType       string
	PlanType          string
	Status            string
	Schedulable       bool
}

type PoolUsage struct {
	UpstreamAccountID int64
	FiveSupported     bool
	FiveUtilization   *float64
	FiveResetAt       *int64
	SevenSupported    bool
	SevenUtilization  *float64
	SevenResetAt      *int64
	Source            string
	SourceUpdatedAt   *int64
	ErrorCode         string
}

type SetupStatus struct {
	Complete  bool  `json:"complete"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

type QuotaResetJob struct {
	ID                int64               `json:"id"`
	Status            string              `json:"status"`
	TotalCount        int                 `json:"total_count"`
	PendingCount      int                 `json:"pending_count"`
	RunningCount      int                 `json:"running_count"`
	SucceededCount    int                 `json:"succeeded_count"`
	FailedCount       int                 `json:"failed_count"`
	UnknownCount      int                 `json:"unknown_count"`
	SkippedCount      int                 `json:"skipped_count"`
	RequestedByUserID *int64              `json:"requested_by_user_id,omitempty"`
	CreatedAt         int64               `json:"created_at"`
	StartedAt         *int64              `json:"started_at,omitempty"`
	CompletedAt       *int64              `json:"completed_at,omitempty"`
	Items             []QuotaResetJobItem `json:"items,omitempty"`
}

type QuotaResetJobItem struct {
	ID            int64  `json:"id"`
	JobID         int64  `json:"job_id"`
	UserID        int64  `json:"user_id"`
	Username      string `json:"username"`
	DisplayName   string `json:"display_name"`
	UserStatus    string `json:"user_status"`
	UpstreamKeyID *int64 `json:"upstream_key_id,omitempty"`
	KeyMask       string `json:"key_mask"`
	Status        string `json:"status"`
	ErrorCode     string `json:"error_code,omitempty"`
	CreatedAt     int64  `json:"created_at"`
	UpdatedAt     int64  `json:"updated_at"`
	StartedAt     *int64 `json:"started_at,omitempty"`
	CompletedAt   *int64 `json:"completed_at,omitempty"`
}

func UnixPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	value := t.Unix()
	return &value
}
