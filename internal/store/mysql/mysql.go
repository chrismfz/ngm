package mysql

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
	"mynginx/internal/nginx"
	"mynginx/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct {
	db *sql.DB
}

func init() {
	store.RegisterDriver("mysql", func(dsn string) (store.SiteStore, error) {
		return Open(dsn)
	})
}

func Open(dsn string) (*Store, error) {
	if dsn == "" {
		return nil, fmt.Errorf("mysql dsn is empty")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Migrate() error {
	if s.db == nil {
		return fmt.Errorf("db is nil")
	}
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("mysql"); err != nil {
		return err
	}
	return goose.Up(s.db, "migrations")
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) EnsureUser(username, homeDir string) (store.User, error) {
	return store.User{}, store.ErrNotImplemented
}
func (s *Store) GetUserByUsername(username string) (store.User, error) {
	return store.User{}, store.ErrNotImplemented
}
func (s *Store) GetUserByID(id int64) (store.User, error) {
	return store.User{}, store.ErrNotImplemented
}
func (s *Store) ListUsers() ([]store.User, error) {
	return nil, store.ErrNotImplemented
}
func (s *Store) UpsertSite(site store.Site) (store.Site, error) {
	return store.Site{}, store.ErrNotImplemented
}
func (s *Store) GetSiteByDomain(domain string) (store.Site, error) {
	return store.Site{}, store.ErrNotImplemented
}
func (s *Store) ListSites() ([]store.Site, error) { return nil, store.ErrNotImplemented }
func (s *Store) ListSitesByUserID(userID int64) ([]store.Site, error) {
	return nil, store.ErrNotImplemented
}
func (s *Store) CountRootDomainsByUserID(userID int64) (int, error) {
	return 0, store.ErrNotImplemented
}
func (s *Store) CountSubdomainsByUserID(userID int64) (int, error) {
	return 0, store.ErrNotImplemented
}
func (s *Store) DisableSiteByDomain(domain string) error { return store.ErrNotImplemented }
func (s *Store) EnableSiteByDomain(domain string) error  { return store.ErrNotImplemented }
func (s *Store) DeleteSiteByDomain(domain string) error  { return store.ErrNotImplemented }
func (s *Store) ListProxyTargetsBySiteID(siteID int64) ([]nginx.UpstreamTarget, error) {
	return nil, store.ErrNotImplemented
}
func (s *Store) UpsertProxyTarget(siteID int64, target string, weight int, isBackup bool, enabled bool) error {
	return store.ErrNotImplemented
}
func (s *Store) DisableProxyTarget(siteID int64, target string) error { return store.ErrNotImplemented }
func (s *Store) CreatePanelUser(username, passwordHash, role string, enabled bool) (store.PanelUser, error) {
	return store.PanelUser{}, store.ErrNotImplemented
}
func (s *Store) GetPanelUserByUsername(username string) (store.PanelUser, error) {
	return store.PanelUser{}, store.ErrNotImplemented
}
func (s *Store) UpdatePanelUserLastLogin(id int64) error    { return store.ErrNotImplemented }
func (s *Store) ListPanelUsers() ([]store.PanelUser, error) { return nil, store.ErrNotImplemented }
func (s *Store) ListPanelUsersByReseller(resellerID int64) ([]store.PanelUser, error) {
	return nil, store.ErrNotImplemented
}
func (s *Store) GetPanelUserByID(id int64) (store.PanelUser, error) {
	return store.PanelUser{}, store.ErrNotImplemented
}
func (s *Store) UpdatePanelUser(u store.PanelUser) (store.PanelUser, error) {
	return store.PanelUser{}, store.ErrNotImplemented
}
func (s *Store) DeletePanelUser(id int64) error                    { return store.ErrNotImplemented }
func (s *Store) IncrementFailedAttempts(userID int64) error        { return store.ErrNotImplemented }
func (s *Store) LockPanelUser(userID int64, until time.Time) error { return store.ErrNotImplemented }
func (s *Store) ResetFailedAttempts(userID int64) error            { return store.ErrNotImplemented }
func (s *Store) CreatePackage(pkg store.Package) (store.Package, error) {
	return store.Package{}, store.ErrNotImplemented
}
func (s *Store) UpdatePackage(pkg store.Package) (store.Package, error) {
	return store.Package{}, store.ErrNotImplemented
}
func (s *Store) DeletePackage(id int64) error { return store.ErrNotImplemented }
func (s *Store) GetPackageByID(id int64) (store.Package, error) {
	return store.Package{}, store.ErrNotImplemented
}
func (s *Store) ListPackages() ([]store.Package, error) { return nil, store.ErrNotImplemented }
func (s *Store) ListPackagesByCreator(creatorID int64) ([]store.Package, error) {
	return nil, store.ErrNotImplemented
}
func (s *Store) AssignPackage(userID, packageID, assignedByID int64) error {
	return store.ErrNotImplemented
}
func (s *Store) UnassignPackage(userID int64) error { return store.ErrNotImplemented }
func (s *Store) GetUserPackage(userID int64) (store.UserPackage, error) {
	return store.UserPackage{}, store.ErrNotImplemented
}
func (s *Store) GetEffectiveLimits(userID int64, role string) (store.UserLimits, error) {
	return store.UserLimits{}, store.ErrNotImplemented
}
