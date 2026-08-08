PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS proxies (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    port                INTEGER NOT NULL UNIQUE,
    tls_domain          TEXT NOT NULL,
    ad_tag              TEXT NOT NULL DEFAULT '',
    secret              TEXT NOT NULL,
    api_token           TEXT NOT NULL,
    container_id        TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL,
    state_message       TEXT NOT NULL DEFAULT '',
    data_quota_bytes    INTEGER,
    expiration_rfc3339  TEXT,
    max_tcp_conns       INTEGER,
    max_unique_ips      INTEGER,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS admins (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    must_change_password INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
