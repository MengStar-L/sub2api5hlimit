CREATE TABLE IF NOT EXISTS announcements (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    title TEXT NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 120),
    body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    published_at INTEGER NOT NULL,
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS announcements_published_idx
    ON announcements (published_at DESC, id DESC);

-- 每位用户对每条公告的「不再弹出」记录；公告删除后一并清理
CREATE TABLE IF NOT EXISTS announcement_dismissals (
    announcement_id INTEGER NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    dismissed_at INTEGER NOT NULL,
    PRIMARY KEY (announcement_id, user_id)
);

CREATE INDEX IF NOT EXISTS announcement_dismissals_user_idx
    ON announcement_dismissals (user_id, announcement_id);

-- Codex 重置预测缓存：单行，由后端定时抓取第三方站点后写入
CREATE TABLE IF NOT EXISTS codex_forecast_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    score INTEGER NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
    breakdown TEXT NOT NULL DEFAULT '[]',
    horizon_hours INTEGER NOT NULL DEFAULT 0,
    days_since_reset INTEGER,
    hours_since_reset REAL,
    latest_reset_at INTEGER,
    reset_announced INTEGER NOT NULL DEFAULT 0 CHECK (reset_announced IN (0, 1)),
    forecast_state TEXT NOT NULL DEFAULT '',
    evidence_tier TEXT NOT NULL DEFAULT '',
    model_version TEXT NOT NULL DEFAULT '',
    source_fetched_at INTEGER,
    next_refresh_at INTEGER,
    checked_at INTEGER,
    last_success_at INTEGER,
    last_error_code TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO codex_forecast_state (id, updated_at) VALUES (1, unixepoch());
