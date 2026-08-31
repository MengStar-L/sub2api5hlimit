package sub2api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrRedirectBlocked  = errors.New("sub2api redirects are disabled")
	ErrResponseTooLarge = errors.New("sub2api response exceeds the configured size limit")
)

// SchemaError reports a response that did not match Sub2API's documented
// code/message/data envelope. It never includes the raw response body.
type SchemaError struct {
	Detail string
}

func (e *SchemaError) Error() string {
	return "invalid sub2api response schema: " + e.Detail
}

// UpstreamError represents a valid error response without retaining its body.
type UpstreamError struct {
	StatusCode int
	Code       int
	Message    string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("sub2api request failed (http=%d code=%d): %s", e.StatusCode, e.Code, e.Message)
}

// IncompatibleVersionError is returned by CheckVersion when Sub2API is older
// than the supported compatibility baseline.
type IncompatibleVersionError struct {
	Current Version
	Minimum Version
}

func (e *IncompatibleVersionError) Error() string {
	return fmt.Sprintf("sub2api %s is unsupported; version %s or newer is required", e.Current, e.Minimum)
}

type Client struct {
	baseURL          *url.URL
	apiKey           string
	httpClient       *http.Client
	maxResponseBytes int64
	pageSize         int
}

func NewClient(config Config) (*Client, error) {
	baseURL, err := validateBaseURL(config.BaseURL, config.AllowPrivateHTTP)
	if err != nil {
		return nil, err
	}

	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("sub2api admin API key is required")
	}

	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("sub2api timeout must be positive")
	}

	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	if maxResponseBytes < 1 || maxResponseBytes > maximumResponseBytes {
		return nil, fmt.Errorf("sub2api response size limit must be between 1 and %d bytes", maximumResponseBytes)
	}

	pageSize := config.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
	if pageSize < 1 || pageSize > maximumPageSize {
		return nil, fmt.Errorf("sub2api page size must be between 1 and %d", maximumPageSize)
	}

	client := &http.Client{}
	if config.HTTPClient != nil {
		copy := *config.HTTPClient
		client = &copy
	}
	client.Timeout = timeout
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return ErrRedirectBlocked
	}

	return &Client{
		baseURL:          baseURL,
		apiKey:           apiKey,
		httpClient:       client,
		maxResponseBytes: maxResponseBytes,
		pageSize:         pageSize,
	}, nil
}

func validateBaseURL(value string, allowPrivateHTTP bool) (*url.URL, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("invalid sub2api base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return nil, fmt.Errorf("sub2api base URL must be an absolute HTTP(S) origin")
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("sub2api base URL must contain a hostname")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("sub2api base URL must not contain credentials, a query, or a fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("sub2api base URL must not contain a path")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("sub2api base URL scheme must be https")
	}
	if parsed.Scheme == "http" {
		if !allowPrivateHTTP {
			return nil, fmt.Errorf("sub2api HTTP requires explicit private-network approval")
		}
		hostname := parsed.Hostname()
		address := net.ParseIP(hostname)
		isLocalhost := strings.EqualFold(hostname, "localhost")
		isPrivateLiteral := address != nil && (address.IsPrivate() || address.IsLoopback())
		if !isLocalhost && !isPrivateLiteral {
			return nil, fmt.Errorf("sub2api HTTP is allowed only for localhost or a private literal IP address")
		}
	}

	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func (c *Client) GetVersion(ctx context.Context) (Version, error) {
	var payload struct {
		Version string `json:"version"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/admin/system/version", nil, nil, &payload); err != nil {
		return Version{}, err
	}
	return ParseVersion(payload.Version)
}

func (c *Client) CheckVersion(ctx context.Context) (Version, error) {
	current, err := c.GetVersion(ctx)
	if err != nil {
		return Version{}, err
	}
	minimum, err := ParseVersion(MinimumVersion)
	if err != nil {
		return Version{}, err
	}
	if !current.AtLeast(minimum) {
		return current, &IncompatibleVersionError{Current: current, Minimum: minimum}
	}
	return current, nil
}

func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	return paginate[userDTO](ctx, c, "/api/v1/admin/users", func(raw *userDTO) (User, error) {
		if raw.ID <= 0 {
			return User{}, &SchemaError{Detail: "user id must be positive"}
		}
		return User{
			ID: raw.ID, Email: raw.Email, Username: raw.Username, Role: raw.Role,
			Status: raw.Status, CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt,
		}, nil
	})
}

func (c *Client) ListAPIKeys(ctx context.Context, userID int64) ([]APIKey, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("sub2api user ID must be positive")
	}
	endpoint := "/api/v1/admin/users/" + strconv.FormatInt(userID, 10) + "/api-keys"
	return paginate[apiKeyDTO](ctx, c, endpoint, func(raw *apiKeyDTO) (APIKey, error) {
		if raw.ID <= 0 || raw.UserID <= 0 {
			return APIKey{}, &SchemaError{Detail: "API key id and user_id must be positive"}
		}
		key := APIKey{
			ID: raw.ID, UserID: raw.UserID, MaskedKey: MaskAPIKey(raw.Key), Name: raw.Name,
			GroupID: raw.GroupID, Status: raw.Status, Quota: raw.Quota, QuotaUsed: raw.QuotaUsed,
			RateLimit5h: raw.RateLimit5h, RateLimit7d: raw.RateLimit7d,
			Usage5h: raw.Usage5h, Usage7d: raw.Usage7d,
			Window5hStart: raw.Window5hStart, Window7dStart: raw.Window7dStart,
			Reset5hAt: raw.Reset5hAt, Reset7dAt: raw.Reset7dAt,
			LastUsedAt: raw.LastUsedAt, ExpiresAt: raw.ExpiresAt,
			CreatedAt: raw.CreatedAt, UpdatedAt: raw.UpdatedAt, SourceUpdatedAt: raw.UpdatedAt,
		}
		// Drop sensitive strings from the page DTO before the next item is handled.
		raw.Key = ""
		raw.LastUsedIP = nil
		return key, nil
	})
}

func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	return paginate[accountDTO](ctx, c, "/api/v1/admin/accounts", func(raw *accountDTO) (Account, error) {
		if raw.ID <= 0 {
			return Account{}, &SchemaError{Detail: "account id must be positive"}
		}
		email := strings.TrimSpace(raw.Credentials.Email)
		if email == "" {
			email = strings.TrimSpace(raw.ParentEmail)
		}
		planType := strings.TrimSpace(raw.Credentials.PlanType)
		if planType == "" {
			planType = strings.TrimSpace(raw.ParentPlanType)
		}
		return Account{
			ID: raw.ID, Name: raw.Name, Email: email, Platform: raw.Platform, Type: raw.Type,
			Status: raw.Status, Schedulable: raw.Schedulable, PlanType: planType,
			LastUsedAt: raw.LastUsedAt, RateLimitedAt: raw.RateLimitedAt,
			RateLimitResetAt:     raw.RateLimitResetAt,
			TemporaryUnavailable: raw.TemporaryUnavailable,
			CreatedAt:            raw.CreatedAt, UpdatedAt: raw.UpdatedAt,
		}, nil
	})
}

func (c *Client) BatchAccountUsage(ctx context.Context, accountIDs []int64) (BatchUsageResult, error) {
	uniqueIDs := make([]int64, 0, len(accountIDs))
	seen := make(map[int64]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if accountID <= 0 {
			return BatchUsageResult{}, fmt.Errorf("sub2api account IDs must be positive")
		}
		if _, exists := seen[accountID]; exists {
			continue
		}
		seen[accountID] = struct{}{}
		uniqueIDs = append(uniqueIDs, accountID)
	}

	request := struct {
		AccountIDs []int64 `json:"account_ids"`
		Force      bool    `json:"force"`
	}{AccountIDs: uniqueIDs, Force: false}
	var raw batchUsageDTO
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/admin/accounts/usage/batch", nil, request, &raw); err != nil {
		return BatchUsageResult{}, err
	}
	if raw.Usage == nil || raw.Errors == nil {
		return BatchUsageResult{}, &SchemaError{Detail: "batch usage must contain usage and errors objects"}
	}

	result := BatchUsageResult{
		Usage:  make(map[int64]AccountUsage, len(raw.Usage)),
		Errors: make(map[int64]string, len(raw.Errors)),
	}
	for accountID, usage := range raw.Usage {
		if usage == nil {
			return BatchUsageResult{}, &SchemaError{Detail: "batch usage entry must be an object"}
		}
		result.Usage[accountID] = AccountUsage{
			Source: usage.Source, UpdatedAt: usage.UpdatedAt,
			FiveHour: safeUsageWindow(usage.FiveHour), SevenDay: safeUsageWindow(usage.SevenDay),
			ErrorCode: usage.ErrorCode, Error: c.redact(usage.Error),
		}
	}
	for accountID, message := range raw.Errors {
		result.Errors[accountID] = c.redact(message)
	}
	return result, nil
}

func safeUsageWindow(raw *usageWindowDTO) *UsageWindow {
	if raw == nil {
		return nil
	}
	return &UsageWindow{
		Utilization: raw.Utilization, ResetsAt: raw.ResetsAt,
		RemainingSeconds: raw.RemainingSeconds,
	}
}

type envelopeDTO struct {
	Code    *int            `json:"code"`
	Message *string         `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, requestBody, responseData any) error {
	target := *c.baseURL
	target.Path = endpoint
	target.RawQuery = query.Encode()

	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return fmt.Errorf("encode sub2api request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return fmt.Errorf("create sub2api request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-api-key", c.apiKey)
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return fmt.Errorf("sub2api request failed: %w", err)
	}
	defer response.Body.Close()

	if response.ContentLength > c.maxResponseBytes {
		return ErrResponseTooLarge
	}
	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read sub2api response: %w", err)
	}
	if int64(len(responseBody)) > c.maxResponseBytes {
		return ErrResponseTooLarge
	}
	defer clear(responseBody)

	var envelope envelopeDTO
	defer func() { clear(envelope.Data) }()
	decodeError := json.Unmarshal(responseBody, &envelope)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := http.StatusText(response.StatusCode)
		code := response.StatusCode
		if decodeError == nil {
			if envelope.Message != nil && strings.TrimSpace(*envelope.Message) != "" {
				message = c.redact(*envelope.Message)
			}
			if envelope.Code != nil {
				code = *envelope.Code
			}
		}
		return &UpstreamError{StatusCode: response.StatusCode, Code: code, Message: message}
	}
	if decodeError != nil {
		return &SchemaError{Detail: "response is not valid JSON"}
	}
	if envelope.Code == nil || envelope.Message == nil {
		return &SchemaError{Detail: "envelope must contain code and message"}
	}
	if *envelope.Code != 0 {
		return &UpstreamError{StatusCode: response.StatusCode, Code: *envelope.Code, Message: c.redact(*envelope.Message)}
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return &SchemaError{Detail: "envelope must contain non-null data"}
	}
	if err := json.Unmarshal(envelope.Data, responseData); err != nil {
		return &SchemaError{Detail: "data has an unexpected shape"}
	}
	return nil
}

func (c *Client) redact(value string) string {
	if c.apiKey == "" {
		return value
	}
	return strings.ReplaceAll(value, c.apiKey, "[redacted]")
}

type pageDTO struct {
	Items    json.RawMessage `json:"items"`
	Total    *int64          `json:"total"`
	Page     *int            `json:"page"`
	PageSize *int            `json:"page_size"`
	Pages    *int            `json:"pages"`
}

func paginate[Raw, Safe any](ctx context.Context, client *Client, endpoint string, sanitize func(*Raw) (Safe, error)) ([]Safe, error) {
	items := make([]Safe, 0)
	for requestedPage := 1; requestedPage <= 10000; requestedPage++ {
		query := url.Values{
			"page":      {strconv.Itoa(requestedPage)},
			"page_size": {strconv.Itoa(client.pageSize)},
		}
		var page pageDTO
		if err := client.doJSON(ctx, http.MethodGet, endpoint, query, nil, &page); err != nil {
			return nil, err
		}
		if page.Total == nil || page.Page == nil || page.PageSize == nil || page.Pages == nil {
			return nil, &SchemaError{Detail: "pagination metadata is incomplete"}
		}
		if *page.Total < 0 || *page.Page != requestedPage || *page.PageSize < 1 || *page.Pages < 1 {
			return nil, &SchemaError{Detail: "pagination metadata is invalid"}
		}
		trimmedItems := bytes.TrimSpace(page.Items)
		if len(trimmedItems) == 0 || bytes.Equal(trimmedItems, []byte("null")) {
			return nil, &SchemaError{Detail: "pagination items must be an array"}
		}
		var rawItems []Raw
		decodeError := json.Unmarshal(page.Items, &rawItems)
		clear(page.Items)
		page.Items = nil
		if decodeError != nil {
			return nil, &SchemaError{Detail: "pagination items must be an array of valid objects"}
		}
		if len(rawItems) > *page.PageSize {
			return nil, &SchemaError{Detail: "pagination page contains too many items"}
		}
		for i := range rawItems {
			safe, err := sanitize(&rawItems[i])
			if err != nil {
				return nil, err
			}
			items = append(items, safe)
			var zero Raw
			rawItems[i] = zero
		}
		if requestedPage >= *page.Pages {
			return items, nil
		}
	}
	return nil, &SchemaError{Detail: "pagination exceeded the safety limit"}
}

type userDTO struct {
	ID        int64      `json:"id"`
	Email     string     `json:"email"`
	Username  string     `json:"username"`
	Role      string     `json:"role"`
	Status    string     `json:"status"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type apiKeyDTO struct {
	ID            int64      `json:"id"`
	UserID        int64      `json:"user_id"`
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	GroupID       *int64     `json:"group_id"`
	Status        string     `json:"status"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	LastUsedIP    *string    `json:"last_used_ip"`
	Quota         float64    `json:"quota"`
	QuotaUsed     float64    `json:"quota_used"`
	ExpiresAt     *time.Time `json:"expires_at"`
	CreatedAt     *time.Time `json:"created_at"`
	UpdatedAt     *time.Time `json:"updated_at"`
	RateLimit5h   float64    `json:"rate_limit_5h"`
	RateLimit7d   float64    `json:"rate_limit_7d"`
	Usage5h       float64    `json:"usage_5h"`
	Usage7d       float64    `json:"usage_7d"`
	Window5hStart *time.Time `json:"window_5h_start"`
	Window7dStart *time.Time `json:"window_7d_start"`
	Reset5hAt     *time.Time `json:"reset_5h_at"`
	Reset7dAt     *time.Time `json:"reset_7d_at"`
}

type accountDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Schedulable bool   `json:"schedulable"`
	Credentials struct {
		Email    string `json:"email"`
		PlanType string `json:"plan_type"`
	} `json:"credentials"`
	ParentEmail          string     `json:"parent_email"`
	ParentPlanType       string     `json:"parent_plan_type"`
	LastUsedAt           *time.Time `json:"last_used_at"`
	RateLimitedAt        *time.Time `json:"rate_limited_at"`
	RateLimitResetAt     *time.Time `json:"rate_limit_reset_at"`
	TemporaryUnavailable *time.Time `json:"temp_unschedulable_until"`
	CreatedAt            *time.Time `json:"created_at"`
	UpdatedAt            *time.Time `json:"updated_at"`
}

type usageWindowDTO struct {
	Utilization      float64    `json:"utilization"`
	ResetsAt         *time.Time `json:"resets_at"`
	RemainingSeconds int        `json:"remaining_seconds"`
}

type accountUsageDTO struct {
	Source    string          `json:"source"`
	UpdatedAt *time.Time      `json:"updated_at"`
	FiveHour  *usageWindowDTO `json:"five_hour"`
	SevenDay  *usageWindowDTO `json:"seven_day"`
	ErrorCode string          `json:"error_code"`
	Error     string          `json:"error"`
}

type batchUsageDTO struct {
	Usage  map[int64]*accountUsageDTO `json:"usage"`
	Errors map[int64]string           `json:"errors"`
}
