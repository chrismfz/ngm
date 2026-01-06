package users

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type SiteDirs struct {
	SiteRoot string
	Public   string
	Logs     string
	Tmp      string
	PHP      string
}

// EnsureSystemUser ensures the Linux user exists. If missing, it will create it (root required).
func EnsureSystemUser(username, homeDir string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("username is empty")
	}
	if homeDir == "" {
		return fmt.Errorf("homeDir is empty")
	}
	if userExists(username) {
		return nil
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("linux user %q does not exist; run as root to create it", username)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "useradd", "-m", "-d", homeDir, "-s", "/bin/bash", username)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("useradd failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureHomeTraversal sets /home/<user> perms so nginx worker group can traverse into it.
// We use: chgrp webGroup + chmod 0710 (group-exec only).
func EnsureHomeTraversal(username, homeDir, webGroup string) error {
	if os.Geteuid() != 0 {
		// best-effort: non-root can't chown/chmod reliably
		return nil
	}

	uid, _, ok := lookupUserUIDGID(username)
	if !ok {
		return fmt.Errorf("cannot find uid for user %q in /etc/passwd", username)
	}

	gid := uint32(0)
	if g, ok := lookupGroupGID(webGroup); ok {
		gid = g
	} else {
		// fallback to user's own gid if webGroup not found
		_, ugid, ok := lookupUserUIDGID(username)
		if ok {
			gid = ugid
		}
	}

	// group = webGroup, mode 0710 so group can traverse but not list
	_ = os.Chown(homeDir, int(uid), int(gid))
	_ = os.Chmod(homeDir, 0710)

	// Also ensure the "sites" container exists and is traversable by group
	sitesBase := filepath.Join(homeDir, "sites")
	_ = os.MkdirAll(sitesBase, 0750)
	_ = os.Chown(sitesBase, int(uid), int(gid))
	_ = os.Chmod(sitesBase, 0750)

	return nil
}

// EnsureSiteDirs creates the site layout around webroot:
//   <siteRoot>/public (webroot)
//   <siteRoot>/logs (+ access.log/error.log)
//   <siteRoot>/tmp
//   <siteRoot>/php
//
// Ownership model (root run):
//   owner: username
//   group: webGroup (e.g. www-data)
// Permissions:
//   dirs: 0750, files: 0640
func EnsureSiteDirs(username, homeDir, webroot, webGroup string) (SiteDirs, error) {
	webroot = filepath.Clean(strings.TrimSpace(webroot))
	if webroot == "" || webroot == "/" {
		return SiteDirs{}, fmt.Errorf("invalid webroot %q", webroot)
	}

	// Make sure /home/<user> is traversable for nginx group
	_ = EnsureHomeTraversal(username, homeDir, webGroup)

	siteRoot := filepath.Dir(webroot)
	dirs := SiteDirs{
		SiteRoot: siteRoot,
		Public:   webroot,
		Logs:     filepath.Join(siteRoot, "logs"),
		Tmp:      filepath.Join(siteRoot, "tmp"),
		PHP:      filepath.Join(siteRoot, "php"),
	}

	// Create dirs
	for _, d := range []string{dirs.SiteRoot, dirs.Public, dirs.Logs, dirs.Tmp, dirs.PHP} {
		if err := os.MkdirAll(d, 0750); err != nil {
			return SiteDirs{}, fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Create log files (so nginx can open them immediately)
	_ = touchFile(filepath.Join(dirs.Logs, "access.log"), 0640)
	_ = touchFile(filepath.Join(dirs.Logs, "error.log"), 0640)

	// Ownership
	if os.Geteuid() == 0 {
		uid, ugid, ok := lookupUserUIDGID(username)
		if !ok {
			return SiteDirs{}, fmt.Errorf("cannot find user %q in /etc/passwd", username)
		}

		gid := ugid
		if g, ok := lookupGroupGID(webGroup); ok {
			gid = g
		}

		// Ensure parent container (/home/<user>/sites) too
		sitesBase := filepath.Dir(dirs.SiteRoot) // .../sites
		_ = os.MkdirAll(sitesBase, 0750)
		_ = os.Chown(sitesBase, int(uid), int(gid))
		_ = os.Chmod(sitesBase, 0750)

		// Chown entire siteRoot tree to user:webGroup
		_ = chownR(dirs.SiteRoot, int(uid), int(gid))
		_ = chmodR(dirs.SiteRoot, 0750, 0640)
	}

	return dirs, nil
}

func userExists(username string) bool {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return false
	}
	defer f.Close()

	prefix := username + ":"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), prefix) {
			return true
		}
	}
	return false
}

func lookupUserUIDGID(username string) (uint32, uint32, bool) {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()

	prefix := username + ":"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			return 0, 0, false
		}
		uid64, err1 := strconv.ParseUint(parts[2], 10, 32)
		gid64, err2 := strconv.ParseUint(parts[3], 10, 32)
		if err1 != nil || err2 != nil {
			return 0, 0, false
		}
		return uint32(uid64), uint32(gid64), true
	}
	return 0, 0, false
}

func lookupGroupGID(group string) (uint32, bool) {
	group = strings.TrimSpace(group)
	if group == "" {
		return 0, false
	}
	f, err := os.Open("/etc/group")
	if err != nil {
		return 0, false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 3 {
			continue
		}
		if parts[0] == group {
			gid64, err := strconv.ParseUint(parts[2], 10, 32)
			if err == nil {
				return uint32(gid64), true
			}
		}
	}
	return 0, false
}

func ownerOfNearestExisting(path string) (uint32, uint32, error) {
	p := path
	for {
		st, err := os.Stat(p)
		if err == nil {
			if sys, ok := st.Sys().(*syscall.Stat_t); ok {
				return sys.Uid, sys.Gid, nil
			}
			return 0, 0, fmt.Errorf("no stat_t for %s", p)
		}
		next := filepath.Dir(p)
		if next == p {
			break
		}
		p = next
	}
	return 0, 0, fmt.Errorf("no existing parent found for %s", path)
}

func chownR(root string, uid, gid int) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		_ = os.Chown(p, uid, gid) // ignore EPERM for weird cases
		return nil
	})
}

func chmodR(root string, dirMode, fileMode os.FileMode) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			_ = os.Chmod(p, dirMode)
		} else {
			_ = os.Chmod(p, fileMode)
		}
		return nil
	})
}

func touchFile(path string, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND, perm)
	if err != nil {
		return err
	}
	return f.Close()
}


// ChownPath best-effort chown to username:webGroup. No-op when not root.
func ChownPath(path, username, webGroup string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(username) == "" {
		return nil
	}
	if os.Geteuid() != 0 {
		return nil
	}

	uid, ugid, ok := lookupUserUIDGID(username)
	if !ok {
		return fmt.Errorf("cannot find user %q in /etc/passwd", username)
	}

	gid := ugid
	if strings.TrimSpace(webGroup) != "" {
		if g, ok := lookupGroupGID(webGroup); ok {
			gid = g
		}
	}
	return os.Chown(path, int(uid), int(gid))
}
