CREATE TABLE IF NOT EXISTS app_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    setup_complete INTEGER NOT NULL DEFAULT 0,
    setup_token_hash TEXT,
    setup_token_expires_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
INSERT OR IGNORE INTO app_meta (id, created_at, updated_at) VALUES (1, unixepoch(), unixepoch());

CREATE TABLE IF NOT EXISTS settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    connection_uuid TEXT NOT NULL,
    base_url TEXT NOT NULL,
    admin_api_key_cipher TEXT NOT NULL,
    owner_user_id INTEGER NOT NULL,
    owner_label TEXT NOT NULL DEFAULT '',
    allow_private_http INTEGER NOT NULL DEFAULT 0,
    last_key_sync_at INTEGER,
    last_account_sync_at INTEGER,
    last_usage_sync_at INTEGER,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL COLLATE NOCASE UNIQUE,
    display_name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('admin', 'user')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    last_login_at INTEGER,
    deleted_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    csrf_hash TEXT NOT NULL,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    idle_expires_at INTEGER NOT NULL,
    absolute_expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);
CREATE INDEX IF NOT EXISTS sessions_expiry_idx ON sessions (absolute_expires_at);

CREATE TABLE IF NOT EXISTS key_bindings (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    upstream_key_id INTEGER NOT NULL UNIQUE,
    key_name TEXT NOT NULL DEFAULT '',
    key_mask TEXT NOT NULL DEFAULT '',
    upstream_status TEXT NOT NULL DEFAULT '',
    binding_state TEXT NOT NULL DEFAULT 'pending',
    rate_limit_5h REAL NOT NULL DEFAULT 0,
    usage_5h REAL NOT NULL DEFAULT 0,
    reset_5h_at INTEGER,
    rate_limit_7d REAL NOT NULL DEFAULT 0,
    usage_7d REAL NOT NULL DEFAULT 0,
    reset_7d_at INTEGER,
    source_updated_at INTEGER,
    last_success_at INTEGER,
    last_error_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS pool_accounts (
    upstream_account_id INTEGER PRIMARY KEY,
    public_alias TEXT NOT NULL UNIQUE,
    published INTEGER NOT NULL DEFAULT 0,
    missing INTEGER NOT NULL DEFAULT 0,
    name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    platform TEXT NOT NULL DEFAULT '',
    account_type TEXT NOT NULL DEFAULT '',
    plan_type TEXT NOT NULL DEFAULT '',
    upstream_status TEXT NOT NULL DEFAULT '',
    schedulable INTEGER NOT NULL DEFAULT 0,
    normalized_status TEXT NOT NULL DEFAULT 'unavailable',
    five_supported INTEGER NOT NULL DEFAULT 0,
    five_utilization REAL,
    five_reset_at INTEGER,
    seven_supported INTEGER NOT NULL DEFAULT 0,
    seven_utilization REAL,
    seven_reset_at INTEGER,
    usage_source TEXT NOT NULL DEFAULT '',
    source_updated_at INTEGER,
    last_success_at INTEGER,
    last_error_code TEXT NOT NULL DEFAULT '',
    last_seen_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS pool_accounts_published_idx ON pool_accounts (published, normalized_status);

CREATE TABLE IF NOT EXISTS audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id TEXT NOT NULL DEFAULT '',
    metadata_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_events_created_idx ON audit_events (created_at DESC);
