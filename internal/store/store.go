package store

import (
	"fmt"
	"mynginx/internal/nginx"
	"time"
)

var (
	ErrProtectedPackage  = fmt.Errorf("protected package")
	ErrNoPackageAssigned = fmt.Errorf("no package assigned")
	ErrNotImplemented    = fmt.Errorf("not implemented")
)

type PanelUser struct {
	ID             int64
	Username       string
	PasswordHash   string
	Role           string
	Enabled        bool
	ResellerID     *int64
	SystemUser     string
	FailedAttempts int
	LockedUntil    *time.Time
	CreatedBy      *int64
	OwnerID        *int64
	LastLoginAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type User struct {
	ID        int64
	Username  string
	HomeDir   string
	CreatedAt time.Time
}

type Package struct {
	ID             int64
	Name           string
	CreatedBy      *int64
	MaxDomains     int
	MaxSubdomains  int
	MaxDiskMB      int
	MaxBandwidthGB int
	PHPVersions    []string
	MaxPHPWorkers  int
	MaxMySQLDBs    int
	MaxMySQLUsers  int
	MaxEmailAccts  int
	MaxUsers       int
	CgroupCPUPct   int
	CgroupMemMB    int
	CgroupIOMBps   int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type UserPackage struct {
	ID         int64
	UserID     int64
	PackageID  int64
	AssignedBy *int64
	AssignedAt time.Time
	Package    Package
}

type UserLimits struct {
	MaxDomains     int
	MaxSubdomains  int
	MaxDiskMB      int
	MaxBandwidthGB int
	PHPVersions    []string
	MaxPHPWorkers  int
	MaxMySQLDBs    int
	MaxMySQLUsers  int
	MaxEmailAccts  int
	MaxUsers       int
	CgroupCPUPct   int
	CgroupMemMB    int
	CgroupIOMBps   int
}

func UnlimitedLimits() UserLimits {
	return UserLimits{
		MaxDomains: -1, MaxSubdomains: -1, MaxDiskMB: -1, MaxBandwidthGB: -1,
		MaxPHPWorkers: -1, MaxMySQLDBs: -1, MaxMySQLUsers: -1, MaxEmailAccts: -1, MaxUsers: -1,
		CgroupCPUPct: -1, CgroupMemMB: -1, CgroupIOMBps: -1,
		PHPVersions: []string{},
	}
}

func ZeroLimits() UserLimits {
	return UserLimits{
		PHPVersions: []string{},
	}
}

type Site struct {
	ID           int64
	UserID       int64
	Domain       string
	ParentDomain *string
	Mode         string // "php" | "proxy" | "static"
	Webroot      string
	PHPVersion   string
	EnableHTTP3  bool
	Enabled      bool

	// Per-site nginx knobs
	ClientMaxBodySize string // e.g. "32M", "128M"

	// Per-site php mode nginx timeouts (fastcgi_*_timeout)
	PHPTimeRead string // e.g. "60s", "300s"
	PHPTimeSend string // e.g. "60s", "300s"

	CreatedAt time.Time
	UpdatedAt time.Time

	LastRenderHash  string
	LastAppliedAt   *time.Time
	LastApplyStatus string
	LastApplyError  string
}

type SiteStore interface {
	Migrate() error

	EnsureUser(username, homeDir string) (User, error)
	GetUserByUsername(username string) (User, error)
	GetUserByID(id int64) (User, error)
	ListUsers() ([]User, error)

	UpsertSite(s Site) (Site, error)
	GetSiteByDomain(domain string) (Site, error)
	ListSites() ([]Site, error)
	ListSitesByUserID(userID int64) ([]Site, error)
	CountRootDomainsByUserID(userID int64) (int, error)
	CountSubdomainsByUserID(userID int64) (int, error)
	DisableSiteByDomain(domain string) error
	// re-enable a previously disabled site
	EnableSiteByDomain(domain string) error

	// hard delete: permanently remove site row (and related rows)
	DeleteSiteByDomain(domain string) error

	// Proxy upstream targets (mode=proxy)
	ListProxyTargetsBySiteID(siteID int64) ([]nginx.UpstreamTarget, error)
	UpsertProxyTarget(siteID int64, target string, weight int, isBackup bool, enabled bool) error
	DisableProxyTarget(siteID int64, target string) error

	CreatePanelUser(username, passwordHash, role string, enabled bool) (PanelUser, error)
	GetPanelUserByUsername(username string) (PanelUser, error)
	UpdatePanelUserLastLogin(id int64) error
	ListPanelUsers() ([]PanelUser, error)
	ListPanelUsersByReseller(resellerID int64) ([]PanelUser, error)
	GetPanelUserByID(id int64) (PanelUser, error)
	UpdatePanelUser(u PanelUser) (PanelUser, error)
	DeletePanelUser(id int64) error
	IncrementFailedAttempts(userID int64) error
	LockPanelUser(userID int64, until time.Time) error
	ResetFailedAttempts(userID int64) error

	CreatePackage(pkg Package) (Package, error)
	UpdatePackage(pkg Package) (Package, error)
	DeletePackage(id int64) error
	GetPackageByID(id int64) (Package, error)
	ListPackages() ([]Package, error)
	ListPackagesByCreator(creatorID int64) ([]Package, error)

	AssignPackage(userID, packageID, assignedByID int64) error
	UnassignPackage(userID int64) error
	GetUserPackage(userID int64) (UserPackage, error)
	GetEffectiveLimits(userID int64, role string) (UserLimits, error)

	Close() error
}
