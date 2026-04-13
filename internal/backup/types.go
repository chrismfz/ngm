package backup

import "time"

type BackupScope string

const (
	ScopeUser     BackupScope = "user"
	ScopeReseller BackupScope = "reseller"
	ScopeAll      BackupScope = "all"
)

type Manifest struct {
	SchemaVersion int             `json:"schema_version"`
	CreatedAt     time.Time       `json:"created_at"`
	NodeID        string          `json:"node_id"`
	Scope         BackupScope     `json:"scope"`
	Driver        string          `json:"driver"`
	Subject       ManifestSubject `json:"subject"`
	IncludeCerts  bool            `json:"include_certs"`
}

type ManifestSubject struct {
	Username   string `json:"username,omitempty"`
	UserID     int64  `json:"user_id,omitempty"`
	ResellerID int64  `json:"reseller_id,omitempty"`
}

type Dump struct {
	PanelUsers   []PanelUserDump   `json:"panel_users"`
	Users        []UserDump        `json:"users"`
	Packages     []PackageDump     `json:"packages"`
	UserPackages []UserPackageDump `json:"user_packages"`
	Sites        []SiteDump        `json:"sites"`
	ProxyTargets []ProxyTargetDump `json:"proxy_targets"`
}

type PanelUserDump struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Role         string `json:"role"`
	Enabled      bool   `json:"enabled"`
	ResellerName string `json:"reseller_name,omitempty"`
	SystemUser   string `json:"system_user,omitempty"`
}

type UserDump struct {
	Username string `json:"username"`
	HomeDir  string `json:"home_dir"`
}

type PackageDump struct {
	Name           string   `json:"name"`
	MaxDomains     int      `json:"max_domains"`
	MaxSubdomains  int      `json:"max_subdomains"`
	MaxDiskMB      int      `json:"max_disk_mb"`
	MaxBandwidthGB int      `json:"max_bandwidth_gb"`
	PHPVersions    []string `json:"php_versions"`
	MaxPHPWorkers  int      `json:"max_php_workers"`
	MaxMySQLDBs    int      `json:"max_mysql_dbs"`
	MaxMySQLUsers  int      `json:"max_mysql_users"`
	MaxEmailAccts  int      `json:"max_email_accts"`
	MaxUsers       int      `json:"max_users"`
	CgroupCPUPct   int      `json:"cgroup_cpu_pct"`
	CgroupMemMB    int      `json:"cgroup_mem_mb"`
	CgroupIOMBps   int      `json:"cgroup_io_mbps"`
}

type UserPackageDump struct {
	Username    string `json:"username"`
	PackageName string `json:"package_name"`
}

type SiteDump struct {
	OwnerUsername string  `json:"owner_username"`
	Domain        string  `json:"domain"`
	ParentDomain  *string `json:"parent_domain,omitempty"`
	Mode          string  `json:"mode"`
	Webroot       string  `json:"webroot"`
	PHPVersion    string  `json:"php_version"`
	EnableHTTP3   bool    `json:"enable_http3"`
	Enabled       bool    `json:"enabled"`
	ClientMaxBody string  `json:"client_max_body_size,omitempty"`
	PHPTimeRead   string  `json:"php_time_read,omitempty"`
	PHPTimeSend   string  `json:"php_time_send,omitempty"`
}

type ProxyTargetDump struct {
	SiteDomain string `json:"site_domain"`
	Target     string `json:"target"`
	Weight     int    `json:"weight"`
	IsBackup   bool   `json:"is_backup"`
	Enabled    bool   `json:"enabled"`
}

type BackupOptions struct {
	Scope        BackupScope
	Username     string
	IncludeCerts bool
	NodeID       string
	Driver       string
	HomeRoot     string
	CertsRoot    string
	Now          time.Time
}

type BackupResult struct {
	Manifest      Manifest
	SiteFileCount int
	CertFileCount int
}

type RestoreOptions struct {
	FilePath  string
	NewUser   string
	HomeRoot  string
	CertsRoot string
}

type RestoreResult struct {
	Manifest      Manifest
	PanelUsers    int
	Users         int
	Packages      int
	Assignments   int
	Sites         int
	ProxyTargets  int
	SiteFileCount int
	CertFileCount int
	Warnings      []string
}
