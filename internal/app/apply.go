package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mynginx/internal/nginx"
	"mynginx/internal/store"
	"mynginx/internal/util"
)

type ApplyRequest struct {
	Domain string
	All    bool
	DryRun bool
	Limit  int
}

type ApplyDomainResult struct {
	Domain     string
	Action     string // apply|delete|skip
	Changed    bool
	RenderHash string
	Status     string // ok|fail|skipped|dry-run
	Error      string
}

type ApplyResult struct {
	Domains  []ApplyDomainResult
	Changed  []string
	Reloaded bool
}

type applyResultUpdater interface {
	UpdateApplyResult(domain, status, errMsg, renderHash string) error
}

type proxyTargetLister interface {
	ListProxyTargetsBySiteID(siteID int64) ([]nginx.UpstreamTarget, error)
}

func (a *App) Apply(ctx context.Context, req ApplyRequest) (ApplyResult, error) {
	// touches files + reloads nginx; avoid concurrent applies
	a.applyMu.Lock()
	defer a.applyMu.Unlock()
	_ = ctx // reserved for future cancellation/timeouts

	var res ApplyResult

	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	if domain != "" {
		dr, changed, err := a.applyOne(domain, req.DryRun)
		res.Domains = []ApplyDomainResult{dr}
		if changed {
			res.Changed = []string{domain}
		}
		return res, err
	}

	sites, err := a.st.ListSites()
	if err != nil {
		return res, err
	}

	updater, _ := a.st.(applyResultUpdater)
	proxyLister, _ := a.st.(proxyTargetLister)

	applied := 0
	var changed []string
	changedHashes := map[string]string{}

	for _, s := range sites {
		if req.Limit > 0 && applied >= req.Limit {
			break
		}

		d := strings.ToLower(strings.TrimSpace(s.Domain))
		if d == "" {
			continue
		}

		if !s.Enabled {
			if req.DryRun {
				res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "delete", Status: "dry-run"})
				applied++
				continue
			}

			ok, err := stageDeleteLiveConf(a.ng, d)
			if err != nil {
				if updater != nil {
					_ = updater.UpdateApplyResult(d, "fail", "delete live conf failed: "+err.Error(), "")
				}
				res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "delete", Status: "fail", Error: err.Error()})
				applied++
				continue
			}
			if ok {
				changed = append(changed, d)
				changedHashes[d] = ""
			}
			if updater != nil {
				_ = updater.UpdateApplyResult(d, "ok", "", "")
			}
			res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "delete", Status: "ok", Changed: ok})
			applied++
			continue
		}

		if !req.All && !siteNeedsApply(s) {
			res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "skip", Status: "skipped"})
			continue
		}

		if req.DryRun {
			res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "apply", Status: "dry-run"})
			applied++
			continue
		}

		td, err := a.buildTemplateData(s, d, proxyLister)
		if err != nil {
			if updater != nil {
				_ = updater.UpdateApplyResult(d, "fail", err.Error(), "")
			}
			res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "apply", Status: "fail", Error: err.Error()})
			applied++
			continue
		}

		_, content, err := a.ng.RenderSiteToStaging(td)
		renderHash := ""
		if content != nil {
			renderHash = util.Sha256Hex(content)
		}
		if err != nil {
			if updater != nil {
				_ = updater.UpdateApplyResult(d, "fail", err.Error(), renderHash)
			}
			res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "apply", Status: "fail", Error: err.Error(), RenderHash: renderHash})
			applied++
			continue
		}

		changedNow, err := a.ng.Publish(d)
		if err != nil {
			if updater != nil {
				_ = updater.UpdateApplyResult(d, "fail", err.Error(), renderHash)
			}
			res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "apply", Status: "fail", Error: err.Error(), RenderHash: renderHash})
			applied++
			continue
		}

		if updater != nil {
			_ = updater.UpdateApplyResult(d, "ok", "", renderHash)
		}
		res.Domains = append(res.Domains, ApplyDomainResult{Domain: d, Action: "apply", Status: "ok", Changed: changedNow, RenderHash: renderHash})

		if changedNow {
			changed = append(changed, d)
			changedHashes[d] = renderHash
		}
		applied++
	}

	sort.Slice(res.Domains, func(i, j int) bool { return res.Domains[i].Domain < res.Domains[j].Domain })

	if req.DryRun || len(changed) == 0 {
		return res, nil
	}

	// validate + reload once for the batch
	if a.cfg.Nginx.Apply.TestBeforeReload {
		if err := a.ng.TestConfig(); err != nil {
			rollbackFromBackup(a.ng, changed)
			_ = a.ng.ReloadOrStart()
			if updater != nil {
				for _, d := range changed {
					_ = updater.UpdateApplyResult(d, "fail", "nginx -t failed (rolled back): "+err.Error(), changedHashes[d])
				}
			}
			return res, fmt.Errorf("nginx -t failed (rolled back): %w", err)
		}
	}

	if err := a.ng.ReloadOrStart(); err != nil {
		rollbackFromBackup(a.ng, changed)
		_ = a.ng.ReloadOrStart()
		if updater != nil {
			for _, d := range changed {
				_ = updater.UpdateApplyResult(d, "fail", "nginx reload/start failed (rolled back): "+err.Error(), changedHashes[d])
			}
		}
		return res, fmt.Errorf("nginx reload/start failed (rolled back): %w", err)
	}

	res.Changed = append([]string{}, changed...)
	res.Reloaded = true
	return res, nil
}

func (a *App) applyOne(domain string, dry bool) (ApplyDomainResult, bool, error) {
	updater, _ := a.st.(applyResultUpdater)
	proxyLister, _ := a.st.(proxyTargetLister)

	s, err := a.st.GetSiteByDomain(domain)
	if err != nil {
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "fail", Error: err.Error()}, false, fmt.Errorf("get site: %w", err)
	}

	if dry {
		if !s.Enabled {
			return ApplyDomainResult{Domain: domain, Action: "delete", Status: "dry-run"}, false, nil
		}
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "dry-run"}, false, nil
	}

	if !s.Enabled {
		ok, err := stageDeleteLiveConf(a.ng, domain)
		if err != nil {
			if updater != nil {
				_ = updater.UpdateApplyResult(domain, "fail", "delete live conf failed: "+err.Error(), "")
			}
			return ApplyDomainResult{Domain: domain, Action: "delete", Status: "fail", Error: err.Error()}, false, err
		}
		if !ok {
			return ApplyDomainResult{Domain: domain, Action: "delete", Status: "ok", Changed: false}, false, nil
		}

		if a.cfg.Nginx.Apply.TestBeforeReload {
			if err := a.ng.TestConfig(); err != nil {
				rollbackFromBackup(a.ng, []string{domain})
				_ = a.ng.ReloadOrStart()
				if updater != nil {
					_ = updater.UpdateApplyResult(domain, "fail", "nginx -t failed (rolled back): "+err.Error(), "")
				}
				return ApplyDomainResult{Domain: domain, Action: "delete", Status: "fail", Error: err.Error()}, true, fmt.Errorf("nginx -t failed (rolled back): %w", err)
			}
		}
		if err := a.ng.ReloadOrStart(); err != nil {
			rollbackFromBackup(a.ng, []string{domain})
			_ = a.ng.ReloadOrStart()
			if updater != nil {
				_ = updater.UpdateApplyResult(domain, "fail", "nginx reload/start failed (rolled back): "+err.Error(), "")
			}
			return ApplyDomainResult{Domain: domain, Action: "delete", Status: "fail", Error: err.Error()}, true, fmt.Errorf("nginx reload/start failed (rolled back): %w", err)
		}
		if updater != nil {
			_ = updater.UpdateApplyResult(domain, "ok", "", "")
		}
		return ApplyDomainResult{Domain: domain, Action: "delete", Status: "ok", Changed: true}, true, nil
	}

	td, err := a.buildTemplateData(s, domain, proxyLister)
	if err != nil {
		if updater != nil {
			_ = updater.UpdateApplyResult(domain, "fail", err.Error(), "")
		}
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "fail", Error: err.Error()}, false, err
	}

	_, content, err := a.ng.RenderSiteToStaging(td)
	renderHash := ""
	if content != nil {
		renderHash = util.Sha256Hex(content)
	}
	if err != nil {
		if updater != nil {
			_ = updater.UpdateApplyResult(domain, "fail", err.Error(), renderHash)
		}
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "fail", Error: err.Error(), RenderHash: renderHash}, false, err
	}

	changed, err := a.ng.Publish(domain)
	if err != nil {
		if updater != nil {
			_ = updater.UpdateApplyResult(domain, "fail", err.Error(), renderHash)
		}
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "fail", Error: err.Error(), RenderHash: renderHash}, false, err
	}

	if !changed {
		if updater != nil {
			_ = updater.UpdateApplyResult(domain, "ok", "", renderHash)
		}
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "ok", Changed: false, RenderHash: renderHash}, false, nil
	}

	if a.cfg.Nginx.Apply.TestBeforeReload {
		if err := a.ng.TestConfig(); err != nil {
			rollbackFromBackup(a.ng, []string{domain})
			_ = a.ng.ReloadOrStart()
			if updater != nil {
				_ = updater.UpdateApplyResult(domain, "fail", "nginx -t failed (rolled back): "+err.Error(), renderHash)
			}
			return ApplyDomainResult{Domain: domain, Action: "apply", Status: "fail", Changed: true, Error: err.Error(), RenderHash: renderHash}, true, fmt.Errorf("nginx -t failed (rolled back): %w", err)
		}
	}
	if err := a.ng.ReloadOrStart(); err != nil {
		rollbackFromBackup(a.ng, []string{domain})
		_ = a.ng.ReloadOrStart()
		if updater != nil {
			_ = updater.UpdateApplyResult(domain, "fail", "nginx reload/start failed (rolled back): "+err.Error(), renderHash)
		}
		return ApplyDomainResult{Domain: domain, Action: "apply", Status: "fail", Changed: true, Error: err.Error(), RenderHash: renderHash}, true, fmt.Errorf("nginx reload/start failed (rolled back): %w", err)
	}

	if updater != nil {
		_ = updater.UpdateApplyResult(domain, "ok", "", renderHash)
	}
	return ApplyDomainResult{Domain: domain, Action: "apply", Status: "ok", Changed: true, RenderHash: renderHash}, true, nil
}

func stageDeleteLiveConf(mgr *nginx.Manager, domain string) (bool, error) {
	live := filepath.Join(mgr.SitesDir, domain+".conf")
	if _, err := os.Stat(live); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := mgr.RemoveLiveSite(domain); err != nil {
		return false, err
	}
	return true, nil
}

func rollbackFromBackup(mgr *nginx.Manager, domains []string) {
	for _, d := range domains {
		dst := filepath.Join(mgr.SitesDir, d+".conf")
		bak := filepath.Join(mgr.BackupDir, d+".conf.bak")

		if data, err := os.ReadFile(bak); err == nil && len(data) > 0 {
			_ = util.WriteFileAtomic(dst, data, 0644)
			continue
		}
		_ = os.Remove(dst)
	}
}

func siteNeedsApply(s store.Site) bool {
	if !s.Enabled {
		return false
	}
	if s.LastAppliedAt == nil {
		return true
	}
	if s.LastApplyStatus != "ok" {
		return true
	}
	return s.UpdatedAt.After(*s.LastAppliedAt)
}
