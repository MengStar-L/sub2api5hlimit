package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrAnnouncementTitle = errors.New("announcement title must be 1-120 characters")
	ErrAnnouncementBody  = errors.New("announcement body must be 1-4000 characters")
)

const announcementColumns = `id, title, body, published_at, created_by_user_id, created_at, updated_at`

func scanAnnouncement(scanner interface{ Scan(...any) error }) (Announcement, error) {
	var out Announcement
	var createdBy sql.NullInt64
	err := scanner.Scan(&out.ID, &out.Title, &out.Body, &out.PublishedAt, &createdBy, &out.CreatedAt, &out.UpdatedAt)
	out.CreatedByUserID = intPtr(createdBy)
	return out, err
}

// 标题去空白后长度 1-120，正文 1-4000；与迁移里的 CHECK 保持一致
func normalizeAnnouncement(title, body string) (string, string, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimRight(body, " \t\r\n")
	if n := utf8.RuneCountInString(title); n < 1 || n > 120 {
		return "", "", ErrAnnouncementTitle
	}
	if n := utf8.RuneCountInString(body); n < 1 || n > 4000 {
		return "", "", ErrAnnouncementBody
	}
	return title, body, nil
}

func (s *Store) CreateAnnouncement(ctx context.Context, title, body string, actorID int64) (Announcement, error) {
	title, body, err := normalizeAnnouncement(title, body)
	if err != nil {
		return Announcement{}, err
	}
	now := time.Now().Unix()
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO announcements (title, body, published_at, created_by_user_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		title, body, now, actor, now, now)
	if err != nil {
		return Announcement{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Announcement{}, err
	}
	return s.Announcement(ctx, id)
}

func (s *Store) UpdateAnnouncement(ctx context.Context, id int64, title, body string) (Announcement, error) {
	title, body, err := normalizeAnnouncement(title, body)
	if err != nil {
		return Announcement{}, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE announcements SET title = ?, body = ?, updated_at = ? WHERE id = ?`,
		title, body, time.Now().Unix(), id)
	if err != nil {
		return Announcement{}, err
	}
	if affected, err := res.RowsAffected(); err != nil {
		return Announcement{}, err
	} else if affected == 0 {
		return Announcement{}, ErrNotFound
	}
	return s.Announcement(ctx, id)
}

func (s *Store) DeleteAnnouncement(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM announcements WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Announcement(ctx context.Context, id int64) (Announcement, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+announcementColumns+` FROM announcements WHERE id = ?`, id)
	out, err := scanAnnouncement(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Announcement{}, ErrNotFound
	}
	return out, err
}

func (s *Store) Announcements(ctx context.Context) ([]Announcement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+announcementColumns+` FROM announcements ORDER BY published_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Announcement, 0, 16)
	for rows.Next() {
		item, err := scanAnnouncement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AnnouncementsForUser 返回全部公告，并标注该用户是否已选择不再弹出。
func (s *Store) AnnouncementsForUser(ctx context.Context, userID int64) ([]Announcement, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.title, a.body, a.published_at, a.created_by_user_id, a.created_at, a.updated_at,
		        d.user_id IS NOT NULL AS dismissed
		 FROM announcements a
		 LEFT JOIN announcement_dismissals d ON d.announcement_id = a.id AND d.user_id = ?
		 ORDER BY a.published_at DESC, a.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Announcement, 0, 16)
	for rows.Next() {
		var item Announcement
		var createdBy sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Title, &item.Body, &item.PublishedAt, &createdBy,
			&item.CreatedAt, &item.UpdatedAt, &item.Dismissed); err != nil {
			return nil, err
		}
		item.CreatedByUserID = intPtr(createdBy)
		out = append(out, item)
	}
	return out, rows.Err()
}

// DismissAnnouncement 记录「已了解，不再弹出」；重复调用是幂等的。
func (s *Store) DismissAnnouncement(ctx context.Context, announcementID, userID int64) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO announcement_dismissals (announcement_id, user_id, dismissed_at)
		 SELECT id, ?, ? FROM announcements WHERE id = ?
		 ON CONFLICT (announcement_id, user_id) DO NOTHING`,
		userID, time.Now().Unix(), announcementID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	// 没插入可能是公告不存在，也可能是已存在记录；后者视为成功
	if affected == 0 {
		var exists int
		if err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM announcement_dismissals WHERE announcement_id = ? AND user_id = ?`,
			announcementID, userID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
	}
	return nil
}
