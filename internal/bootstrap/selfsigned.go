package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"mynginx/internal/config"
)

func EnsureGlobalSelfSigned(cfg *config.Config, paths config.Paths) (certPath string, keyPath string, err error) {
	certDir := filepath.Join(paths.NginxRoot, "conf", "selfsigned")
	certPath = filepath.Join(certDir, "fullchain.pem")
	keyPath = filepath.Join(certDir, "privkey.pem")

	if fileExists(certPath) && fileExists(keyPath) {
		return certPath, keyPath, nil
	}

	if err := os.MkdirAll(certDir, 0750); err != nil {
		return "", "", fmt.Errorf("mkdir %s: %w", certDir, err)
	}

	if err := chmodAndChownCertDir(certDir, cfg.Nginx.Group); err != nil {
		return "", "", err
	}

	cmd := exec.Command(
		"openssl", "req",
		"-x509",
		"-nodes",
		"-newkey", "ec",
		"-pkeyopt", "ec_paramgen_curve:P-256",
		"-days", "3650",
		"-subj", "/CN=localhost",
		"-addext", "subjectAltName=DNS:localhost",
		"-keyout", keyPath,
		"-out", certPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("generate global self-signed cert failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	if err := os.Chmod(certPath, 0644); err != nil {
		return "", "", fmt.Errorf("chmod cert %s: %w", certPath, err)
	}
	if err := os.Chmod(keyPath, 0640); err != nil {
		return "", "", fmt.Errorf("chmod key %s: %w", keyPath, err)
	}
	if err := chownByGroup(certPath, cfg.Nginx.Group); err != nil {
		return "", "", err
	}
	if err := chownByGroup(keyPath, cfg.Nginx.Group); err != nil {
		return "", "", err
	}

	return certPath, keyPath, nil
}

func chmodAndChownCertDir(dir, group string) error {
	if err := os.Chmod(dir, 0750); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return chownByGroup(dir, group)
}

func chownByGroup(path, group string) error {
	if strings.TrimSpace(group) == "" {
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("%s requires root to set owner/group on %s", group, path)
	}
	g, err := lookupGroupGID(group)
	if err != nil {
		return err
	}
	if err := os.Chown(path, 0, int(g)); err != nil {
		return fmt.Errorf("chown %s root:%s: %w", path, group, err)
	}
	return nil
}

func lookupGroupGID(group string) (uint32, error) {
	raw, err := os.ReadFile("/etc/group")
	if err != nil {
		return 0, fmt.Errorf("read /etc/group: %w", err)
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		parts := strings.Split(ln, ":")
		if len(parts) < 3 || parts[0] != group {
			continue
		}
		gid, err := strconv.Atoi(parts[2])
		if err != nil {
			return 0, fmt.Errorf("parse gid for %q: %w", group, err)
		}
		return uint32(gid), nil
	}
	return 0, fmt.Errorf("group %q not found in /etc/group", group)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err == syscall.EACCES || pe.Err == syscall.EPERM
	}
	return false
}
