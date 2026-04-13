package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	API      APIConfig      `yaml:"api"`
	Nginx    NginxConfig    `yaml:"nginx"`
	Certs    CertsConfig    `yaml:"certs"`
	PHPFPM   PHPFPMConfig   `yaml:"phpfpm"`
	Hosting  HostingConfig  `yaml:"hosting"`
	Security SecurityConfig `yaml:"security"`
	Storage  StorageConfig  `yaml:"storage"`
	DNS      DNSConfig      `yaml:"dns"`
}

type APIConfig struct {
	Listen   string   `yaml:"listen"`
	Hostname string   `yaml:"hostname"`
	Tokens   []string `yaml:"tokens"`
	AllowIPs []string `yaml:"allow_ips"`
}

type NginxConfig struct {
	Root        string           `yaml:"root"`
	MainConf    string           `yaml:"main_conf"`
	SitesDir    string           `yaml:"sites_dir"`
	Bin         string           `yaml:"bin"`
	ServiceName string           `yaml:"service_name"`
	CacheRoot   string           `yaml:"cache_root"`
	User        string           `yaml:"user"`
	Group       string           `yaml:"group"`
	Apply       NginxApplyConfig `yaml:"apply"`
}

type NginxApplyConfig struct {
	StagingDir       string `yaml:"staging_dir"`
	BackupDir        string `yaml:"backup_dir"`
	TestBeforeReload bool   `yaml:"test_before_reload"`
	ReloadMode       string `yaml:"reload_mode"` // "signal" or "systemd"
}

type CertsConfig struct {
	Mode            string `yaml:"mode"` // "certbot" (MVP)
	Email           string `yaml:"email"`
	Webroot         string `yaml:"webroot"`
	LetsEncryptLive string `yaml:"letsencrypt_live"`
	CertbotBin      string `yaml:"certbot_bin"`
}

type PHPFPMConfig struct {
	DefaultVersion string                   `yaml:"default_version"`
	Versions       map[string]PHPFPMVersion `yaml:"versions"`
}

type PHPFPMVersion struct {
	PoolsDir string `yaml:"pools_dir"`
	Service  string `yaml:"service"`
	SockDir  string `yaml:"sock_dir"`
}

type HostingConfig struct {
	HomeRoot      string `yaml:"home_root"`
	SitesRootName string `yaml:"sites_root_name"`
	WebGroup      string `yaml:"web_group"`
}

type SecurityConfig struct {
	AuditLog string `yaml:"audit_log"`
}

type StorageConfig struct {
	Driver     string `yaml:"driver"`
	DSN        string `yaml:"dsn"`
	SQLitePath string `yaml:"sqlite_path"`
}

type DNSConfig struct {
	Enabled         bool                         `yaml:"enabled"`
	Provider        string                       `yaml:"provider"`
	DefaultTemplate string                       `yaml:"default_template"`
	DefaultIPv4     string                       `yaml:"default_ipv4"`
	DefaultIPv6     string                       `yaml:"default_ipv6"`
	Bind            DNSBindConfig                `yaml:"bind"`
	Templates       map[string]DNSTemplateConfig `yaml:"templates"`
}

type DNSBindConfig struct {
	NamedConfInclude string `yaml:"named_conf_include"`
	ZonesDir         string `yaml:"zones_dir"`
	RNDCBin          string `yaml:"rndc_bin"`
	CheckConfBin     string `yaml:"checkconf_bin"`
	CheckZoneBin     string `yaml:"checkzone_bin"`
	NamedConfPath    string `yaml:"named_conf_path"`
	ZoneFileSuffix   string `yaml:"zone_file_suffix"`
}

type DNSTemplateConfig struct {
	TTL         uint32              `yaml:"ttl"`
	SOA         DNSSOAConfig        `yaml:"soa"`
	Nameservers []string            `yaml:"nameservers"`
	Records     []DNSRecordTemplate `yaml:"records"`
}

type DNSSOAConfig struct {
	MName   string `yaml:"mname"`
	RName   string `yaml:"rname"`
	Refresh uint32 `yaml:"refresh"`
	Retry   uint32 `yaml:"retry"`
	Expire  uint32 `yaml:"expire"`
	Minimum uint32 `yaml:"minimum"`
}

type DNSRecordTemplate struct {
	Name  string `yaml:"name"`
	Type  string `yaml:"type"`
	Value string `yaml:"value"`
	TTL   uint32 `yaml:"ttl"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // catch typos in YAML
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse yaml %q: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	// API
	if c.API.Listen == "" {
		c.API.Listen = "127.0.0.1:9601"
	}

	// Nginx
	if c.Nginx.MainConf == "" {
		c.Nginx.MainConf = "conf/nginx.conf"
	}
	if c.Nginx.SitesDir == "" {
		c.Nginx.SitesDir = "conf/sites"
	}
	if c.Nginx.Bin == "" {
		c.Nginx.Bin = "sbin/nginx"
	}
	if c.Nginx.ServiceName == "" {
		c.Nginx.ServiceName = "nginx"
	}
	if c.Nginx.CacheRoot == "" {
		c.Nginx.CacheRoot = "/var/cache/nginx"
	}
	if c.Nginx.User == "" {
		c.Nginx.User = "www-data"
	}
	if c.Nginx.Group == "" {
		c.Nginx.Group = "nobody"
	}
	if c.Nginx.Apply.StagingDir == "" {
		c.Nginx.Apply.StagingDir = "conf/.staging"
	}
	if c.Nginx.Apply.BackupDir == "" {
		c.Nginx.Apply.BackupDir = "conf/.backup"
	}
	// default true
	if !c.Nginx.Apply.TestBeforeReload {
		c.Nginx.Apply.TestBeforeReload = true
	}
	if c.Nginx.Apply.ReloadMode == "" {
		c.Nginx.Apply.ReloadMode = "signal"
		if st, err := os.Stat("/run/systemd/system"); err == nil && st.IsDir() {
			c.Nginx.Apply.ReloadMode = "systemd"
		}
	}

	// Certs
	if c.Certs.Mode == "" {
		c.Certs.Mode = "certbot"
	}
	if c.Certs.CertbotBin == "" {
		c.Certs.CertbotBin = "certbot"
	}

	// PHP-FPM
	if c.PHPFPM.DefaultVersion == "" {
		c.PHPFPM.DefaultVersion = "8.4"
	}
	if c.PHPFPM.Versions == nil {
		c.PHPFPM.Versions = map[string]PHPFPMVersion{}
	}

	// Hosting
	if c.Hosting.HomeRoot == "" {
		c.Hosting.HomeRoot = "/home"
	}
	if c.Hosting.SitesRootName == "" {
		c.Hosting.SitesRootName = "sites"
	}
	if c.Hosting.WebGroup == "" {
		c.Hosting.WebGroup = "www-data"
	}

	// Storage
	if c.Storage.Driver == "" {
		c.Storage.Driver = "sqlite"
	}
	if c.Storage.SQLitePath == "" {
		c.Storage.SQLitePath = "/var/lib/ngm/ngm.db"
	}
	if c.Storage.DSN == "" {
		if c.Storage.SQLitePath != "" {
			c.Storage.DSN = c.Storage.SQLitePath
		} else {
			c.Storage.DSN = "/var/lib/ngm/ngm.db"
		}
	}
	// Security
	if c.Security.AuditLog == "" {
		c.Security.AuditLog = "/var/log/ngm/audit.log"
	}
	// DNS
	if c.DNS.Provider == "" {
		c.DNS.Provider = "bind"
	}
	if c.DNS.DefaultTemplate == "" {
		c.DNS.DefaultTemplate = "default"
	}
	if c.DNS.Bind.NamedConfInclude == "" {
		c.DNS.Bind.NamedConfInclude = "/etc/named/ngm-zones.conf"
	}
	if c.DNS.Bind.ZonesDir == "" {
		c.DNS.Bind.ZonesDir = "/var/named/ngm"
	}
	if c.DNS.Bind.RNDCBin == "" {
		c.DNS.Bind.RNDCBin = "rndc"
	}
	if c.DNS.Bind.CheckConfBin == "" {
		c.DNS.Bind.CheckConfBin = "named-checkconf"
	}
	if c.DNS.Bind.CheckZoneBin == "" {
		c.DNS.Bind.CheckZoneBin = "named-checkzone"
	}
	if c.DNS.Bind.NamedConfPath == "" {
		c.DNS.Bind.NamedConfPath = "/etc/named.conf"
	}
	if c.DNS.Bind.ZoneFileSuffix == "" {
		c.DNS.Bind.ZoneFileSuffix = ".zone"
	}
	if c.DNS.Templates == nil {
		c.DNS.Templates = map[string]DNSTemplateConfig{}
	}
	if _, ok := c.DNS.Templates[c.DNS.DefaultTemplate]; !ok {
		c.DNS.Templates[c.DNS.DefaultTemplate] = DNSTemplateConfig{
			TTL: 3600,
			SOA: DNSSOAConfig{
				MName:   "ns1.example.net.",
				RName:   "hostmaster.example.net.",
				Refresh: 3600,
				Retry:   900,
				Expire:  1209600,
				Minimum: 3600,
			},
			Nameservers: []string{"ns1.example.net.", "ns2.example.net."},
			Records: []DNSRecordTemplate{
				{Name: "www", Type: "CNAME", Value: "@", TTL: 3600},
			},
		}
	}
}

// validate
func (c *Config) Validate() error {
	var errs []string

	// Nginx basics
	if strings.TrimSpace(c.Nginx.Root) == "" {
		errs = append(errs, "nginx.root is required (e.g. /opt/nginx)")
	}
	if strings.TrimSpace(c.Nginx.Apply.ReloadMode) != "" {
		mode := strings.ToLower(strings.TrimSpace(c.Nginx.Apply.ReloadMode))
		if mode != "signal" && mode != "systemd" {
			errs = append(errs, fmt.Sprintf("nginx.apply.reload_mode=%q unsupported (expected 'signal' or 'systemd')", c.Nginx.Apply.ReloadMode))
		}
	}

	// API auth basics
	if len(c.API.Tokens) == 0 {
		errs = append(errs, "api.tokens must contain at least one token")
	}
	for i, t := range c.API.Tokens {
		if strings.TrimSpace(t) == "" {
			errs = append(errs, fmt.Sprintf("api.tokens[%d] is empty", i))
		}
	}

	// Allowlist CIDRs (optional but recommended)
	for i, cidr := range c.API.AllowIPs {
		if strings.TrimSpace(cidr) == "" {
			errs = append(errs, fmt.Sprintf("api.allow_ips[%d] is empty", i))
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			errs = append(errs, fmt.Sprintf("api.allow_ips[%d]=%q invalid CIDR: %v", i, cidr, err))
		}
	}

	// Certs
	if c.Certs.Mode != "" && c.Certs.Mode != "certbot" {
		errs = append(errs, fmt.Sprintf("certs.mode=%q unsupported (MVP supports only 'certbot')", c.Certs.Mode))
	}
	if strings.TrimSpace(c.Certs.Webroot) == "" {
		errs = append(errs, "certs.webroot is required (e.g. /opt/nginx/html)")
	}
	if strings.TrimSpace(c.Certs.LetsEncryptLive) == "" {
		errs = append(errs, "certs.letsencrypt_live is required (e.g. /etc/letsencrypt/live)")
	}

	// PHP versions map (optional, but if present must be consistent)
	if c.PHPFPM.DefaultVersion != "" {
		if _, ok := c.PHPFPM.Versions[c.PHPFPM.DefaultVersion]; !ok && len(c.PHPFPM.Versions) > 0 {
			errs = append(errs, fmt.Sprintf("phpfpm.default_version=%q not found in phpfpm.versions map", c.PHPFPM.DefaultVersion))
		}
	}
	for ver, v := range c.PHPFPM.Versions {
		if strings.TrimSpace(v.PoolsDir) == "" {
			errs = append(errs, fmt.Sprintf("phpfpm.versions[%q].pools_dir is required", ver))
		}
		if strings.TrimSpace(v.Service) == "" {
			errs = append(errs, fmt.Sprintf("phpfpm.versions[%q].service is required", ver))
		}
		if strings.TrimSpace(v.SockDir) == "" {
			errs = append(errs, fmt.Sprintf("phpfpm.versions[%q].sock_dir is required", ver))
		}
	}
	if c.DNS.Enabled {
		if strings.TrimSpace(c.DNS.DefaultTemplate) == "" {
			errs = append(errs, "dns.default_template is required when dns.enabled=true")
		}
		if _, ok := c.DNS.Templates[c.DNS.DefaultTemplate]; !ok {
			errs = append(errs, fmt.Sprintf("dns.default_template=%q not found in dns.templates", c.DNS.DefaultTemplate))
		}
		if strings.TrimSpace(c.DNS.Bind.NamedConfInclude) == "" {
			errs = append(errs, "dns.bind.named_conf_include is required when dns.enabled=true")
		}
		if strings.TrimSpace(c.DNS.Bind.ZonesDir) == "" {
			errs = append(errs, "dns.bind.zones_dir is required when dns.enabled=true")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("config validation failed:\n- %s", strings.Join(errs, "\n- "))
	}
	return nil
}

//validate end

// paths//
type Paths struct {
	// Nginx
	NginxRoot                string
	NginxBin                 string
	NginxMainConf            string
	NginxSitesDir            string
	NginxStageDir            string
	NginxBackupDir           string
	NginxCacheRoot           string
	NginxPHPFastCGICacheDir  string
	NginxProxyMicroCacheDir  string
	NginxProxyStaticCacheDir string

	// Certs
	CertbotBin      string
	ACMEWebroot     string
	LetsEncryptLive string
}

func (c *Config) ResolvePaths() Paths {
	root := c.Nginx.Root

	return Paths{
		NginxRoot:                root,
		NginxBin:                 absOrJoin(root, c.Nginx.Bin),
		NginxMainConf:            absOrJoin(root, c.Nginx.MainConf),
		NginxSitesDir:            absOrJoin(root, c.Nginx.SitesDir),
		NginxStageDir:            absOrJoin(root, c.Nginx.Apply.StagingDir),
		NginxBackupDir:           absOrJoin(root, c.Nginx.Apply.BackupDir),
		NginxCacheRoot:           c.Nginx.CacheRoot,
		NginxPHPFastCGICacheDir:  filepath.Join(c.Nginx.CacheRoot, "php"),
		NginxProxyMicroCacheDir:  filepath.Join(c.Nginx.CacheRoot, "proxy_micro"),
		NginxProxyStaticCacheDir: filepath.Join(c.Nginx.CacheRoot, "proxy_static"),

		CertbotBin:      c.Certs.CertbotBin, // can be PATH lookup
		ACMEWebroot:     c.Certs.Webroot,
		LetsEncryptLive: c.Certs.LetsEncryptLive,
	}
}

func absOrJoin(root, p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

//paths end//
