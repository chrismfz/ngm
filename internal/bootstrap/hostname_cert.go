package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"mynginx/internal/certs"
	"mynginx/internal/config"
)

func DetectProvisionHostname(cfg *config.Config) string {
	h := strings.TrimSpace(cfg.API.Hostname)
	if h != "" {
		return strings.ToLower(h)
	}
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(host))
}

func HostnameLooksPublicFQDN(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	if !strings.Contains(host, ".") {
		return false
	}
	for _, suffix := range []string{".local", ".localdomain", ".lan", ".internal", ".home", ".test"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

func HostnameHasPublicDNS(host string) (bool, []string, error) {
	ips, err := net.LookupIP(host)
	if err != nil {
		return false, nil, err
	}
	pub := make([]string, 0, len(ips))
	for _, ip := range ips {
		if isPublicIP(ip) {
			pub = append(pub, ip.String())
		}
	}
	return len(pub) > 0, pub, nil
}

func EnsureHostnameCert(ctx context.Context, cfg *config.Config, paths config.Paths, host string) (certPath string, keyPath string, usedRealCert bool, err error) {
	mgr := certs.NewCertbotManager(paths.CertbotBin, paths.ACMEWebroot, paths.LetsEncryptLive, cfg.Certs.Email)

	certPath = filepath.Join(paths.LetsEncryptLive, host, "fullchain.pem")
	keyPath = filepath.Join(paths.LetsEncryptLive, host, "privkey.pem")
	if fileExists(certPath) && fileExists(keyPath) {
		return certPath, keyPath, true, nil
	}

	if err := mgr.IssueCert(ctx, host); err != nil {
		return "", "", false, fmt.Errorf("issue hostname cert: %w", err)
	}
	if !fileExists(certPath) || !fileExists(keyPath) {
		return "", "", false, fmt.Errorf("hostname cert issuance did not produce expected files")
	}
	return certPath, keyPath, true, nil
}

func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() {
		return false
	}
	if ip.To16() != nil && strings.HasPrefix(strings.ToLower(ip.String()), "fc") {
		return false
	}
	return true
}
