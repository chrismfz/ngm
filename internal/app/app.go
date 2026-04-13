package app

import (
	"fmt"
	"sync"

	"mynginx/internal/config"
	appdns "mynginx/internal/dns"
	bindprovider "mynginx/internal/dns/bind"
	"mynginx/internal/nginx"
	"mynginx/internal/store"
)

// App wires core business logic used by CLI/API/UI.
// Keep it transport-agnostic (no net/http, no templates, no flag parsing).
type App struct {
	cfg   *config.Config
	paths config.Paths
	st    store.SiteStore
	ng    *nginx.Manager
	dns   appdns.Provider

	applyMu sync.Mutex
}

func New(cfg *config.Config, paths config.Paths, st store.SiteStore) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cfg is nil")
	}
	if st == nil {
		return nil, fmt.Errorf("store is nil")
	}

	mgr := nginx.NewManager(
		paths.NginxRoot,
		paths.NginxBin,
		paths.NginxMainConf,
		paths.NginxSitesDir,
		paths.NginxStageDir,
		paths.NginxBackupDir,
	)
	mgr.SetControlMode(cfg.Nginx.Apply.ReloadMode, cfg.Nginx.ServiceName)
	if err := mgr.EnsureLayout(); err != nil {
		return nil, fmt.Errorf("nginx layout: %w", err)
	}

	var dnsProvider appdns.Provider
	if cfg.DNS.Enabled {
		switch cfg.DNS.Provider {
		case "", "bind":
			p, err := bindprovider.New(cfg.DNS)
			if err != nil {
				return nil, fmt.Errorf("dns provider init: %w", err)
			}
			dnsProvider = p
		default:
			return nil, fmt.Errorf("unsupported dns.provider %q", cfg.DNS.Provider)
		}
	}

	return &App{cfg: cfg, paths: paths, st: st, ng: mgr, dns: dnsProvider}, nil
}
