package backup

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mynginx/internal/store"
)

func Create(st store.SiteStore, opts BackupOptions, out io.Writer) (BackupResult, error) {
	manifest, dump, sites, certDomains, err := buildDump(st, opts)
	if err != nil {
		return BackupResult{}, err
	}
	tw := newTarWriter(out)
	defer tw.close()

	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := tw.addBytes("manifest.json", mb, 0o644, manifest.CreatedAt); err != nil {
		return BackupResult{}, err
	}
	db, _ := json.MarshalIndent(dump, "", "  ")
	if err := tw.addBytes("db/dump.json", db, 0o644, manifest.CreatedAt); err != nil {
		return BackupResult{}, err
	}

	siteCount := 0
	for _, s := range sites {
		u, ok := s.userHomes[s.site.UserID]
		if !ok {
			continue
		}
		siteRoot := filepath.Dir(s.site.Webroot)
		rel, err := filepath.Rel(u.HomeDir, siteRoot)
		if err != nil {
			continue
		}
		rel = filepath.Clean(rel)
		if strings.HasPrefix(rel, "..") {
			continue
		}
		prefix := filepath.Join("sites", u.Username, rel)
		added, err := tw.addTree(siteRoot, prefix, func(rel string, d os.DirEntry) bool {
			return strings.HasPrefix(rel, "logs/") || rel == "logs"
		})
		if err != nil {
			return BackupResult{}, err
		}
		siteCount += added
	}

	certCount := 0
	if opts.IncludeCerts {
		for _, d := range certDomains {
			for _, f := range []string{"fullchain.pem", "privkey.pem"} {
				src := filepath.Join(opts.CertsRoot, d, f)
				if _, err := os.Stat(src); err != nil {
					continue
				}
				if err := tw.addFileFromDisk(src, filepath.Join("certs", d, f)); err != nil {
					continue
				}
				certCount++
			}
		}
	}
	return BackupResult{Manifest: manifest, SiteFileCount: siteCount, CertFileCount: certCount}, nil
}

type scopedSite struct {
	site      store.Site
	userHomes map[int64]store.User
}

func buildDump(st store.SiteStore, opts BackupOptions) (Manifest, Dump, []scopedSite, []string, error) {
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	allPanel, err := st.ListPanelUsers()
	if err != nil {
		return Manifest{}, Dump{}, nil, nil, err
	}
	allUsers, err := st.ListUsers()
	if err != nil {
		return Manifest{}, Dump{}, nil, nil, err
	}
	userByName := map[string]store.User{}
	for _, u := range allUsers {
		userByName[u.Username] = u
	}
	panelByName := map[string]store.PanelUser{}
	for _, pu := range allPanel {
		panelByName[pu.Username] = pu
	}

	includeUsernames := map[string]bool{}
	manifest := Manifest{SchemaVersion: 1, CreatedAt: now, NodeID: opts.NodeID, Scope: opts.Scope, Driver: opts.Driver, IncludeCerts: opts.IncludeCerts}
	switch opts.Scope {
	case ScopeUser:
		username := strings.TrimSpace(opts.Username)
		pu, ok := panelByName[username]
		if !ok {
			return Manifest{}, Dump{}, nil, nil, fmt.Errorf("panel user %q not found", username)
		}
		includeUsernames[username] = true
		manifest.Subject = ManifestSubject{Username: pu.Username, UserID: pu.ID}
		if pu.ResellerID != nil {
			manifest.Subject.ResellerID = *pu.ResellerID
		}
	case ScopeReseller:
		username := strings.TrimSpace(opts.Username)
		reseller, ok := panelByName[username]
		if !ok || reseller.Role != "reseller" {
			return Manifest{}, Dump{}, nil, nil, fmt.Errorf("reseller %q not found", username)
		}
		includeUsernames[username] = true
		for _, pu := range allPanel {
			if pu.Role == "user" && pu.ResellerID != nil && *pu.ResellerID == reseller.ID {
				includeUsernames[pu.Username] = true
			}
		}
		manifest.Subject = ManifestSubject{Username: reseller.Username, UserID: reseller.ID, ResellerID: reseller.ID}
	case ScopeAll:
		for _, pu := range allPanel {
			includeUsernames[pu.Username] = true
		}
	default:
		return Manifest{}, Dump{}, nil, nil, fmt.Errorf("invalid scope %q", opts.Scope)
	}

	dump := Dump{}
	userByID := map[int64]store.User{}
	for _, pu := range allPanel {
		if !includeUsernames[pu.Username] {
			continue
		}
		resellerName := ""
		if pu.ResellerID != nil {
			if r, err := st.GetPanelUserByID(*pu.ResellerID); err == nil {
				resellerName = r.Username
			}
		}
		dump.PanelUsers = append(dump.PanelUsers, PanelUserDump{Username: pu.Username, PasswordHash: pu.PasswordHash, Role: pu.Role, Enabled: pu.Enabled, ResellerName: resellerName, SystemUser: pu.SystemUser})
		if u, ok := userByName[pu.Username]; ok {
			dump.Users = append(dump.Users, UserDump{Username: u.Username, HomeDir: u.HomeDir})
			userByID[u.ID] = u
		}
	}
	sort.Slice(dump.PanelUsers, func(i, j int) bool { return dump.PanelUsers[i].Username < dump.PanelUsers[j].Username })
	sort.Slice(dump.Users, func(i, j int) bool { return dump.Users[i].Username < dump.Users[j].Username })

	pkgs, err := st.ListPackages()
	if err != nil {
		return Manifest{}, Dump{}, nil, nil, err
	}
	pkgByID := map[int64]store.Package{}
	for _, p := range pkgs {
		pkgByID[p.ID] = p
		dump.Packages = append(dump.Packages, packageDumpFromStore(p))
	}

	sitesScoped := []scopedSite{}
	domainSet := map[string]bool{}
	for _, u := range userByID {
		sites, err := st.ListSitesByUserID(u.ID)
		if err != nil {
			return Manifest{}, Dump{}, nil, nil, err
		}
		for _, s := range sites {
			dump.Sites = append(dump.Sites, SiteDump{OwnerUsername: u.Username, Domain: s.Domain, ParentDomain: s.ParentDomain, Mode: s.Mode, Webroot: s.Webroot, PHPVersion: s.PHPVersion, EnableHTTP3: s.EnableHTTP3, Enabled: s.Enabled, ClientMaxBody: s.ClientMaxBodySize, PHPTimeRead: s.PHPTimeRead, PHPTimeSend: s.PHPTimeSend})
			sitesScoped = append(sitesScoped, scopedSite{site: s, userHomes: userByID})
			domainSet[s.Domain] = true
			pts, err := st.ListProxyTargetsBySiteID(s.ID)
			if err == nil {
				for _, t := range pts {
					dump.ProxyTargets = append(dump.ProxyTargets, ProxyTargetDump{SiteDomain: s.Domain, Target: t.Addr, Weight: t.Weight, IsBackup: t.Backup, Enabled: t.Enabled})
				}
			}
		}
		up, err := st.GetUserPackage(u.ID)
		if err == nil {
			pkg, ok := pkgByID[up.PackageID]
			if ok {
				dump.UserPackages = append(dump.UserPackages, UserPackageDump{Username: u.Username, PackageName: pkg.Name})
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return Manifest{}, Dump{}, nil, nil, err
		}
	}
	sort.Slice(dump.Packages, func(i, j int) bool { return dump.Packages[i].Name < dump.Packages[j].Name })
	certDomains := make([]string, 0, len(domainSet))
	for d := range domainSet {
		certDomains = append(certDomains, d)
	}
	sort.Strings(certDomains)
	return manifest, dump, sitesScoped, certDomains, nil
}

func packageDumpFromStore(p store.Package) PackageDump {
	return PackageDump{Name: p.Name, MaxDomains: p.MaxDomains, MaxSubdomains: p.MaxSubdomains, MaxDiskMB: p.MaxDiskMB, MaxBandwidthGB: p.MaxBandwidthGB, PHPVersions: append([]string{}, p.PHPVersions...), MaxPHPWorkers: p.MaxPHPWorkers, MaxMySQLDBs: p.MaxMySQLDBs, MaxMySQLUsers: p.MaxMySQLUsers, MaxEmailAccts: p.MaxEmailAccts, MaxUsers: p.MaxUsers, CgroupCPUPct: p.CgroupCPUPct, CgroupMemMB: p.CgroupMemMB, CgroupIOMBps: p.CgroupIOMBps}
}
