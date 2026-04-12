-- +goose Up
CREATE TABLE IF NOT EXISTS users(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    home_dir TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS sites(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    domain TEXT NOT NULL UNIQUE,
    mode TEXT NOT NULL DEFAULT 'php',
    webroot TEXT NOT NULL,
    php_version TEXT NOT NULL DEFAULT '',
    enable_http3 INTEGER NOT NULL DEFAULT 1,
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at TEXT,
    tls_mode TEXT NOT NULL DEFAULT 'letsencrypt',
    tls_cert_path TEXT NOT NULL DEFAULT '',
    tls_key_path TEXT NOT NULL DEFAULT '',
    acme_webroot_override TEXT NOT NULL DEFAULT '',
    letsencrypt_email_override TEXT NOT NULL DEFAULT '',
    cert_issued_at TEXT,
    cert_expires_at TEXT,
    last_cert_error TEXT NOT NULL DEFAULT '',
    client_max_body_size TEXT NOT NULL DEFAULT '',
    php_time_read TEXT NOT NULL DEFAULT '',
    php_time_send TEXT NOT NULL DEFAULT '',
    last_render_hash TEXT NOT NULL DEFAULT '',
    last_applied_at TEXT,
    last_apply_status TEXT NOT NULL DEFAULT '',
    last_apply_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_sites_user_id ON sites(user_id);

CREATE TABLE IF NOT EXISTS proxy_targets(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id INTEGER NOT NULL,
    target TEXT NOT NULL,
    weight INTEGER NOT NULL DEFAULT 100,
    is_backup INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE(site_id, target),
    FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_proxy_targets_site_id ON proxy_targets(site_id);

CREATE TABLE IF NOT EXISTS apply_runs(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id INTEGER,
    action TEXT NOT NULL,
    status TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    FOREIGN KEY(site_id) REFERENCES sites(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS panel_users(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'admin',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_login_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS idx_panel_users_username ON panel_users(username);

-- +goose Down
-- intentionally empty
