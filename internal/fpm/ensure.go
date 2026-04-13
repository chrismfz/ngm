package fpm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mynginx/internal/util"
)

var nonIdent = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func makeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = nonIdent.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "site"
	}
	return s
}

func SocketPath(sockDir, domain, phpVersion string) string {
	key := makeKey(domain)
	// /run/php/ngm-example_com-8.3.sock
	return filepath.Join(sockDir, fmt.Sprintf("ngm-%s-%s.sock", key, phpVersion))
}

func PoolFilePath(poolsDir, domain string) string {
	key := makeKey(domain)
	return filepath.Join(poolsDir, fmt.Sprintf("ngm-%s.conf", key))
}

// EnsurePool renders a pool file and reloads the php-fpm service only if the content changes.
// Returns (socketPath, changed, err).
func EnsurePool(poolsDir, service, sockDir, domain, phpVersion string, td PoolData) (string, bool, error) {
	if domain == "" {
		return "", false, fmt.Errorf("domain required")
	}
	if poolsDir == "" || service == "" || sockDir == "" || phpVersion == "" {
		return "", false, fmt.Errorf("poolsDir/service/sockDir/phpVersion required")
	}

	// Always use deterministic per-domain socket
	td.Socket = SocketPath(sockDir, domain, phpVersion)

	// Ensure dirs exist for logs/slowlogs (php-fpm will create files, but directory must exist)
	if td.ErrorLog != "" {
		_ = util.MkdirAll(filepath.Dir(td.ErrorLog), 0755)
	}
	if td.SlowlogPath != "" {
		_ = util.MkdirAll(filepath.Dir(td.SlowlogPath), 0755)
	}

	pm := &PoolManager{} // uses embedded default template
	rendered, err := pm.Render(td)
	if err != nil {
		return "", false, err
	}

	outPath := PoolFilePath(poolsDir, domain)
	_ = util.MkdirAll(filepath.Dir(outPath), 0755)

	if old, err := os.ReadFile(outPath); err == nil {
		if bytes.Equal(old, rendered) {
			return td.Socket, false, nil
		}
	}

	// Write new pool conf
	if err := writePoolFileAtomic(outPath, rendered); err != nil {
		return "", false, fmt.Errorf("write pool %s: %w", outPath, err)
	}

	// Reload php-fpm so it picks up pool changes
	if err := ReloadService(service); err != nil {
		return "", true, err
	}
	return td.Socket, true, nil
}
