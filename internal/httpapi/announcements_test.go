package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

type announcementBody struct {
	Data store.Announcement `json:"data"`
}

type announcementFeed struct {
	Data struct {
		Announcements []store.Announcement `json:"announcements"`
		Popup         *store.Announcement  `json:"popup"`
		UnreadCount   int                  `json:"unread_count"`
	} `json:"data"`
}

// 一位管理员 + 一位普通用户，用于验证公告的可见性与「不再弹出」的作用域。
func announcementFixture(t *testing.T) (*httpFixture, *http.Client, string, *http.Client, string) {
	t.Helper()
	fixture := newHTTPFixture(t)
	adminClient, adminCSRF := initializeAndLoginAdmin(t, fixture)

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/users", map[string]any{
		"username": "alice", "display_name": "Alice", "password": testUserPassword, "upstream_key_id": 1001,
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusCreated {
		t.Fatalf("create user = %d, body = %s", status, body)
	}

	userClient := newCookieClient(t)
	status, body, _ = fixture.request(t, userClient, http.MethodPost, "/api/auth/login", map[string]any{
		"username": "alice", "password": testUserPassword,
	}, fixture.server.URL, "")
	if status != http.StatusOK {
		t.Fatalf("user login = %d, body = %s", status, body)
	}
	return fixture, adminClient, adminCSRF, userClient, loginCSRF(t, body)
}

func TestAnnouncementPublishReadAndDismiss(t *testing.T) {
	fixture, adminClient, adminCSRF, userClient, userCSRF := announcementFixture(t)

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "首条公告", "body": "初始说明。",
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusCreated {
		t.Fatalf("create announcement = %d, body = %s", status, body)
	}
	var first announcementBody
	decodeResponse(t, body, &first)
	if first.Data.ID == 0 || first.Data.PublishedAt == 0 {
		t.Fatalf("created announcement = %#v, want an id and a publish time", first.Data)
	}

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "维护窗口", "body": "周日 02:00 起同步暂停。",
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusCreated {
		t.Fatalf("create second announcement = %d, body = %s", status, body)
	}
	var second announcementBody
	decodeResponse(t, body, &second)

	// 用户首次读取：两条都在，弹窗指向最新一条
	status, body, _ = fixture.request(t, userClient, http.MethodGet, "/api/me/announcements", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("user feed = %d, body = %s", status, body)
	}
	var feed announcementFeed
	decodeResponse(t, body, &feed)
	if len(feed.Data.Announcements) != 2 {
		t.Fatalf("feed length = %d, want 2; body = %s", len(feed.Data.Announcements), body)
	}
	if feed.Data.Announcements[0].ID != second.Data.ID {
		t.Fatalf("feed order = %d first, want newest %d", feed.Data.Announcements[0].ID, second.Data.ID)
	}
	if feed.Data.Popup == nil || feed.Data.Popup.ID != second.Data.ID {
		t.Fatalf("popup = %#v, want newest announcement %d", feed.Data.Popup, second.Data.ID)
	}
	if feed.Data.UnreadCount != 2 {
		t.Fatalf("unread count = %d, want 2", feed.Data.UnreadCount)
	}

	// 「已了解，不再弹出此公告」只作用于这一条
	status, body, _ = fixture.request(t, userClient, http.MethodPost,
		"/api/me/announcements/"+itoa(second.Data.ID)+"/dismiss", map[string]any{}, fixture.server.URL, userCSRF)
	if status != http.StatusOK {
		t.Fatalf("dismiss = %d, body = %s", status, body)
	}

	status, body, _ = fixture.request(t, userClient, http.MethodGet, "/api/me/announcements", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("user feed after dismiss = %d, body = %s", status, body)
	}
	feed = announcementFeed{}
	decodeResponse(t, body, &feed)
	if feed.Data.Popup == nil || feed.Data.Popup.ID != first.Data.ID {
		t.Fatalf("popup after dismiss = %#v, want the older announcement %d", feed.Data.Popup, first.Data.ID)
	}
	if feed.Data.UnreadCount != 1 {
		t.Fatalf("unread count after dismiss = %d, want 1", feed.Data.UnreadCount)
	}
	for _, item := range feed.Data.Announcements {
		want := item.ID == second.Data.ID
		if item.Dismissed != want {
			t.Fatalf("announcement %d dismissed = %v, want %v", item.ID, item.Dismissed, want)
		}
	}

	// 重复确认是幂等的
	status, body, _ = fixture.request(t, userClient, http.MethodPost,
		"/api/me/announcements/"+itoa(second.Data.ID)+"/dismiss", map[string]any{}, fixture.server.URL, userCSRF)
	if status != http.StatusOK {
		t.Fatalf("repeat dismiss = %d, body = %s", status, body)
	}

	// 另一位用户不受影响
	status, body, _ = fixture.request(t, adminClient, http.MethodGet, "/api/me/announcements", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("admin feed = %d, body = %s", status, body)
	}
	var adminFeed announcementFeed
	decodeResponse(t, body, &adminFeed)
	if adminFeed.Data.UnreadCount != 2 {
		t.Fatalf("admin unread count = %d, want 2 (dismissals are per user)", adminFeed.Data.UnreadCount)
	}
}

func TestAnnouncementValidationAndRBAC(t *testing.T) {
	fixture, adminClient, adminCSRF, userClient, userCSRF := announcementFixture(t)

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "   ", "body": "正文",
	}, fixture.server.URL, adminCSRF)
	assertAPIError(t, status, body, http.StatusBadRequest, "INVALID_TITLE")

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "标题", "body": "",
	}, fixture.server.URL, adminCSRF)
	assertAPIError(t, status, body, http.StatusBadRequest, "INVALID_BODY")

	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": strings.Repeat("标", 121), "body": "正文",
	}, fixture.server.URL, adminCSRF)
	assertAPIError(t, status, body, http.StatusBadRequest, "INVALID_TITLE")

	// 普通用户不能发布、修改或删除
	status, body, _ = fixture.request(t, userClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "越权", "body": "越权",
	}, fixture.server.URL, userCSRF)
	assertAPIError(t, status, body, http.StatusForbidden, "FORBIDDEN")

	status, body, _ = fixture.request(t, userClient, http.MethodGet, "/api/admin/announcements", nil, "", "")
	assertAPIError(t, status, body, http.StatusForbidden, "FORBIDDEN")

	// 缺少 CSRF 头的写请求要被拒
	status, body, _ = fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "无 CSRF", "body": "无 CSRF",
	}, fixture.server.URL, "")
	assertAPIError(t, status, body, http.StatusForbidden, "CSRF_REJECTED")

	status, body, _ = fixture.request(t, adminClient, http.MethodPut, "/api/admin/announcements/9999", map[string]any{
		"title": "不存在", "body": "不存在",
	}, fixture.server.URL, adminCSRF)
	assertAPIError(t, status, body, http.StatusNotFound, "NOT_FOUND")

	status, body, _ = fixture.request(t, adminClient, http.MethodDelete, "/api/admin/announcements/abc", nil, fixture.server.URL, adminCSRF)
	assertAPIError(t, status, body, http.StatusBadRequest, "INVALID_ID")

	status, body, _ = fixture.request(t, userClient, http.MethodPost, "/api/me/announcements/9999/dismiss",
		map[string]any{}, fixture.server.URL, userCSRF)
	assertAPIError(t, status, body, http.StatusNotFound, "NOT_FOUND")
}

type fakeCodexProvider struct {
	state store.CodexForecastState
	err   error
}

func (f fakeCodexProvider) View(context.Context) (store.CodexForecastState, error) {
	return f.state, f.err
}

func TestCodexForecastEndpoint(t *testing.T) {
	fixture, adminClient, _, userClient, _ := announcementFixture(t)

	// 未挂载数据源时明确降级，而不是返回空对象假装有数据
	status, body, _ := fixture.request(t, userClient, http.MethodGet, "/api/codex-forecast", nil, "", "")
	assertAPIError(t, status, body, http.StatusServiceUnavailable, "CODEX_FORECAST_DISABLED")

	fetched := int64(1_756_000_000)
	fixture.api.SetCodexForecastProvider(fakeCodexProvider{state: store.CodexForecastState{
		Score: 63, HorizonHours: 24, ForecastState: "possible",
		Breakdown:       []store.CodexForecastBreakdown{{Label: "社区报告增多", Points: 12}},
		SourceFetchedAt: &fetched, LastSuccessAt: &fetched,
	}})

	type forecastResponse struct {
		Data struct {
			Forecast   store.CodexForecastState `json:"forecast"`
			SourceURL  string                   `json:"source_url"`
			Disclaimer string                   `json:"disclaimer"`
		} `json:"data"`
	}

	// 用户与管理员都能读
	for name, client := range map[string]*http.Client{"user": userClient, "admin": adminClient} {
		status, body, _ = fixture.request(t, client, http.MethodGet, "/api/codex-forecast", nil, "", "")
		if status != http.StatusOK {
			t.Fatalf("%s forecast = %d, body = %s", name, status, body)
		}
		var response forecastResponse
		decodeResponse(t, body, &response)
		if response.Data.Forecast.Score != 63 {
			t.Fatalf("%s score = %d, want 63", name, response.Data.Forecast.Score)
		}
		if response.Data.SourceURL == "" || !strings.Contains(response.Data.SourceURL, "willcodexquotareset.com") {
			t.Fatalf("%s source url = %q", name, response.Data.SourceURL)
		}
		if !strings.Contains(response.Data.Disclaimer, "预测") {
			t.Fatalf("%s disclaimer = %q, want it to flag the value as a prediction", name, response.Data.Disclaimer)
		}
		if response.Data.Forecast.SourceFetchedAt == nil || *response.Data.Forecast.SourceFetchedAt != fetched {
			t.Fatalf("%s fetch time = %v, want %d", name, response.Data.Forecast.SourceFetchedAt, fetched)
		}
	}

	// 匿名请求仍需登录
	status, body, _ = fixture.request(t, newCookieClient(t), http.MethodGet, "/api/codex-forecast", nil, "", "")
	assertAPIError(t, status, body, http.StatusUnauthorized, "UNAUTHENTICATED")
}

func TestAnnouncementUpdateAndDelete(t *testing.T) {
	fixture, adminClient, adminCSRF, userClient, _ := announcementFixture(t)

	status, body, _ := fixture.request(t, adminClient, http.MethodPost, "/api/admin/announcements", map[string]any{
		"title": "原标题", "body": "原正文",
	}, fixture.server.URL, adminCSRF)
	if status != http.StatusCreated {
		t.Fatalf("create = %d, body = %s", status, body)
	}
	var created announcementBody
	decodeResponse(t, body, &created)

	status, body, _ = fixture.request(t, adminClient, http.MethodPut, "/api/admin/announcements/"+itoa(created.Data.ID),
		map[string]any{"title": "新标题", "body": "新正文"}, fixture.server.URL, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("update = %d, body = %s", status, body)
	}
	var updated announcementBody
	decodeResponse(t, body, &updated)
	if updated.Data.Title != "新标题" || updated.Data.Body != "新正文" {
		t.Fatalf("updated announcement = %#v", updated.Data)
	}
	if updated.Data.PublishedAt != created.Data.PublishedAt {
		t.Fatalf("publish time changed on edit: %d -> %d", created.Data.PublishedAt, updated.Data.PublishedAt)
	}

	status, body, _ = fixture.request(t, adminClient, http.MethodDelete, "/api/admin/announcements/"+itoa(created.Data.ID),
		nil, fixture.server.URL, adminCSRF)
	if status != http.StatusOK {
		t.Fatalf("delete = %d, body = %s", status, body)
	}

	status, body, _ = fixture.request(t, userClient, http.MethodGet, "/api/me/announcements", nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("feed after delete = %d, body = %s", status, body)
	}
	var feed announcementFeed
	decodeResponse(t, body, &feed)
	if len(feed.Data.Announcements) != 0 || feed.Data.Popup != nil {
		t.Fatalf("feed after delete = %#v, want empty", feed.Data)
	}
}
