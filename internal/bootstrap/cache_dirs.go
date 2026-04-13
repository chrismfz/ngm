package bootstrap

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"mynginx/internal/config"
)

const nginxCacheDirMode = 0750

func EnsureNginxCacheDirs(cfg *config.Config, paths config.Paths) error {
	cacheDirs := []string{
		paths.NginxCacheRoot,
		paths.NginxPHPFastCGICacheDir,
		paths.NginxProxyMicroCacheDir,
		paths.NginxProxyStaticCacheDir,
	}

	for _, dir := range cacheDirs {
		if err := os.MkdirAll(dir, nginxCacheDirMode); err != nil {
			return fmt.Errorf("mkdir cache dir %s: %w", dir, err)
		}
		if err := os.Chmod(dir, nginxCacheDirMode); err != nil {
			return fmt.Errorf("chmod cache dir %s: %w", dir, err)
		}
	}

	if os.Geteuid() != 0 {
		return fmt.Errorf("provision requires root to set cache directory ownership to %s:%s", cfg.Nginx.User, cfg.Nginx.Group)
	}

	uid, err := lookupUserUID(cfg.Nginx.User)
	if err != nil {
		return err
	}
	gid, err := lookupGroupGID(cfg.Nginx.Group)
	if err != nil {
		return err
	}

	for _, dir := range cacheDirs {
		if err := os.Chown(dir, int(uid), int(gid)); err != nil {
			return fmt.Errorf("chown cache dir %s to %s:%s: %w", dir, cfg.Nginx.User, cfg.Nginx.Group, err)
		}
	}

	return nil
}

func lookupUserUID(username string) (uint32, error) {
	raw, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return 0, fmt.Errorf("read /etc/passwd: %w", err)
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		parts := strings.Split(ln, ":")
		if len(parts) < 3 || parts[0] != username {
			continue
		}
		uid, err := strconv.Atoi(parts[2])
		if err != nil {
			return 0, fmt.Errorf("parse uid for %q: %w", username, err)
		}
		return uint32(uid), nil
	}
	return 0, fmt.Errorf("user %q not found in /etc/passwd", username)
}
