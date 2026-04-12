-- +goose Up
CREATE TABLE IF NOT EXISTS users (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(255) NOT NULL UNIQUE,
    home_dir VARCHAR(1024) NOT NULL,
    created_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ'))
);

CREATE TABLE IF NOT EXISTS sites (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    user_id BIGINT NOT NULL,
    domain VARCHAR(255) NOT NULL UNIQUE,
    mode VARCHAR(16) NOT NULL DEFAULT 'php',
    webroot VARCHAR(1024) NOT NULL,
    php_version VARCHAR(32) NOT NULL DEFAULT '',
    enable_http3 TINYINT(1) NOT NULL DEFAULT 1,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    deleted_at VARCHAR(64) NULL,
    tls_mode VARCHAR(32) NOT NULL DEFAULT 'letsencrypt',
    tls_cert_path VARCHAR(1024) NOT NULL DEFAULT '',
    tls_key_path VARCHAR(1024) NOT NULL DEFAULT '',
    acme_webroot_override VARCHAR(1024) NOT NULL DEFAULT '',
    letsencrypt_email_override VARCHAR(255) NOT NULL DEFAULT '',
    cert_issued_at VARCHAR(64) NULL,
    cert_expires_at VARCHAR(64) NULL,
    last_cert_error TEXT NOT NULL,
    client_max_body_size VARCHAR(32) NOT NULL DEFAULT '',
    php_time_read VARCHAR(32) NOT NULL DEFAULT '',
    php_time_send VARCHAR(32) NOT NULL DEFAULT '',
    last_render_hash TEXT NOT NULL,
    last_applied_at VARCHAR(64) NULL,
    last_apply_status VARCHAR(64) NOT NULL DEFAULT '',
    last_apply_error TEXT NOT NULL,
    created_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    updated_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    INDEX idx_sites_user_id(user_id),
    CONSTRAINT fk_sites_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS proxy_targets (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    site_id BIGINT NOT NULL,
    target VARCHAR(512) NOT NULL,
    weight INT NOT NULL DEFAULT 100,
    is_backup TINYINT(1) NOT NULL DEFAULT 0,
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    created_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    UNIQUE KEY uq_proxy_target(site_id, target),
    INDEX idx_proxy_targets_site_id(site_id),
    CONSTRAINT fk_proxy_targets_site FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS apply_runs (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    site_id BIGINT NULL,
    action VARCHAR(64) NOT NULL,
    status VARCHAR(64) NOT NULL,
    message TEXT NOT NULL,
    created_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    CONSTRAINT fk_apply_runs_site FOREIGN KEY (site_id) REFERENCES sites(id) ON DELETE SET NULL
);

CREATE TABLE IF NOT EXISTS panel_users (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    username VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(32) NOT NULL DEFAULT 'admin',
    enabled TINYINT(1) NOT NULL DEFAULT 1,
    last_login_at VARCHAR(64) NULL,
    created_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    updated_at VARCHAR(64) NOT NULL DEFAULT (DATE_FORMAT(UTC_TIMESTAMP(6),'%Y-%m-%dT%H:%i:%s.%fZ')),
    INDEX idx_panel_users_username(username)
);

-- +goose Down
-- intentionally empty
