package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mynginx/internal/fpm"
	"mynginx/internal/nginx"
	"mynginx/internal/store"
)

func (a *App) buildTemplateData(s store.Site, domain string, proxyLister proxyTargetLister) (nginx.SiteTemplateData, error) {
	paths := a.paths
	cfg := a.cfg

	siteRoot := filepath.Dir(s.Webroot)
	logsDir := filepath.Join(siteRoot, "logs")

	phpPass := ""
	if s.Mode == "" || s.Mode == "php" {
		ver, ok := cfg.PHPFPM.Versions[s.PHPVersion]
		if !ok {
			return nginx.SiteTemplateData{}, fmt.Errorf("unknown php version %q (not in config.phpfpm.versions)", s.PHPVersion)
		}

		runUser, ok := inferUserFromWebroot(cfg.Hosting.HomeRoot, s.Webroot)
		if !ok {
			return nginx.SiteTemplateData{}, fmt.Errorf("cannot infer site user from webroot %q (expected under %q)", s.Webroot, cfg.Hosting.HomeRoot)
		}
		runGroup := runUser
		webGroup := cfg.Hosting.WebGroup
		if webGroup == "" {
			webGroup = "www-data"
		}

		phpSock := fpm.SocketPath(ver.SockDir, domain, s.PHPVersion)

		poolTD := fpm.PoolData{
			PoolName:                "ngm_" + strings.ReplaceAll(domain, ".", "_"),
			RunUser:                 runUser,
			RunGroup:                runGroup,
			Socket:                  phpSock,
			ListenOwner:             runUser,
			ListenGroup:             webGroup,
			MaxChildren:             10,
			IdleTimeout:             "10s",
			MaxRequests:             500,
			RequestTerminateTimeout: "60s",
			SlowlogTimeout:          "5s",
			SlowlogPath:             filepath.Join(logsDir, "php-fpm.slow.log"),
			ErrorLog:                filepath.Join(logsDir, "php-fpm.error.log"),
			PHPAdminValues:          map[string]string{},
			PHPValues:               map[string]string{},
		}

		if _, _, err := fpm.EnsurePool(ver.PoolsDir, ver.Service, ver.SockDir, domain, s.PHPVersion, poolTD); err != nil {
			return nginx.SiteTemplateData{}, fmt.Errorf("ensure fpm pool: %w", err)
		}

		phpPass = "unix:" + phpSock
	}

	leCert := filepath.Join(paths.LetsEncryptLive, domain, "fullchain.pem")
	leKey := filepath.Join(paths.LetsEncryptLive, domain, "privkey.pem")

	tlsCert := leCert
	tlsKey := leKey

	if !fileExists(leCert) || !fileExists(leKey) {
		selfSignedRoot := filepath.Join(paths.NginxRoot, "conf", "selfsigned")
		fbCert := filepath.Join(selfSignedRoot, domain, "fullchain.pem")
		fbKey := filepath.Join(selfSignedRoot, domain, "privkey.pem")
		if err := ensureSelfSignedCert(domain, fbCert, fbKey); err != nil {
			return nginx.SiteTemplateData{}, err
		}
		tlsCert = fbCert
		tlsKey = fbKey
	}

	td := nginx.SiteTemplateData{
		Domain:          domain,
		Mode:            s.Mode,
		Webroot:         s.Webroot,
		ACMEWebroot:     paths.ACMEWebroot,
		EnableHTTP3:     s.EnableHTTP3,
		TLSCert:         tlsCert,
		TLSKey:          tlsKey,
		FrontController: true,
		AccessLog:       filepath.Join(logsDir, "access.log"),
		ErrorLog:        filepath.Join(logsDir, "error.log"),
	}


// Defaults so template never renders empty directives.
// (Empty in DB means "use defaults".)
	clientMax := strings.TrimSpace(s.ClientMaxBodySize)
	if clientMax == "" {
		clientMax = "32M"
	}
	td.ClientMaxBodySize = clientMax

	phpRead := strings.TrimSpace(s.PHPTimeRead)
	if phpRead == "" {
		phpRead = "60s"
	}
	phpSend := strings.TrimSpace(s.PHPTimeSend)
	if phpSend == "" {
		phpSend = "60s"
	}



	if s.Mode == "" || s.Mode == "php" {
		td.PHP = nginx.FastCGICfg{
			Pass: phpPass,
			TimeRead: phpRead,
			TimeSend: phpSend,
			Cache: nginx.CacheCfg{
				Enabled: true,
				Zone:    "php_cache",
				TTL200:  "15s",
			},
		}
	}

	if s.Mode == "proxy" {
		td.Proxy = nginx.ProxyCfg{
			LB:          "least_conn",
			PassHost:    true,
			Websockets:  false,
			TimeConnect: "3s",
			TimeRead:    "60s",
			TimeSend:    "60s",
			Microcache: nginx.CacheCfg{
				Enabled: true,
				Zone:    "proxy_micro",
				TTL200:  "15s",
			},
			StaticCache: nginx.CacheCfg{
				Enabled: true,
				Zone:    "proxy_static",
				TTL200:  "30d",
			},
		}

		if proxyLister == nil {
			return nginx.SiteTemplateData{}, fmt.Errorf("proxy mode requires sqlite store (to load proxy targets)")
		}
		targets, err := proxyLister.ListProxyTargetsBySiteID(s.ID)
		if err != nil {
			return nginx.SiteTemplateData{}, fmt.Errorf("load proxy targets: %w", err)
		}
		if len(targets) == 0 {
			return nginx.SiteTemplateData{}, fmt.Errorf("proxy mode requires at least 1 proxy target for %s", domain)
		}
		td.Proxy.Targets = targets
	}

	return td, nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func ensureSelfSignedCert(domain, certPath, keyPath string) error {
	if fileExists(certPath) && fileExists(keyPath) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		return fmt.Errorf("mkdir cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return fmt.Errorf("mkdir key dir: %w", err)
	}

	cmd := exec.Command(
		"openssl", "req",
		"-x509",
		"-nodes",
		"-newkey", "rsa:2048",
		"-days", "7",
		"-subj", "/CN="+domain,
		"-keyout", keyPath,
		"-out", certPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate self-signed cert failed: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}

	_ = os.Chmod(certPath, 0644)
	_ = os.Chmod(keyPath, 0600)
	return nil
}

func inferUserFromWebroot(homeRoot, webroot string) (string, bool) {
	homeRoot = strings.TrimRight(homeRoot, "/")
	if homeRoot == "" {
		return "", false
	}
	prefix := homeRoot + "/"
	if !strings.HasPrefix(webroot, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(webroot, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || parts[0] == "" {
		return "", false
	}
	return parts[0], true
}
