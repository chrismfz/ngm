package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mynginx/internal/store"
	"mynginx/internal/users"
)

func Restore(st store.SiteStore, opts RestoreOptions, applyFn func() error) (RestoreResult, error) {
	manifest, dump, extractRoot, err := readArchive(opts.FilePath)
	if err != nil {
		return RestoreResult{}, err
	}
	if manifest.SchemaVersion != 1 {
		return RestoreResult{}, fmt.Errorf("unsupported schema_version=%d", manifest.SchemaVersion)
	}
	if opts.NewUser != "" && manifest.Scope != ScopeUser {
		return RestoreResult{}, fmt.Errorf("--new-user is supported only for user-scoped backups")
	}
	res := RestoreResult{Manifest: manifest}

	nameMap := map[string]string{}
	for _, pu := range dump.PanelUsers {
		nameMap[pu.Username] = pu.Username
	}
	if opts.NewUser != "" {
		nameMap[manifest.Subject.Username] = opts.NewUser
	}

	packageIDs := map[string]int64{}
	for _, p := range dump.Packages {
		existing, err := findPackageByName(st, p.Name)
		if err == nil {
			existing.MaxDomains = p.MaxDomains
			existing.MaxSubdomains = p.MaxSubdomains
			existing.MaxDiskMB = p.MaxDiskMB
			existing.MaxBandwidthGB = p.MaxBandwidthGB
			existing.PHPVersions = append([]string{}, p.PHPVersions...)
			existing.MaxPHPWorkers = p.MaxPHPWorkers
			existing.MaxMySQLDBs = p.MaxMySQLDBs
			existing.MaxMySQLUsers = p.MaxMySQLUsers
			existing.MaxEmailAccts = p.MaxEmailAccts
			existing.MaxUsers = p.MaxUsers
			existing.CgroupCPUPct = p.CgroupCPUPct
			existing.CgroupMemMB = p.CgroupMemMB
			existing.CgroupIOMBps = p.CgroupIOMBps
			existing, err = st.UpdatePackage(existing)
			if err != nil {
				return res, err
			}
			packageIDs[p.Name] = existing.ID
			res.Packages++
			continue
		}
		created, err := st.CreatePackage(store.Package{Name: p.Name, MaxDomains: p.MaxDomains, MaxSubdomains: p.MaxSubdomains, MaxDiskMB: p.MaxDiskMB, MaxBandwidthGB: p.MaxBandwidthGB, PHPVersions: append([]string{}, p.PHPVersions...), MaxPHPWorkers: p.MaxPHPWorkers, MaxMySQLDBs: p.MaxMySQLDBs, MaxMySQLUsers: p.MaxMySQLUsers, MaxEmailAccts: p.MaxEmailAccts, MaxUsers: p.MaxUsers, CgroupCPUPct: p.CgroupCPUPct, CgroupMemMB: p.CgroupMemMB, CgroupIOMBps: p.CgroupIOMBps})
		if err != nil {
			return res, err
		}
		packageIDs[p.Name] = created.ID
		res.Packages++
	}

	panelIDs := map[string]int64{}
	for _, pu := range dump.PanelUsers {
		uname := mappedName(nameMap, pu.Username)
		created, err := st.CreatePanelUser(uname, pu.PasswordHash, pu.Role, pu.Enabled)
		if err != nil {
			return res, err
		}
		created.SystemUser = mappedName(nameMap, defaultSystemUser(pu))
		if pu.ResellerName != "" {
			if rid, ok := panelIDs[mappedName(nameMap, pu.ResellerName)]; ok {
				created.ResellerID = &rid
				created.OwnerID = &rid
			}
		}
		if _, err := st.UpdatePanelUser(created); err != nil {
			return res, err
		}
		panelIDs[uname] = created.ID
		res.PanelUsers++
	}

	userIDs := map[string]int64{}
	for _, u := range dump.Users {
		uname := mappedName(nameMap, u.Username)
		home := u.HomeDir
		if home == "" || opts.NewUser != "" && u.Username == manifest.Subject.Username {
			home = filepath.Join(opts.HomeRoot, uname)
		}
		row, err := st.EnsureUser(uname, home)
		if err != nil {
			return res, err
		}
		userIDs[uname] = row.ID
		res.Users++
		if err := users.EnsureSystemUser(uname, home); err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("ensure system user %s: %v", uname, err))
		}
	}

	for _, up := range dump.UserPackages {
		uid, ok := userIDs[mappedName(nameMap, up.Username)]
		if !ok {
			continue
		}
		pid, ok := packageIDs[up.PackageName]
		if !ok {
			continue
		}
		if err := st.AssignPackage(uid, pid, uid); err != nil {
			return res, err
		}
		res.Assignments++
	}

	siteIDs := map[string]int64{}
	for _, s := range dump.Sites {
		uname := mappedName(nameMap, s.OwnerUsername)
		uid, ok := userIDs[uname]
		if !ok {
			continue
		}
		webroot := s.Webroot
		if opts.NewUser != "" && s.OwnerUsername == manifest.Subject.Username {
			oldHome := filepath.Join(opts.HomeRoot, s.OwnerUsername)
			newHome := filepath.Join(opts.HomeRoot, uname)
			webroot = strings.Replace(webroot, oldHome, newHome, 1)
		}
		site, err := st.UpsertSite(store.Site{UserID: uid, Domain: s.Domain, ParentDomain: s.ParentDomain, Mode: s.Mode, Webroot: webroot, PHPVersion: s.PHPVersion, EnableHTTP3: s.EnableHTTP3, Enabled: s.Enabled, ClientMaxBodySize: s.ClientMaxBody, PHPTimeRead: s.PHPTimeRead, PHPTimeSend: s.PHPTimeSend})
		if err != nil {
			return res, err
		}
		siteIDs[s.Domain] = site.ID
		res.Sites++
	}
	for _, p := range dump.ProxyTargets {
		sid, ok := siteIDs[p.SiteDomain]
		if !ok {
			continue
		}
		if err := st.UpsertProxyTarget(sid, p.Target, p.Weight, p.IsBackup, p.Enabled); err != nil {
			return res, err
		}
		res.ProxyTargets++
	}

	sf, cf, warns := restoreExtractedFiles(extractRoot, opts, manifest, nameMap)
	res.SiteFileCount = sf
	res.CertFileCount = cf
	res.Warnings = append(res.Warnings, warns...)

	if applyFn != nil {
		if err := applyFn(); err != nil {
			return res, err
		}
	}
	return res, nil
}

func defaultSystemUser(p PanelUserDump) string {
	if strings.TrimSpace(p.SystemUser) != "" {
		return p.SystemUser
	}
	return p.Username
}

func mappedName(m map[string]string, old string) string {
	if v, ok := m[old]; ok {
		return v
	}
	return old
}

func readArchive(path string) (Manifest, Dump, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, Dump{}, "", err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, Dump{}, "", err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	tmp, err := os.MkdirTemp("", "ngm-restore-")
	if err != nil {
		return Manifest{}, Dump{}, "", err
	}
	var m Manifest
	var d Dump
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, Dump{}, "", err
		}
		name := filepath.ToSlash(h.Name)
		switch name {
		case "manifest.json":
			b, _ := io.ReadAll(tr)
			if err := json.Unmarshal(b, &m); err != nil {
				return Manifest{}, Dump{}, "", err
			}
		case "db/dump.json":
			b, _ := io.ReadAll(tr)
			if err := json.Unmarshal(b, &d); err != nil {
				return Manifest{}, Dump{}, "", err
			}
		default:
			if strings.HasPrefix(name, "sites/") || strings.HasPrefix(name, "certs/") {
				target := filepath.Join(tmp, filepath.FromSlash(name))
				if h.FileInfo().IsDir() {
					_ = os.MkdirAll(target, 0o755)
					continue
				}
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return Manifest{}, Dump{}, "", err
				}
				w, err := os.Create(target)
				if err != nil {
					return Manifest{}, Dump{}, "", err
				}
				if _, err := io.Copy(w, tr); err != nil {
					w.Close()
					return Manifest{}, Dump{}, "", err
				}
				_ = w.Close()
			}
		}
	}
	if m.SchemaVersion == 0 {
		return Manifest{}, Dump{}, "", fmt.Errorf("manifest.json missing")
	}
	return m, d, tmp, nil
}

func restoreExtractedFiles(tmp string, opts RestoreOptions, manifest Manifest, nameMap map[string]string) (int, int, []string) {
	warnings := []string{}
	siteCount := 0
	certCount := 0
	sitesRoot := filepath.Join(tmp, "sites")
	_ = filepath.WalkDir(sitesRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(sitesRoot, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 2 {
			return nil
		}
		oldUser := parts[0]
		newUser := mappedName(nameMap, oldUser)
		target := filepath.Join(opts.HomeRoot, newUser, filepath.FromSlash(strings.Join(parts[1:], "/")))
		if err := copyFile(path, target); err != nil {
			warnings = append(warnings, err.Error())
		} else {
			siteCount++
		}
		return nil
	})
	certsRoot := filepath.Join(tmp, "certs")
	_ = filepath.WalkDir(certsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(certsRoot, path)
		target := filepath.Join(opts.CertsRoot, rel)
		if err := copyFile(path, target); err != nil {
			warnings = append(warnings, err.Error())
		} else {
			certCount++
		}
		return nil
	})
	if manifest.Scope == ScopeUser {
		warnings = append(warnings, "restored user accounts keep $SHADOW$ hash; set system password manually if needed")
	}
	return siteCount, certCount, warnings
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func findPackageByName(st store.SiteStore, name string) (store.Package, error) {
	pkgs, err := st.ListPackages()
	if err != nil {
		return store.Package{}, err
	}
	for _, p := range pkgs {
		if p.Name == name {
			return p, nil
		}
	}
	return store.Package{}, fmt.Errorf("not found")
}
