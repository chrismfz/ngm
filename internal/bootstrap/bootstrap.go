package bootstrap

import (
	"context"
	"fmt"
	"time"

	"mynginx/internal/config"
	"mynginx/internal/nginx"
)

func Init(cfg *config.Config, paths config.Paths) error {
	mgr := nginx.NewManager(paths.NginxRoot, paths.NginxBin, paths.NginxMainConf, paths.NginxSitesDir, paths.NginxStageDir, paths.NginxBackupDir)
	if err := mgr.EnsureLayout(); err != nil {
		return fmt.Errorf("ensure nginx layout: %w", err)
	}

	certPath, keyPath, err := EnsureGlobalSelfSigned(cfg, paths)
	if err != nil {
		return err
	}
	if err := InstallMasterConfig(cfg, paths, certPath, keyPath); err != nil {
		return err
	}
	if err := mgr.TestConfig(); err != nil {
		return err
	}

	activeType := "self-signed fallback"
	candidate := DetectProvisionHostname(cfg)
	if HostnameLooksPublicFQDN(candidate) {
		hasPublicDNS, ips, dnsErr := HostnameHasPublicDNS(candidate)
		if dnsErr != nil {
			fmt.Printf("WARN: hostname cert skipped for %q: DNS lookup failed: %v\n", candidate, dnsErr)
		} else if !hasPublicDNS {
			fmt.Printf("INFO: hostname cert skipped for %q: only non-public DNS records found\n", candidate)
		} else {
			fmt.Printf("INFO: attempting hostname cert for %q (public DNS: %v)\n", candidate, ips)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			leCert, leKey, ok, certErr := EnsureHostnameCert(ctx, cfg, paths, candidate)
			if certErr != nil {
				fmt.Printf("WARN: hostname cert attempt failed for %q: %v\n", candidate, certErr)
			} else if ok {
				if err := InstallMasterConfig(cfg, paths, leCert, leKey); err != nil {
					return fmt.Errorf("install master nginx config with hostname cert: %w", err)
				}
				if err := mgr.TestConfig(); err != nil {
					return fmt.Errorf("nginx -t after hostname cert switch: %w", err)
				}
				activeType = "Let's Encrypt hostname cert"
			}
		}
	} else if candidate != "" {
		fmt.Printf("INFO: hostname cert skipped for %q: not a public FQDN\n", candidate)
	}

	fmt.Printf("Provision init complete: active global listener cert = %s\n", activeType)
	fmt.Printf("Master config: %s\n", paths.NginxMainConf)
	return nil
}

func Test(cfg *config.Config, paths config.Paths) error {
	mgr := nginx.NewManager(paths.NginxRoot, paths.NginxBin, paths.NginxMainConf, paths.NginxSitesDir, paths.NginxStageDir, paths.NginxBackupDir)
	if err := mgr.EnsureLayout(); err != nil {
		return fmt.Errorf("ensure nginx layout: %w", err)
	}
	if err := mgr.TestConfig(); err != nil {
		return err
	}
	fmt.Printf("OK: nginx config test passed (%s -t -c %s)\n", paths.NginxBin, paths.NginxMainConf)
	return nil
}
