-- +goose Up
ALTER TABLE panel_users ADD COLUMN reseller_id INTEGER REFERENCES panel_users(id);
ALTER TABLE panel_users ADD COLUMN system_user TEXT NOT NULL DEFAULT '';
ALTER TABLE panel_users ADD COLUMN failed_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE panel_users ADD COLUMN locked_until TEXT;
ALTER TABLE panel_users ADD COLUMN created_by INTEGER REFERENCES panel_users(id);
ALTER TABLE panel_users ADD COLUMN owner_id INTEGER REFERENCES panel_users(id);

CREATE TABLE IF NOT EXISTS packages(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_by INTEGER REFERENCES panel_users(id) ON DELETE SET NULL,
    max_domains INTEGER NOT NULL DEFAULT 5,
    max_subdomains INTEGER NOT NULL DEFAULT 20,
    max_disk_mb INTEGER NOT NULL DEFAULT 1024,
    max_bandwidth_gb INTEGER NOT NULL DEFAULT -1,
    php_versions TEXT NOT NULL DEFAULT '[]',
    max_php_workers INTEGER NOT NULL DEFAULT 5,
    max_mysql_dbs INTEGER NOT NULL DEFAULT 1,
    max_mysql_users INTEGER NOT NULL DEFAULT 2,
    max_email_accts INTEGER NOT NULL DEFAULT 0,
    max_users INTEGER NOT NULL DEFAULT -1,
    cgroup_cpu_pct INTEGER NOT NULL DEFAULT -1,
    cgroup_mem_mb INTEGER NOT NULL DEFAULT -1,
    cgroup_io_mbps INTEGER NOT NULL DEFAULT -1,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS user_packages(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL UNIQUE REFERENCES panel_users(id) ON DELETE CASCADE,
    package_id INTEGER NOT NULL REFERENCES packages(id),
    assigned_by INTEGER REFERENCES panel_users(id) ON DELETE SET NULL,
    assigned_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX IF NOT EXISTS idx_packages_created_by ON packages(created_by);
CREATE INDEX IF NOT EXISTS idx_user_packages_package_id ON user_packages(package_id);
CREATE INDEX IF NOT EXISTS idx_panel_users_created_by ON panel_users(created_by);
CREATE INDEX IF NOT EXISTS idx_panel_users_owner_id ON panel_users(owner_id);
CREATE INDEX IF NOT EXISTS idx_panel_users_reseller_id ON panel_users(reseller_id);

INSERT OR IGNORE INTO packages(
    id, name, created_by,
    max_domains, max_subdomains, max_disk_mb, max_bandwidth_gb,
    php_versions, max_php_workers,
    max_mysql_dbs, max_mysql_users, max_email_accts, max_users,
    cgroup_cpu_pct, cgroup_mem_mb, cgroup_io_mbps
) VALUES (
    1, 'unlimited', NULL,
    -1, -1, -1, -1,
    '[]', -1,
    -1, -1, -1, -1,
    -1, -1, -1
);

-- +goose Down
-- intentionally empty
