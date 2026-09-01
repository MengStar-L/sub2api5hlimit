package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/MengStar-L/sub2api5hlimit/internal/store"
)

type announcementRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func announcementID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "无效的公告 ID")
		return 0, false
	}
	return id, true
}

func writeAnnouncementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAnnouncementTitle):
		writeError(w, http.StatusBadRequest, "INVALID_TITLE", "公告标题需为 1-120 个字符")
	case errors.Is(err, store.ErrAnnouncementBody):
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "公告正文需为 1-4000 个字符")
	default:
		writeStoreError(w, err)
	}
}

// GET /api/me/announcements —— 用户可见的公告列表，附带是否已选择不再弹出
func (s *Server) myAnnouncements(w http.ResponseWriter, r *http.Request) {
	userID := currentSession(r).User.ID
	items, err := s.store.AnnouncementsForUser(r.Context(), userID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// 首次登录自动弹出的目标：最新一条尚未被本人关闭的公告
	var popup *store.Announcement
	for i := range items {
		if !items[i].Dismissed {
			popup = &items[i]
			break
		}
	}
	writeData(w, http.StatusOK, map[string]any{
		"announcements": items,
		"popup":         popup,
		"unread_count":  countUndismissed(items),
	})
}

func countUndismissed(items []store.Announcement) int {
	total := 0
	for _, item := range items {
		if !item.Dismissed {
			total++
		}
	}
	return total
}

// POST /api/me/announcements/{id}/dismiss —— 「已了解，不再弹出此公告」
func (s *Server) dismissAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := announcementID(w, r)
	if !ok {
		return
	}
	if err := s.store.DismissAnnouncement(r.Context(), id, currentSession(r).User.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"dismissed": true})
}

// GET /api/admin/announcements
func (s *Server) listAnnouncements(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Announcements(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"announcements": items})
}

// POST /api/admin/announcements
func (s *Server) createAnnouncement(w http.ResponseWriter, r *http.Request) {
	var request announcementRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := s.store.CreateAnnouncement(r.Context(), request.Title, request.Body, currentSession(r).User.ID)
	if err != nil {
		writeAnnouncementError(w, err)
		return
	}
	writeData(w, http.StatusCreated, item)
}

// PUT /api/admin/announcements/{id}
func (s *Server) updateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := announcementID(w, r)
	if !ok {
		return
	}
	var request announcementRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	item, err := s.store.UpdateAnnouncement(r.Context(), id, request.Title, request.Body)
	if err != nil {
		writeAnnouncementError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

// DELETE /api/admin/announcements/{id}
func (s *Server) deleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, ok := announcementID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAnnouncement(r.Context(), id); err != nil {
		writeStoreError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}
