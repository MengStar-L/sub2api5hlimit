CREATE TABLE IF NOT EXISTS quota_reset_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'completed')),
    requested_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    total_count INTEGER NOT NULL DEFAULT 0 CHECK (total_count >= 0),
    pending_count INTEGER NOT NULL DEFAULT 0 CHECK (pending_count >= 0),
    running_count INTEGER NOT NULL DEFAULT 0 CHECK (running_count >= 0),
    succeeded_count INTEGER NOT NULL DEFAULT 0 CHECK (succeeded_count >= 0),
    failed_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    unknown_count INTEGER NOT NULL DEFAULT 0 CHECK (unknown_count >= 0),
    skipped_count INTEGER NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    completed_at INTEGER,
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS quota_reset_jobs_one_active_idx
    ON quota_reset_jobs ((1))
    WHERE status IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS quota_reset_jobs_created_idx
    ON quota_reset_jobs (created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS quota_reset_job_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id INTEGER NOT NULL REFERENCES quota_reset_jobs(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL,
    username TEXT NOT NULL,
    display_name TEXT NOT NULL,
    user_status TEXT NOT NULL CHECK (user_status IN ('active', 'disabled')),
    upstream_key_id INTEGER,
    key_mask TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'unknown', 'skipped')),
    error_code TEXT NOT NULL DEFAULT '' CHECK (
        error_code = '' OR (
            length(error_code) <= 64
            AND error_code GLOB '[A-Z]*'
            AND error_code NOT GLOB '*[^A-Z0-9_]*'
        )
    ),
    created_at INTEGER NOT NULL,
    started_at INTEGER,
    completed_at INTEGER,
    updated_at INTEGER NOT NULL,
    UNIQUE (job_id, user_id)
);
CREATE INDEX IF NOT EXISTS quota_reset_job_items_job_idx
    ON quota_reset_job_items (job_id, id);
CREATE INDEX IF NOT EXISTS quota_reset_job_items_status_idx
    ON quota_reset_job_items (job_id, status, id);
