package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"strings"
	_ "modernc.org/sqlite"

	"mynginx/internal/store"

       "mynginx/internal/nginx"

)

type Store struct {
	db *sql.DB
}

// ListProxyTargetsBySiteID returns enabled proxy upstream targets for a site.
func (s *Store) ListProxyTargetsBySiteID(siteID int64) ([]nginx.UpstreamTarget, error) {
    rows, err := s.db.Query(`
	  SELECT target, weight, is_backup, enabled
          FROM proxy_targets
         WHERE site_id = ?
         ORDER BY is_backup ASC, id ASC
    `, siteID)
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var out []nginx.UpstreamTarget
    for rows.Next() {
        var t nginx.UpstreamTarget
        var isBackup, enabled int
        if err := rows.Scan(&t.Addr, &t.Weight, &isBackup, &enabled); err != nil {
            return nil, err
        }
        t.Backup = isBackup == 1
        t.Enabled = enabled == 1
        out = append(out, t)
    }
    return out, rows.Err()
}



func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}

	// busy_timeout helps when you add API later
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// conservative pool for single-file sqlite
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	s := &Store{db: db}
	return s, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate() error {
	return migrate(s.db)
}

func (s *Store) EnsureUser(username, homeDir string) (store.User, error) {
	if username == "" {
		return store.User{}, fmt.Errorf("username is required")
	}
	if homeDir == "" {
		return store.User{}, fmt.Errorf("homeDir is required")
	}

	// insert if not exists
	_, _ = s.db.Exec(`
		INSERT INTO users(username, home_dir)
		VALUES (?, ?)
		ON CONFLICT(username) DO UPDATE SET home_dir=excluded.home_dir
	`, username, homeDir)

	return s.GetUserByUsername(username)
}

func (s *Store) GetUserByUsername(username string) (store.User, error) {
	var u store.User
	var created string

	err := s.db.QueryRow(`
		SELECT id, username, home_dir, created_at
		FROM users
		WHERE username=?
	`, username).Scan(&u.ID, &u.Username, &u.HomeDir, &created)
	if err != nil {
		return store.User{}, err
	}

	t, _ := time.Parse(time.RFC3339Nano, created)
	u.CreatedAt = t
	return u, nil
}

func (s *Store) UpsertSite(site store.Site) (store.Site, error) {
	if site.Domain == "" {
		return store.Site{}, fmt.Errorf("domain is required")
	}
	if site.UserID == 0 {
		return store.Site{}, fmt.Errorf("user_id is required")
	}
	if site.Mode == "" {
		site.Mode = "php"
	}
	if site.Webroot == "" {
		return store.Site{}, fmt.Errorf("webroot is required")
	}
	if site.Mode != "php" && site.Mode != "proxy" && site.Mode != "static" {
		return store.Site{}, fmt.Errorf("invalid mode %q", site.Mode)
	}

	enableHTTP3 := 0
	if site.EnableHTTP3 {
		enableHTTP3 = 1
	}
	enabled := 0
	if site.Enabled {
		enabled = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO sites(
			user_id, domain, mode, webroot, php_version,
			enable_http3, enabled
			, client_max_body_size, php_time_read, php_time_send
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain) DO UPDATE SET
			user_id=excluded.user_id,
			mode=excluded.mode,
			webroot=excluded.webroot,
			php_version=excluded.php_version,
			enable_http3=excluded.enable_http3,
			enabled=excluded.enabled,
			client_max_body_size=excluded.client_max_body_size,
			php_time_read=excluded.php_time_read,
			php_time_send=excluded.php_time_send,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`,
		site.UserID, site.Domain, site.Mode, site.Webroot, site.PHPVersion,
		enableHTTP3, enabled,
		site.ClientMaxBodySize, site.PHPTimeRead, site.PHPTimeSend,
	)
	if err != nil {
		return store.Site{}, err
	}

	return s.GetSiteByDomain(site.Domain)
}

func (s *Store) GetSiteByDomain(domain string) (store.Site, error) {
	var out store.Site
	var created, updated string
	var enableHTTP3, enabled int
	var lastApplied sql.NullString

	err := s.db.QueryRow(`
		SELECT id, user_id, domain, mode, webroot, php_version,
		       COALESCE(client_max_body_size,''), COALESCE(php_time_read,''), COALESCE(php_time_send,''),
		       enable_http3, enabled,
		       created_at, updated_at,
		       COALESCE(last_render_hash,''), COALESCE(last_apply_status,''), COALESCE(last_apply_error,''),
		       last_applied_at
		FROM sites WHERE domain=?
	`, domain).Scan(
		&out.ID, &out.UserID, &out.Domain, &out.Mode, &out.Webroot, &out.PHPVersion,
		&out.ClientMaxBodySize, &out.PHPTimeRead, &out.PHPTimeSend,
		&enableHTTP3, &enabled,
		&created, &updated,
		&out.LastRenderHash, &out.LastApplyStatus, &out.LastApplyError,
		&lastApplied,
	)
	if err != nil {
		return store.Site{}, err
	}

	out.EnableHTTP3 = enableHTTP3 == 1
	out.Enabled = enabled == 1

	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		out.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updated); err == nil {
		out.UpdatedAt = t
	}
	if lastApplied.Valid && lastApplied.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, lastApplied.String); err == nil {
			out.LastAppliedAt = &t
		}
	}
	return out, nil
}

func (s *Store) ListSites() ([]store.Site, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, domain, mode, webroot, php_version,
		       COALESCE(client_max_body_size,''), COALESCE(php_time_read,''), COALESCE(php_time_send,''),
		       enable_http3, enabled,
		       created_at, updated_at,
		       COALESCE(last_render_hash,''), COALESCE(last_apply_status,''), COALESCE(last_apply_error,''),
		       last_applied_at
		FROM sites
		ORDER BY domain ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Site
	for rows.Next() {
		var sitem store.Site
		var created, updated string
		var enableHTTP3, enabled int
		var lastApplied sql.NullString

		if err := rows.Scan(
			&sitem.ID, &sitem.UserID, &sitem.Domain, &sitem.Mode, &sitem.Webroot, &sitem.PHPVersion,
			&sitem.ClientMaxBodySize, &sitem.PHPTimeRead, &sitem.PHPTimeSend,
			&enableHTTP3, &enabled,
			&created, &updated,
			&sitem.LastRenderHash, &sitem.LastApplyStatus, &sitem.LastApplyError,
			&lastApplied,
		); err != nil {
			return nil, err
		}

		sitem.EnableHTTP3 = enableHTTP3 == 1
		sitem.Enabled = enabled == 1

		if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
			sitem.CreatedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, updated); err == nil {
			sitem.UpdatedAt = t
		}
		if lastApplied.Valid && lastApplied.String != "" {
			if t, err := time.Parse(time.RFC3339Nano, lastApplied.String); err == nil {
				sitem.LastAppliedAt = &t
			}
		}
		out = append(out, sitem)
	}

	return out, rows.Err()
}


func (s *Store) EnableSiteByDomain(domain string) error {
    domain = strings.TrimSpace(domain)
    if domain == "" {
        return fmt.Errorf("domain is required")
    }
    _, err := s.db.Exec(`
        UPDATE sites
           SET enabled    = 1,
               deleted_at = NULL,
               updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
         WHERE domain = ?
    `, domain)
    return err
}

func (s *Store) DeleteSiteByDomain(domain string) error {
    domain = strings.TrimSpace(domain)
    if domain == "" {
        return fmt.Errorf("domain is required")
    }

    tx, err := s.db.Begin()
    if err != nil {
        return err
    }
    defer func() {
        if err != nil {
            _ = tx.Rollback()
        }
    }()

    var siteID int64
    row := tx.QueryRow(`SELECT id FROM sites WHERE domain=?`, domain)
    if scanErr := row.Scan(&siteID); scanErr != nil {
        err = scanErr
        return err
    }

    // Remove children first (FK-safe)
    if _, execErr := tx.Exec(`DELETE FROM proxy_targets WHERE site_id=?`, siteID); execErr != nil {
        err = execErr
        return err
    }
    if _, execErr := tx.Exec(`DELETE FROM apply_runs WHERE site_id=?`, siteID); execErr != nil {
        err = execErr
        return err
    }
    if _, execErr := tx.Exec(`DELETE FROM sites WHERE id=?`, siteID); execErr != nil {
        err = execErr
        return err
    }

    if commitErr := tx.Commit(); commitErr != nil {
        err = commitErr
        return err
    }
    return nil
}


func (s *Store) DisableSiteByDomain(domain string) error {
        // soft delete: keep row for audit + pending delete apply
        _, err := s.db.Exec(`
                UPDATE sites
                   SET enabled = 0,
                       deleted_at = COALESCE(deleted_at, strftime('%Y-%m-%dT%H:%M:%fZ','now')),
                       updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
                 WHERE domain = ?
        `, domain)
        return err
}

func (s *Store) UpdateApplyResult(domain, status, errMsg, renderHash string) error {
        if domain == "" {
                return fmt.Errorf("domain is required")
        }
    if domain == "" {
        return fmt.Errorf("domain is required")
    }
    _, err := s.db.Exec(`
        UPDATE sites
           SET last_apply_status = ?,
               last_apply_error  = ?,
               last_render_hash  = ?,
               last_applied_at   = (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
         WHERE domain = ?;
    `, status, errMsg, renderHash, domain)
        return err
}


func (s *Store) ListPendingSites() ([]store.Site, error) {
        rows, err := s.db.Query(`
                SELECT id, user_id, domain, mode, webroot, php_version,
		       COALESCE(client_max_body_size,''), COALESCE(php_time_read,''), COALESCE(php_time_send,''),
                       enable_http3, enabled,
                       created_at, updated_at,
                       COALESCE(last_render_hash,''), COALESCE(last_apply_status,''), COALESCE(last_apply_error,''),
                       last_applied_at
                FROM sites
                WHERE enabled=1
                  AND (last_applied_at IS NULL
                       OR last_apply_status!='ok'
                       OR updated_at > last_applied_at)
                ORDER BY domain ASC
        `)
        if err != nil {
                return nil, err
        }
        defer rows.Close()

        // reuse existing scanner by calling s.ListSites() would be heavy; keep simple:
        var out []store.Site
        for rows.Next() {
                var site store.Site
                var created, updated string
                var enableHTTP3, enabled int
                var lastApplied *string // nullable

                if err := rows.Scan(
                        &site.ID, &site.UserID, &site.Domain, &site.Mode, &site.Webroot, &site.PHPVersion,
			&site.ClientMaxBodySize, &site.PHPTimeRead, &site.PHPTimeSend,
                        &enableHTTP3, &enabled,
                        &created, &updated,
                        &site.LastRenderHash, &site.LastApplyStatus, &site.LastApplyError,
                        &lastApplied,
                ); err != nil {
                        return nil, err
                }

                site.EnableHTTP3 = enableHTTP3 == 1
                site.Enabled = enabled == 1
                // timestamps parsed already in Get/List; not critical for apply
                out = append(out, site)
        }
        return out, rows.Err()
}




func (s *Store) CreatePanelUser(username, passwordHash, role string, enabled bool) (store.PanelUser, error) {
	if username == "" {
		return store.PanelUser{}, fmt.Errorf("username is required")
	}
	if passwordHash == "" {
		return store.PanelUser{}, fmt.Errorf("passwordHash is required")
	}
	if role == "" {
		role = "admin"
	}
	en := 0
	if enabled {
		en = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO panel_users(username, password_hash, role, enabled)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET
			password_hash=excluded.password_hash,
			role=excluded.role,
			enabled=excluded.enabled,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, username, passwordHash, role, en)
	if err != nil {
		return store.PanelUser{}, err
	}
	return s.GetPanelUserByUsername(username)
}

func (s *Store) GetPanelUserByUsername(username string) (store.PanelUser, error) {
	var u store.PanelUser
	var enabled int
	var lastLogin sql.NullString
	var created, updated string

	err := s.db.QueryRow(`
		SELECT id, username, password_hash, role, enabled,
		       last_login_at, created_at, updated_at
		  FROM panel_users
		 WHERE username=?
	`, username).Scan(
		&u.ID, &u.Username, &u.PasswordHash, &u.Role, &enabled,
		&lastLogin, &created, &updated,
	)
	if err != nil {
		return store.PanelUser{}, err
	}
	u.Enabled = enabled == 1

	if lastLogin.Valid && lastLogin.String != "" {
		if t, err := time.Parse(time.RFC3339Nano, lastLogin.String); err == nil {
			u.LastLoginAt = &t
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
		u.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updated); err == nil {
		u.UpdatedAt = t
	}
	return u, nil
}

func (s *Store) UpdatePanelUserLastLogin(id int64) error {
	if id == 0 {
		return fmt.Errorf("id is required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`
		UPDATE panel_users
		   SET last_login_at=?,
		       updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE id=?
	`, now, id)
	return err
}

func (s *Store) GetUserByID(id int64) (store.User, error) {
        if id == 0 {
                return store.User{}, fmt.Errorf("id is required")
        }
        var out store.User
        var created string
        err := s.db.QueryRow(`
                SELECT id, username, home_dir, created_at
                  FROM users
                 WHERE id=?
        `, id).Scan(&out.ID, &out.Username, &out.HomeDir, &created)
        if err != nil {
                return store.User{}, err
        }
        if t, err := time.Parse(time.RFC3339Nano, created); err == nil {
                out.CreatedAt = t
        }
        return out, nil
}


func (s *Store) UpsertProxyTarget(siteID int64, target string, weight int, isBackup bool, enabled bool) error {
	if siteID == 0 {
		return fmt.Errorf("siteID is required")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("target is required")
	}
	if weight <= 0 {
		weight = 100
	}
	bk := 0
	if isBackup {
		bk = 1
	}
	en := 0
	if enabled {
		en = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO proxy_targets(site_id, target, weight, is_backup, enabled)
		VALUES(?,?,?,?,?)
		ON CONFLICT(site_id, target) DO UPDATE SET
			weight=excluded.weight,
			is_backup=excluded.is_backup,
			enabled=excluded.enabled
	`, siteID, target, weight, bk, en)
	return err
}

func (s *Store) DisableProxyTarget(siteID int64, target string) error {
	if siteID == 0 {
		return fmt.Errorf("siteID is required")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("target is required")
	}
	_, err := s.db.Exec(`
		UPDATE proxy_targets
		   SET enabled=0
		 WHERE site_id=? AND target=?
	`, siteID, target)
	return err
}
