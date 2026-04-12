-- +goose Up
ALTER TABLE panel_users ADD COLUMN reseller_id BIGINT NULL;
ALTER TABLE panel_users ADD COLUMN system_user VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE panel_users ADD COLUMN failed_attempts INT NOT NULL DEFAULT 0;
ALTER TABLE panel_users ADD COLUMN locked_until VARCHAR(64) NULL;
ALTER TABLE panel_users ADD COLUMN created_by BIGINT NULL;
ALTER TABLE panel_users ADD COLUMN owner_id BIGINT NULL;
ALTER TABLE panel_users ADD INDEX idx_panel_users_created_by(created_by);
ALTER TABLE panel_users ADD INDEX idx_panel_users_owner_id(owner_id);
ALTER TABLE panel_users ADD INDEX idx_panel_users_reseller_id(reseller_id);
ALTER TABLE panel_users ADD CONSTRAINT fk_panel_users_reseller FOREIGN KEY (reseller_id) REFERENCES panel_users(id) ON DELETE SET NULL;
ALTER TABLE panel_users ADD CONSTRAINT fk_panel_users_created_by FOREIGN KEY (created_by) REFERENCES panel_users(id) ON DELETE SET NULL;
ALTER TABLE panel_users ADD CONSTRAINT fk_panel_users_owner_id FOREIGN KEY (owner_id) REFERENCES panel_users(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS packages (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    name VARCHAR(255) NOT NULL UNIQUE,
    created_by BIGINT NULL,
    max_domains INT NOT NULL DEFAULT 5,
    max_subdomains INT NOT NULL DEFAULT 20,
    max_disk_mb INT NOT NULL DEFAULT 1024,
    max_bandwidth_gb INT NOT NULL DEFAULT -1,
    php_versions TEXT NOT NULL,
    max_php_workers INT NOT NULL DEFAULT 5,
    max_mysql_dbs INT NOT NULL DEFAULT 1,
    max_mysql_users INT NOT NULL DEFAULT 2,
    max_email_accts INT NOT NULL DEFAULT 0,
    max_users INT NOT NULL DEFAULT -1,
    cgroup_cpu_pct INT NOT NULL DEFAULT -1,
    cgroup_mem_mb INT NOT NULL DEFAULT -1,
    cgroup_io_mbps INT NOT NULL DEFAULT -1,
    created_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    updated_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    INDEX idx_packages_created_by(created_by),
    CONSTRAINT fk_packages_created_by FOREIGN KEY (created_by) REFERENCES panel_users(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS user_packages (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE,
    package_id BIGINT NOT NULL,
    assigned_by BIGINT NULL,
    assigned_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    INDEX idx_user_packages_package_id(package_id),
    CONSTRAINT fk_user_packages_user FOREIGN KEY (user_id) REFERENCES panel_users(id) ON DELETE CASCADE,
    CONSTRAINT fk_user_packages_package FOREIGN KEY (package_id) REFERENCES packages(id),
    CONSTRAINT fk_user_packages_assigned_by FOREIGN KEY (assigned_by) REFERENCES panel_users(id) ON DELETE SET NULL
);

INSERT IGNORE INTO packages (
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
