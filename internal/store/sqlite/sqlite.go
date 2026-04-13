package sqlite

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
	"mynginx/internal/nginx"
	"mynginx/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db *sql.DB
}

func init() {
	store.RegisterDriver("sqlite", func(dsn string) (store.SiteStore, error) {
		return Open(dsn)
	})
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate() error {
	if s.db == nil {
		return fmt.Errorf("db is nil")
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	return goose.Up(s.db, "migrations")
}

func (s *Store) EnsureUser(username, homeDir string) (store.User, error) {
	if username == "" || homeDir == "" {
		return store.User{}, fmt.Errorf("username and homeDir are required")
	}
	_, _ = s.db.Exec(`INSERT INTO users(username, home_dir) VALUES (?, ?) ON CONFLICT(username) DO UPDATE SET home_dir=excluded.home_dir`, username, homeDir)
	return s.GetUserByUsername(username)
}

func (s *Store) GetUserByUsername(username string) (store.User, error) {
	var u store.User
	var created string
	err := s.db.QueryRow(`SELECT id, username, home_dir, created_at FROM users WHERE username=?`, username).Scan(&u.ID, &u.Username, &u.HomeDir, &created)
	if err != nil {
		return store.User{}, err
	}
	u.CreatedAt = parseTime(created)
	return u, nil
}

func (s *Store) GetUserByID(id int64) (store.User, error) {
	var u store.User
	var created string
	err := s.db.QueryRow(`SELECT id, username, home_dir, created_at FROM users WHERE id=?`, id).Scan(&u.ID, &u.Username, &u.HomeDir, &created)
	if err != nil {
		return store.User{}, err
	}
	u.CreatedAt = parseTime(created)
	return u, nil
}

func (s *Store) ListUsers() ([]store.User, error) {
	rows, err := s.db.Query(`SELECT id, username, home_dir, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.User
	for rows.Next() {
		var u store.User
		var created string
		if err := rows.Scan(&u.ID, &u.Username, &u.HomeDir, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = parseTime(created)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UpsertSite(site store.Site) (store.Site, error) {
	if site.Domain == "" || site.UserID == 0 || site.Webroot == "" {
		return store.Site{}, fmt.Errorf("domain, user_id, webroot are required")
	}
	if site.Mode == "" {
		site.Mode = "php"
	}
	enableHTTP3 := boolInt(site.EnableHTTP3)
	enabled := boolInt(site.Enabled)
	_, err := s.db.Exec(`
		INSERT INTO sites(user_id, domain, parent_domain, mode, webroot, php_version, enable_http3, enabled, client_max_body_size, php_time_read, php_time_send)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain) DO UPDATE SET
			user_id=excluded.user_id, parent_domain=excluded.parent_domain, mode=excluded.mode, webroot=excluded.webroot, php_version=excluded.php_version,
			enable_http3=excluded.enable_http3, enabled=excluded.enabled,
			client_max_body_size=excluded.client_max_body_size, php_time_read=excluded.php_time_read, php_time_send=excluded.php_time_send,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, site.UserID, site.Domain, normalizeNullableString(site.ParentDomain), site.Mode, site.Webroot, site.PHPVersion, enableHTTP3, enabled, site.ClientMaxBodySize, site.PHPTimeRead, site.PHPTimeSend)
	if err != nil {
		return store.Site{}, err
	}
	return s.GetSiteByDomain(site.Domain)
}

func (s *Store) GetSiteByDomain(domain string) (store.Site, error) {
	rows, err := s.querySites(`WHERE domain=? ORDER BY domain`, domain)
	if err != nil {
		return store.Site{}, err
	}
	defer rows.Close()
	if rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return store.Site{}, err
		}
		return site, nil
	}
	return store.Site{}, sql.ErrNoRows
}

func (s *Store) ListSites() ([]store.Site, error) {
	rows, err := s.querySites(`ORDER BY domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) ListSitesByUserID(userID int64) ([]store.Site, error) {
	rows, err := s.querySites(`WHERE user_id=? ORDER BY domain`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) CountRootDomainsByUserID(userID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM sites WHERE user_id=? AND parent_domain IS NULL`, userID).Scan(&count)
	return count, err
}

func (s *Store) CountSubdomainsByUserID(userID int64) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(1) FROM sites WHERE user_id=? AND parent_domain IS NOT NULL`, userID).Scan(&count)
	return count, err
}

func (s *Store) DisableSiteByDomain(domain string) error {
	_, err := s.db.Exec(`UPDATE sites SET enabled=0, deleted_at=COALESCE(deleted_at, strftime('%Y-%m-%dT%H:%M:%fZ','now')), updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE domain=?`, strings.TrimSpace(domain))
	return err
}

func (s *Store) EnableSiteByDomain(domain string) error {
	_, err := s.db.Exec(`UPDATE sites SET enabled=1, deleted_at=NULL, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE domain=?`, strings.TrimSpace(domain))
	return err
}

func (s *Store) DeleteSiteByDomain(domain string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var siteID int64
	if err := tx.QueryRow(`SELECT id FROM sites WHERE domain=?`, strings.TrimSpace(domain)).Scan(&siteID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM proxy_targets WHERE site_id=?`, siteID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM apply_runs WHERE site_id=?`, siteID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sites WHERE id=?`, siteID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListProxyTargetsBySiteID(siteID int64) ([]nginx.UpstreamTarget, error) {
	rows, err := s.db.Query(`SELECT target, weight, is_backup, enabled FROM proxy_targets WHERE site_id=? ORDER BY is_backup ASC, id ASC`, siteID)
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

func (s *Store) UpsertProxyTarget(siteID int64, target string, weight int, isBackup bool, enabled bool) error {
	_, err := s.db.Exec(`INSERT INTO proxy_targets(site_id, target, weight, is_backup, enabled) VALUES(?,?,?,?,?) ON CONFLICT(site_id,target) DO UPDATE SET weight=excluded.weight,is_backup=excluded.is_backup,enabled=excluded.enabled`, siteID, strings.TrimSpace(target), weight, boolInt(isBackup), boolInt(enabled))
	return err
}

func (s *Store) DisableProxyTarget(siteID int64, target string) error {
	_, err := s.db.Exec(`UPDATE proxy_targets SET enabled=0 WHERE site_id=? AND target=?`, siteID, strings.TrimSpace(target))
	return err
}

func (s *Store) CreatePanelUser(username, passwordHash, role string, enabled bool) (store.PanelUser, error) {
	if role == "" {
		role = "admin"
	}
	_, err := s.db.Exec(`
		INSERT INTO panel_users(username, password_hash, role, enabled)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(username) DO UPDATE SET
			password_hash=excluded.password_hash,
			role=excluded.role,
			enabled=excluded.enabled,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, strings.TrimSpace(username), passwordHash, role, boolInt(enabled))
	if err != nil {
		return store.PanelUser{}, err
	}
	return s.GetPanelUserByUsername(username)
}

func (s *Store) GetPanelUserByUsername(username string) (store.PanelUser, error) {
	return s.getPanelUser(`WHERE username=?`, strings.TrimSpace(username))
}

func (s *Store) GetPanelUserByID(id int64) (store.PanelUser, error) {
	return s.getPanelUser(`WHERE id=?`, id)
}

func (s *Store) UpdatePanelUserLastLogin(id int64) error {
	_, err := s.db.Exec(`UPDATE panel_users SET last_login_at=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) ListPanelUsers() ([]store.PanelUser, error) {
	rows, err := s.queryPanelUsers(`ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectPanelUsers(rows)
}

func (s *Store) ListPanelUsersByReseller(resellerID int64) ([]store.PanelUser, error) {
	rows, err := s.queryPanelUsers(`WHERE reseller_id=? ORDER BY username`, resellerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectPanelUsers(rows)
}

func (s *Store) UpdatePanelUser(u store.PanelUser) (store.PanelUser, error) {
	_, err := s.db.Exec(`
		UPDATE panel_users SET
			password_hash=?, role=?, enabled=?, reseller_id=?, system_user=?,
			failed_attempts=?, locked_until=?, created_by=?, owner_id=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=?
	`, u.PasswordHash, u.Role, boolInt(u.Enabled), u.ResellerID, u.SystemUser, u.FailedAttempts, nullableTimeString(u.LockedUntil), u.CreatedBy, u.OwnerID, u.ID)
	if err != nil {
		return store.PanelUser{}, err
	}
	return s.GetPanelUserByID(u.ID)
}

func (s *Store) DeletePanelUser(id int64) error {
	_, err := s.db.Exec(`DELETE FROM panel_users WHERE id=?`, id)
	return err
}

func (s *Store) IncrementFailedAttempts(userID int64) error {
	_, err := s.db.Exec(`UPDATE panel_users SET failed_attempts=failed_attempts+1, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, userID)
	return err
}

func (s *Store) LockPanelUser(userID int64, until time.Time) error {
	_, err := s.db.Exec(`UPDATE panel_users SET locked_until=?, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, until.UTC().Format(time.RFC3339Nano), userID)
	return err
}

func (s *Store) ResetFailedAttempts(userID int64) error {
	_, err := s.db.Exec(`UPDATE panel_users SET failed_attempts=0, locked_until=NULL, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, userID)
	return err
}

func (s *Store) CreatePackage(pkg store.Package) (store.Package, error) {
	phpJSON, _ := json.Marshal(pkg.PHPVersions)
	res, err := s.db.Exec(`
		INSERT INTO packages(name, created_by, max_domains, max_subdomains, max_disk_mb, max_bandwidth_gb, php_versions,
			max_php_workers, max_mysql_dbs, max_mysql_users, max_email_accts, max_users, cgroup_cpu_pct, cgroup_mem_mb, cgroup_io_mbps)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, pkg.Name, pkg.CreatedBy, pkg.MaxDomains, pkg.MaxSubdomains, pkg.MaxDiskMB, pkg.MaxBandwidthGB, string(phpJSON), pkg.MaxPHPWorkers, pkg.MaxMySQLDBs, pkg.MaxMySQLUsers, pkg.MaxEmailAccts, pkg.MaxUsers, pkg.CgroupCPUPct, pkg.CgroupMemMB, pkg.CgroupIOMBps)
	if err != nil {
		return store.Package{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetPackageByID(id)
}

func (s *Store) UpdatePackage(pkg store.Package) (store.Package, error) {
	phpJSON, _ := json.Marshal(pkg.PHPVersions)
	_, err := s.db.Exec(`
		UPDATE packages SET
			name=?, created_by=?, max_domains=?, max_subdomains=?, max_disk_mb=?, max_bandwidth_gb=?, php_versions=?,
			max_php_workers=?, max_mysql_dbs=?, max_mysql_users=?, max_email_accts=?, max_users=?,
			cgroup_cpu_pct=?, cgroup_mem_mb=?, cgroup_io_mbps=?,
			updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE id=?
	`, pkg.Name, pkg.CreatedBy, pkg.MaxDomains, pkg.MaxSubdomains, pkg.MaxDiskMB, pkg.MaxBandwidthGB, string(phpJSON), pkg.MaxPHPWorkers, pkg.MaxMySQLDBs, pkg.MaxMySQLUsers, pkg.MaxEmailAccts, pkg.MaxUsers, pkg.CgroupCPUPct, pkg.CgroupMemMB, pkg.CgroupIOMBps, pkg.ID)
	if err != nil {
		return store.Package{}, err
	}
	return s.GetPackageByID(pkg.ID)
}

func (s *Store) DeletePackage(id int64) error {
	if id == 1 {
		return store.ErrProtectedPackage
	}
	var refs int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM user_packages WHERE package_id=?`, id).Scan(&refs); err != nil {
		return err
	}
	if refs > 0 {
		return fmt.Errorf("package %d is assigned to %d user(s)", id, refs)
	}
	_, err := s.db.Exec(`DELETE FROM packages WHERE id=?`, id)
	return err
}

func (s *Store) GetPackageByID(id int64) (store.Package, error) {
	rows, err := s.queryPackages(`WHERE id=?`, id)
	if err != nil {
		return store.Package{}, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanPackage(rows)
	}
	return store.Package{}, sql.ErrNoRows
}

func (s *Store) ListPackages() ([]store.Package, error) {
	rows, err := s.queryPackages(`ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Package
	for rows.Next() {
		pkg, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pkg)
	}
	return out, rows.Err()
}

func (s *Store) ListPackagesByCreator(creatorID int64) ([]store.Package, error) {
	rows, err := s.queryPackages(`WHERE created_by=? ORDER BY id ASC`, creatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Package
	for rows.Next() {
		pkg, err := scanPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pkg)
	}
	return out, rows.Err()
}

func (s *Store) AssignPackage(userID, packageID, assignedByID int64) error {
	_, err := s.db.Exec(`
		INSERT INTO user_packages(user_id, package_id, assigned_by)
		VALUES(?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET package_id=excluded.package_id, assigned_by=excluded.assigned_by, assigned_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	`, userID, packageID, assignedByID)
	return err
}

func (s *Store) UnassignPackage(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM user_packages WHERE user_id=?`, userID)
	return err
}

func (s *Store) GetUserPackage(userID int64) (store.UserPackage, error) {
	row := s.db.QueryRow(`
		SELECT up.id, up.user_id, up.package_id, up.assigned_by, up.assigned_at,
		       p.id, p.name, p.created_by, p.max_domains, p.max_subdomains, p.max_disk_mb, p.max_bandwidth_gb,
		       p.php_versions, p.max_php_workers, p.max_mysql_dbs, p.max_mysql_users, p.max_email_accts, p.max_users,
		       p.cgroup_cpu_pct, p.cgroup_mem_mb, p.cgroup_io_mbps, p.created_at, p.updated_at
		FROM user_packages up
		JOIN packages p ON p.id = up.package_id
		WHERE up.user_id=?
	`, userID)
	var up store.UserPackage
	var assignedAt string
	var pkg store.Package
	var phpJSON, pkgCreated, pkgUpdated string
	if err := row.Scan(&up.ID, &up.UserID, &up.PackageID, &up.AssignedBy, &assignedAt,
		&pkg.ID, &pkg.Name, &pkg.CreatedBy, &pkg.MaxDomains, &pkg.MaxSubdomains, &pkg.MaxDiskMB, &pkg.MaxBandwidthGB,
		&phpJSON, &pkg.MaxPHPWorkers, &pkg.MaxMySQLDBs, &pkg.MaxMySQLUsers, &pkg.MaxEmailAccts, &pkg.MaxUsers,
		&pkg.CgroupCPUPct, &pkg.CgroupMemMB, &pkg.CgroupIOMBps, &pkgCreated, &pkgUpdated); err != nil {
		return store.UserPackage{}, err
	}
	_ = json.Unmarshal([]byte(phpJSON), &pkg.PHPVersions)
	up.AssignedAt = parseTime(assignedAt)
	pkg.CreatedAt = parseTime(pkgCreated)
	pkg.UpdatedAt = parseTime(pkgUpdated)
	up.Package = pkg
	return up, nil
}

func (s *Store) GetEffectiveLimits(userID int64, role string) (store.UserLimits, error) {
	if role == "admin" || role == "reseller" {
		return store.UnlimitedLimits(), nil
	}
	up, err := s.GetUserPackage(userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ZeroLimits(), store.ErrNoPackageAssigned
		}
		return store.ZeroLimits(), err
	}
	return store.UserLimits{
		MaxDomains: up.Package.MaxDomains, MaxSubdomains: up.Package.MaxSubdomains, MaxDiskMB: up.Package.MaxDiskMB,
		MaxBandwidthGB: up.Package.MaxBandwidthGB, PHPVersions: up.Package.PHPVersions, MaxPHPWorkers: up.Package.MaxPHPWorkers,
		MaxMySQLDBs: up.Package.MaxMySQLDBs, MaxMySQLUsers: up.Package.MaxMySQLUsers, MaxEmailAccts: up.Package.MaxEmailAccts,
		MaxUsers: up.Package.MaxUsers, CgroupCPUPct: up.Package.CgroupCPUPct, CgroupMemMB: up.Package.CgroupMemMB, CgroupIOMBps: up.Package.CgroupIOMBps,
	}, nil
}

func (s *Store) UpdateApplyResult(domain, status, errMsg, renderHash string) error {
	_, err := s.db.Exec(`UPDATE sites SET last_apply_status=?, last_apply_error=?, last_render_hash=?, last_applied_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE domain=?`, status, errMsg, renderHash, domain)
	return err
}

func (s *Store) ListPendingSites() ([]store.Site, error) {
	rows, err := s.querySites(`WHERE enabled=1 AND (last_applied_at IS NULL OR last_apply_status!='ok' OR updated_at > last_applied_at) ORDER BY domain ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

func (s *Store) querySites(clause string, args ...any) (*sql.Rows, error) {
	q := `SELECT id, user_id, domain, mode, webroot, php_version,
		parent_domain,
		COALESCE(client_max_body_size,''), COALESCE(php_time_read,''), COALESCE(php_time_send,''),
		enable_http3, enabled,
		created_at, updated_at,
		COALESCE(last_render_hash,''), COALESCE(last_apply_status,''), COALESCE(last_apply_error,''),
		last_applied_at
		FROM sites ` + clause
	return s.db.Query(q, args...)
}

func scanSite(rows scanner) (store.Site, error) {
	var out store.Site
	var created, updated string
	var enableHTTP3, enabled int
	var parentDomain sql.NullString
	var lastApplied sql.NullString
	if err := rows.Scan(&out.ID, &out.UserID, &out.Domain, &out.Mode, &out.Webroot, &out.PHPVersion, &parentDomain,
		&out.ClientMaxBodySize, &out.PHPTimeRead, &out.PHPTimeSend,
		&enableHTTP3, &enabled, &created, &updated, &out.LastRenderHash, &out.LastApplyStatus, &out.LastApplyError, &lastApplied); err != nil {
		return store.Site{}, err
	}
	if parentDomain.Valid && strings.TrimSpace(parentDomain.String) != "" {
		val := strings.TrimSpace(parentDomain.String)
		out.ParentDomain = &val
	}
	out.EnableHTTP3 = enableHTTP3 == 1
	out.Enabled = enabled == 1
	out.CreatedAt = parseTime(created)
	out.UpdatedAt = parseTime(updated)
	if lastApplied.Valid && lastApplied.String != "" {
		t := parseTime(lastApplied.String)
		out.LastAppliedAt = &t
	}
	return out, nil
}

func normalizeNullableString(in *string) any {
	if in == nil {
		return nil
	}
	v := strings.TrimSpace(*in)
	if v == "" {
		return nil
	}
	return v
}

func (s *Store) queryPanelUsers(clause string, args ...any) (*sql.Rows, error) {
	q := `SELECT id, username, password_hash, role, enabled, reseller_id, system_user, failed_attempts, locked_until, created_by, owner_id, last_login_at, created_at, updated_at FROM panel_users ` + clause
	return s.db.Query(q, args...)
}

func (s *Store) getPanelUser(clause string, arg any) (store.PanelUser, error) {
	rows, err := s.queryPanelUsers(clause, arg)
	if err != nil {
		return store.PanelUser{}, err
	}
	defer rows.Close()
	if rows.Next() {
		return scanPanelUser(rows)
	}
	return store.PanelUser{}, sql.ErrNoRows
}

func collectPanelUsers(rows *sql.Rows) ([]store.PanelUser, error) {
	var out []store.PanelUser
	for rows.Next() {
		u, err := scanPanelUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func scanPanelUser(rows scanner) (store.PanelUser, error) {
	var u store.PanelUser
	var enabled int
	var lockedUntil, lastLogin sql.NullString
	var created, updated string
	if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &enabled, &u.ResellerID, &u.SystemUser, &u.FailedAttempts, &lockedUntil, &u.CreatedBy, &u.OwnerID, &lastLogin, &created, &updated); err != nil {
		return store.PanelUser{}, err
	}
	u.Enabled = enabled == 1
	if lockedUntil.Valid && lockedUntil.String != "" {
		t := parseTime(lockedUntil.String)
		u.LockedUntil = &t
	}
	if lastLogin.Valid && lastLogin.String != "" {
		t := parseTime(lastLogin.String)
		u.LastLoginAt = &t
	}
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, nil
}

func (s *Store) queryPackages(clause string, args ...any) (*sql.Rows, error) {
	q := `SELECT id, name, created_by, max_domains, max_subdomains, max_disk_mb, max_bandwidth_gb, php_versions, max_php_workers, max_mysql_dbs, max_mysql_users, max_email_accts, max_users, cgroup_cpu_pct, cgroup_mem_mb, cgroup_io_mbps, created_at, updated_at FROM packages ` + clause
	return s.db.Query(q, args...)
}

func scanPackage(rows scanner) (store.Package, error) {
	var p store.Package
	var phpJSON, created, updated string
	if err := rows.Scan(&p.ID, &p.Name, &p.CreatedBy, &p.MaxDomains, &p.MaxSubdomains, &p.MaxDiskMB, &p.MaxBandwidthGB, &phpJSON, &p.MaxPHPWorkers, &p.MaxMySQLDBs, &p.MaxMySQLUsers, &p.MaxEmailAccts, &p.MaxUsers, &p.CgroupCPUPct, &p.CgroupMemMB, &p.CgroupIOMBps, &created, &updated); err != nil {
		return store.Package{}, err
	}
	_ = json.Unmarshal([]byte(phpJSON), &p.PHPVersions)
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

type scanner interface{ Scan(dest ...any) error }

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func parseTime(v string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

func nullableTimeString(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
