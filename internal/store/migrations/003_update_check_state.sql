CREATE TABLE IF NOT EXISTS update_check_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    latest_version TEXT NOT NULL DEFAULT '',
    release_url TEXT NOT NULL DEFAULT '',
    published_at INTEGER,
    mode TEXT NOT NULL DEFAULT '',
    min_updater_version TEXT NOT NULL DEFAULT '',
    checked_at INTEGER,
    last_success_at INTEGER,
    last_error_code TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

INSERT OR IGNORE INTO update_check_state (id, updated_at) VALUES (1, unixepoch());
