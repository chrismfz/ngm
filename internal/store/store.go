package store

import (
	"time"
	"mynginx/internal/nginx"
)

type PanelUser struct {
	ID           int64
	Username     string
	PasswordHash string
	Role         string
	Enabled      bool
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}


type User struct {
	ID       int64
	Username string
	HomeDir  string
	CreatedAt time.Time
}

type Site struct {
	ID          int64
	UserID      int64
	Domain      string
	Mode        string // "php" | "proxy" | "static"
	Webroot     string
	PHPVersion  string
	EnableHTTP3 bool
	Enabled     bool

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

	UpsertSite(s Site) (Site, error)
	GetSiteByDomain(domain string) (Site, error)
	ListSites() ([]Site, error)
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

	Close() error
}

