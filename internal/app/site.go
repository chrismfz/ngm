package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mynginx/internal/bootstrap"
	appdns "mynginx/internal/dns"
	"mynginx/internal/store"
	"mynginx/internal/users"
	"mynginx/internal/util"
)

type SiteAddRequest struct {
	User         string
	Domain       string
	ParentDomain string
	Mode         string // php|proxy|static
	PHP          string
	Webroot      string // optional
	HTTP3        bool
	Provision    bool
	SkipCert     bool
	ApplyNow     bool

	// nginx knobs
	ClientMaxBodySize string // e.g. "32M", "128M"
	PHPTimeRead       string // e.g. "60s", "300s"
	PHPTimeSend       string // e.g. "60s", "300s"

	// Optional: raw php.ini overrides (textarea). Stored in a per-site file, not sqlite.
	PHPIniOverrides string

	// For proxy mode: one per line, e.g. "127.0.0.1:8080" or "10.0.0.2:8080 50"
	ProxyTargets []string
}

type SiteAddResult struct {
	Site     store.Site
	Warnings []string
}

type SiteEditRequest struct {
	Domain string

	// optional fields (empty = keep existing, except booleans via pointers)
	User            string
	ParentDomain    string
	ParentDomainSet bool
	Mode            string
	PHP             string
	Webroot         string

	HTTP3   *bool
	Enabled *bool

	// nginx knobs (empty = keep existing)
	ClientMaxBodySize string
	PHPTimeRead       string
	PHPTimeSend       string

	// Optional: raw php.ini overrides textarea.
	// If empty string => keep existing (so UI can send empty only if user explicitly cleared).
	// We'll treat " " (spaces) as clear.
	PHPIniOverrides *string

	ApplyNow bool
}

type SiteListItem struct {
	Site      store.Site
	State     string // OK|PENDING|ERROR|DISABLED
	Last      string // formatted last applied (or "-")
	DNSStatus string // zone|record|missing|error|disabled
}

func phpOverridesPathFromWebroot(webroot string) string {
	siteRoot := filepath.Dir(webroot) // .../<domain> (since webroot ends with /public)
	// Store inside existing php/ folder:
	return filepath.Join(siteRoot, "php", "php.ini")
}

// ReadPHPOverridesFile loads the current overrides (if any) for UI prefilling.
func ReadPHPOverridesFile(webroot string) (string, error) {
	p := phpOverridesPathFromWebroot(webroot)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	// keep as-is; UI textarea can show it
	return string(b), nil
}

func writePHPOverridesFile(webroot, ownerUser, webGroup, raw string) error {
	p := phpOverridesPathFromWebroot(webroot)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.TrimSpace(raw)

	// Clear overrides -> remove file
	if raw == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(p)
	if err := util.MkdirAll(dir, 0750); err != nil {
		return err
	}

	_ = users.ChownPath(dir, ownerUser, webGroup) // best-effort

	// newline at end for nicer diffs

	if err := util.WriteFileAtomic(p, []byte(raw+"\n"), 0640); err != nil {
		return err
	}
	_ = users.ChownPath(p, ownerUser, webGroup) // best-effort
	return nil
}

func normalizeDomain(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

var domainLabelRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func isLikelyFQDN(domain string) bool {
	domain = normalizeDomain(domain)
	if domain == "" || strings.Contains(domain, " ") || strings.Contains(domain, "..") {
		return false
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || !strings.Contains(domain, ".") {
		return false
	}
	parts := strings.Split(domain, ".")
	for _, p := range parts {
		if len(p) < 1 || len(p) > 63 || !domainLabelRe.MatchString(p) {
			return false
		}
	}
	return true
}

func (a *App) validateParentDomain(owner store.User, domain, parentDomain string) (store.Site, string, error) {
	parentDomain = normalizeDomain(parentDomain)
	domain = normalizeDomain(domain)
	if parentDomain == "" {
		return store.Site{}, "", nil
	}
	if domain == parentDomain {
		return store.Site{}, "", fmt.Errorf("domain must not equal parent domain")
	}
	if !strings.HasSuffix(domain, "."+parentDomain) {
		return store.Site{}, "", fmt.Errorf("domain %s must be under parent domain %s", domain, parentDomain)
	}
	labelPart := strings.TrimSuffix(domain, "."+parentDomain)
	labelPart = strings.TrimSuffix(labelPart, ".")
	if labelPart == "" || strings.Contains(labelPart, ".") {
		return store.Site{}, "", fmt.Errorf("domain %s must be a direct subdomain of %s", domain, parentDomain)
	}
	parentSite, err := a.st.GetSiteByDomain(parentDomain)
	if err != nil {
		return store.Site{}, "", fmt.Errorf("parent domain %s not found", parentDomain)
	}
	if parentSite.ParentDomain != nil && strings.TrimSpace(*parentSite.ParentDomain) != "" {
		return store.Site{}, "", fmt.Errorf("parent domain %s must be a root domain", parentDomain)
	}
	if parentSite.UserID != owner.ID {
		return store.Site{}, "", fmt.Errorf("parent domain %s does not belong to this user", parentDomain)
	}
	return parentSite, labelPart, nil
}

func enforceMaxLimit(limit, current int, errFmt string) error {
	if limit == -1 {
		return nil
	}
	if current >= limit {
		return fmt.Errorf(errFmt, current, limit)
	}
	return nil
}

func validatePHPVersionAllowed(allowed []string, selected string) error {
	if len(allowed) == 0 {
		return nil
	}
	for _, v := range allowed {
		if strings.EqualFold(strings.TrimSpace(v), strings.TrimSpace(selected)) {
			return nil
		}
	}
	return fmt.Errorf("php version %s is not allowed for this package", selected)
}

func deriveDefaultWebroot(home, domain, parent, label string) string {
	if parent == "" {
		return filepath.Join(home, "sites", domain, "public")
	}
	return filepath.Join(home, "sites", parent, "subdomains", label, "public")
}

func normalizeParentPtr(parent string) *string {
	parent = normalizeDomain(parent)
	if parent == "" {
		return nil
	}
	return &parent
}

func (a *App) resolveUserLimitsByUsername(username string) (store.PanelUser, store.UserLimits, error) {
	panelUser, err := a.st.GetPanelUserByUsername(username)
	if err != nil {
		return store.PanelUser{}, store.UserLimits{}, fmt.Errorf("panel user %s not found", username)
	}
	limits, err := a.st.GetEffectiveLimits(panelUser.ID, panelUser.Role)
	if err != nil && !errors.Is(err, store.ErrNoPackageAssigned) {
		return store.PanelUser{}, store.UserLimits{}, err
	}
	return panelUser, limits, nil
}

func (a *App) SiteAdd(ctx context.Context, req SiteAddRequest) (SiteAddResult, error) {
	_ = ctx

	var out SiteAddResult

	user := strings.TrimSpace(req.User)
	domain := normalizeDomain(req.Domain)
	parentDomain := normalizeDomain(req.ParentDomain)
	if user == "" || domain == "" {
		return out, fmt.Errorf("required: user and domain")
	}
	if !isLikelyFQDN(domain) {
		return out, fmt.Errorf("invalid domain %q: expected a fully qualified domain name like example.com", domain)
	}
	if parentDomain != "" && !isLikelyFQDN(parentDomain) {
		return out, fmt.Errorf("invalid parent domain %q: expected a root domain like example.com", parentDomain)
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "php"
	}
	if mode != "php" && mode != "proxy" && mode != "static" {
		return out, fmt.Errorf("invalid mode %q", mode)
	}

	phpv := strings.TrimSpace(req.PHP)
	if phpv == "" {
		phpv = a.cfg.PHPFPM.DefaultVersion
	}

	home := filepath.Join(a.cfg.Hosting.HomeRoot, user)

	u, err := a.st.EnsureUser(user, home)
	if err != nil {
		return out, err
	}
	_, limits, err := a.resolveUserLimitsByUsername(u.Username)
	if err != nil {
		return out, err
	}
	if err := validatePHPVersionAllowed(limits.PHPVersions, phpv); err != nil {
		return out, err
	}
	_, label, err := a.validateParentDomain(u, domain, parentDomain)
	if err != nil {
		return out, err
	}
	if parentDomain == "" {
		currentRoots, err := a.st.CountRootDomainsByUserID(u.ID)
		if err != nil {
			return out, err
		}
		if err := enforceMaxLimit(limits.MaxDomains, currentRoots, "domain limit reached (%d/%d)"); err != nil {
			return out, err
		}
	} else {
		currentSubs, err := a.st.CountSubdomainsByUserID(u.ID)
		if err != nil {
			return out, err
		}
		if err := enforceMaxLimit(limits.MaxSubdomains, currentSubs, "subdomain limit reached (%d/%d)"); err != nil {
			return out, err
		}
	}
	wr := strings.TrimSpace(req.Webroot)
	if wr == "" {
		wr = deriveDefaultWebroot(home, domain, parentDomain, label)
	}

	// Provision OS user + filesystem layout
	if req.Provision {
		if err := users.EnsureSystemUser(user, home); err != nil {
			return out, err
		}
		webGroup := a.cfg.Hosting.WebGroup
		if webGroup == "" {
			webGroup = "www-data"
		}
		if _, err := users.EnsureSiteDirs(user, home, wr, webGroup); err != nil {
			return out, err
		}
	}

	_, preExistingErr := a.st.GetSiteByDomain(domain)
	siteCreated := errors.Is(preExistingErr, sql.ErrNoRows)
	if preExistingErr != nil && !siteCreated {
		return out, preExistingErr
	}

	s, err := a.st.UpsertSite(store.Site{
		UserID:            u.ID,
		Domain:            domain,
		ParentDomain:      normalizeParentPtr(parentDomain),
		Mode:              mode,
		Webroot:           wr,
		PHPVersion:        phpv,
		EnableHTTP3:       req.HTTP3,
		Enabled:           true,
		ClientMaxBodySize: strings.TrimSpace(req.ClientMaxBodySize),
		PHPTimeRead:       strings.TrimSpace(req.PHPTimeRead),
		PHPTimeSend:       strings.TrimSpace(req.PHPTimeSend),
	})
	if err != nil {
		return out, err
	}
	if a.dns != nil {
		dnsIn := a.siteDNSInputFromParts(domain, parentDomain)
		if parentDomain == "" {
			if err := a.dns.EnsureRootSite(ctx, dnsIn); err != nil {
				if siteCreated {
					if derr := a.st.DeleteSiteByDomain(domain); derr != nil {
						return out, fmt.Errorf("dns ensure root site: %v; compensation delete failed: %w", err, derr)
					}
				}
				return out, fmt.Errorf("dns ensure root site: %w", err)
			}
		} else {
			if err := a.dns.EnsureSubdomainSite(ctx, dnsIn); err != nil {
				if siteCreated {
					if derr := a.st.DeleteSiteByDomain(domain); derr != nil {
						return out, fmt.Errorf("dns ensure subdomain site: %v; compensation delete failed: %w", err, derr)
					}
				}
				return out, fmt.Errorf("dns ensure subdomain site: %w", err)
			}
		}
	}
	out.Site = s

	// Persist php.ini overrides sidecar (php-mode only)
	if mode == "php" {
		webGroup := a.cfg.Hosting.WebGroup
		if webGroup == "" {
			webGroup = "www-data"
		}
		_ = writePHPOverridesFile(wr, user, webGroup, req.PHPIniOverrides) // best-effort
	}

	// If proxy targets were provided on create, persist them before apply.
	if mode == "proxy" && len(req.ProxyTargets) > 0 {
		for _, line := range req.ProxyTargets {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			weight := 100
			addr := line
			// allow "addr weight"
			if parts := strings.Fields(line); len(parts) >= 1 {
				addr = parts[0]
				if len(parts) >= 2 {
					if w, err := strconv.Atoi(parts[1]); err == nil && w > 0 {
						weight = w
					}
				}
			}
			if err := a.st.UpsertProxyTarget(s.ID, addr, weight, false, true); err != nil {
				out.Warnings = append(out.Warnings, "proxy target add failed: "+err.Error())
			}
		}
	}

	// Don't apply proxy site if still no targets.
	if mode == "proxy" && req.ApplyNow {
		ts, err := a.st.ListProxyTargetsBySiteID(s.ID)
		if err != nil || len(ts) == 0 {
			out.Warnings = append(out.Warnings, "proxy site created: add at least 1 proxy target, then click Apply")
			req.ApplyNow = false
		}
	}

	// Bootstrap vhost immediately so HTTP-01 can work (unless disabled).
	applyOK := !req.ApplyNow
	if req.ApplyNow {
		if _, err := a.Apply(context.Background(), ApplyRequest{Domain: domain}); err != nil {
			out.Warnings = append(out.Warnings, err.Error())
		} else {
			applyOK = true
		}
	}

	// Issue certificate automatically (unless skipped).
	if !req.SkipCert {
		switch {
		case !req.ApplyNow:
			out.Warnings = append(out.Warnings, "certificate issuance skipped: apply-now is disabled; apply the site first, then issue cert")
		case !applyOK:
			out.Warnings = append(out.Warnings, "certificate issuance skipped: apply-now failed; fix apply errors before issuing cert")
		default:
			ctx2, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if err := a.CertIssue(ctx2, domain, true /* apply */); err != nil {
				if errors.Is(err, bootstrap.ErrProvisionInitNotCompleted) {
					out.Warnings = append(out.Warnings, "certificate issuance blocked: "+bootstrap.ErrProvisionInitNotCompleted.Error())
				} else {
					out.Warnings = append(out.Warnings, "certificate issuance failed: "+err.Error())
				}
			}
		}
	}

	return out, nil
}

func (a *App) SiteDisable(ctx context.Context, domain string) error {
	_ = ctx
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return fmt.Errorf("domain is required")
	}
	return a.st.DisableSiteByDomain(d)
}

func (a *App) SiteEnable(ctx context.Context, domain string) (store.Site, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return store.Site{}, fmt.Errorf("domain is required")
	}
	if err := a.st.EnableSiteByDomain(domain); err != nil {
		return store.Site{}, err
	}
	return a.st.GetSiteByDomain(domain)
}

// SiteDelete hard-deletes DB rows and also removes the live nginx vhost (best-effort).
// (We intentionally do NOT delete cert files here.)
func (a *App) SiteDelete(ctx context.Context, domain string) error {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	site, err := a.st.GetSiteByDomain(domain)
	if err != nil {
		return err
	}
	if a.dns != nil {
		if site.ParentDomain == nil || strings.TrimSpace(*site.ParentDomain) == "" {
			sites, err := a.st.ListSites()
			if err != nil {
				return err
			}
			for _, s := range sites {
				if s.ParentDomain != nil && strings.EqualFold(strings.TrimSpace(*s.ParentDomain), domain) {
					return fmt.Errorf("cannot delete root domain %s: dependent subdomain site %s exists", domain, s.Domain)
				}
			}
			if err := a.dns.DeleteRootSite(ctx, a.siteDNSInputFromParts(site.Domain, "")); err != nil {
				return fmt.Errorf("dns delete root site: %w", err)
			}
		} else {
			if err := a.dns.DeleteSubdomainSite(ctx, a.siteDNSInputFromParts(site.Domain, strings.TrimSpace(*site.ParentDomain))); err != nil {
				return fmt.Errorf("dns delete subdomain site: %w", err)
			}
		}
	}

	// Best-effort remove live vhost (ignore missing file)
	removed := false
	if err := a.ng.RemoveLiveSite(domain); err == nil {
		removed = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("remove live vhost: %w", err)
	}

	if removed {
		if err := a.ng.Reload(); err != nil {
			return fmt.Errorf("nginx reload: %w", err)
		}
	}

	// Hard delete from DB (handles proxy_targets/apply_runs too)
	if err := a.st.DeleteSiteByDomain(domain); err != nil {
		if a.dns != nil {
			restoreIn := a.siteDNSInputFromParts(site.Domain, "")
			if site.ParentDomain != nil && strings.TrimSpace(*site.ParentDomain) != "" {
				restoreIn = a.siteDNSInputFromParts(site.Domain, strings.TrimSpace(*site.ParentDomain))
			}
			var rerr error
			if restoreIn.SiteKind == appdns.SiteKindRoot {
				rerr = a.dns.EnsureRootSite(ctx, restoreIn)
			} else {
				rerr = a.dns.EnsureSubdomainSite(ctx, restoreIn)
			}
			if rerr != nil {
				return fmt.Errorf("delete site row: %v; dns restore failed: %w", err, rerr)
			}
		}
		return err
	}
	return nil
}

func (a *App) SiteEdit(ctx context.Context, req SiteEditRequest) (store.Site, error) {
	_ = ctx

	d := normalizeDomain(req.Domain)
	if d == "" {
		return store.Site{}, fmt.Errorf("domain is required")
	}
	if !isLikelyFQDN(d) {
		return store.Site{}, fmt.Errorf("invalid domain %q: expected a fully qualified domain name like example.com", d)
	}

	cur, err := a.st.GetSiteByDomain(d)
	if err != nil {
		return store.Site{}, err
	}

	// Update user (optional)
	userID := cur.UserID
	ownerUser := ""
	owner, err := a.st.GetUserByID(cur.UserID)
	if err == nil {
		ownerUser = owner.Username
	}
	if strings.TrimSpace(req.User) != "" {
		user := strings.TrimSpace(req.User)
		home := filepath.Join(a.cfg.Hosting.HomeRoot, user)
		u, err := a.st.EnsureUser(user, home)
		if err != nil {
			return store.Site{}, err
		}
		userID = u.ID
		owner = u
		ownerUser = u.Username
	}
	if owner.ID == 0 {
		owner, err = a.st.GetUserByID(userID)
		if err != nil {
			return store.Site{}, err
		}
		if ownerUser == "" {
			ownerUser = owner.Username
		}
	}

	mode := cur.Mode
	if strings.TrimSpace(req.Mode) != "" {
		mode = strings.TrimSpace(req.Mode)
		if mode != "php" && mode != "proxy" && mode != "static" {
			return store.Site{}, fmt.Errorf("invalid mode %q", mode)
		}
	}

	phpv := cur.PHPVersion
	if strings.TrimSpace(req.PHP) != "" {
		phpv = strings.TrimSpace(req.PHP)
	}
	_, limits, err := a.resolveUserLimitsByUsername(owner.Username)
	if err != nil {
		return store.Site{}, err
	}
	if err := validatePHPVersionAllowed(limits.PHPVersions, phpv); err != nil {
		return store.Site{}, err
	}

	newParent := ""
	if cur.ParentDomain != nil {
		newParent = *cur.ParentDomain
	}
	if req.ParentDomainSet {
		newParent = normalizeDomain(req.ParentDomain)
		if newParent != "" && !isLikelyFQDN(newParent) {
			return store.Site{}, fmt.Errorf("invalid parent domain %q: expected a root domain like example.com", newParent)
		}
	}
	if a.dns != nil && req.ParentDomainSet {
		curParent := ""
		if cur.ParentDomain != nil {
			curParent = normalizeDomain(*cur.ParentDomain)
		}
		if normalizeDomain(newParent) != curParent {
			return store.Site{}, fmt.Errorf("changing parent domain is not supported while dns.enabled=true in this phase")
		}
	}
	_, label, err := a.validateParentDomain(owner, d, newParent)
	if err != nil {
		return store.Site{}, err
	}
	webroot := cur.Webroot
	if strings.TrimSpace(req.Webroot) != "" {
		webroot = strings.TrimSpace(req.Webroot)
	} else if req.ParentDomainSet || strings.TrimSpace(req.User) != "" {
		home := filepath.Join(a.cfg.Hosting.HomeRoot, owner.Username)
		webroot = deriveDefaultWebroot(home, d, newParent, label)
	}

	http3 := cur.EnableHTTP3
	if req.HTTP3 != nil {
		http3 = *req.HTTP3
	}

	enabled := cur.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	clientMax := cur.ClientMaxBodySize
	if strings.TrimSpace(req.ClientMaxBodySize) != "" {
		clientMax = strings.TrimSpace(req.ClientMaxBodySize)
	}
	phpRead := cur.PHPTimeRead
	if strings.TrimSpace(req.PHPTimeRead) != "" {
		phpRead = strings.TrimSpace(req.PHPTimeRead)
	}
	phpSend := cur.PHPTimeSend
	if strings.TrimSpace(req.PHPTimeSend) != "" {
		phpSend = strings.TrimSpace(req.PHPTimeSend)
	}
	if req.ParentDomainSet {
		wasRoot := cur.ParentDomain == nil || strings.TrimSpace(*cur.ParentDomain) == ""
		isRoot := strings.TrimSpace(newParent) == ""
		if wasRoot != isRoot {
			if a.dns != nil {
				return store.Site{}, fmt.Errorf("changing root/subdomain relationship is not supported while dns.enabled=true in this phase")
			}
			if isRoot {
				currentRoots, err := a.st.CountRootDomainsByUserID(owner.ID)
				if err != nil {
					return store.Site{}, err
				}
				if err := enforceMaxLimit(limits.MaxDomains, currentRoots, "domain limit reached (%d/%d)"); err != nil {
					return store.Site{}, err
				}
			} else {
				currentSubs, err := a.st.CountSubdomainsByUserID(owner.ID)
				if err != nil {
					return store.Site{}, err
				}
				if err := enforceMaxLimit(limits.MaxSubdomains, currentSubs, "subdomain limit reached (%d/%d)"); err != nil {
					return store.Site{}, err
				}
			}
		}
	}

	updated, err := a.st.UpsertSite(store.Site{
		UserID:            userID,
		Domain:            d,
		ParentDomain:      normalizeParentPtr(newParent),
		Mode:              mode,
		Webroot:           webroot,
		PHPVersion:        phpv,
		EnableHTTP3:       http3,
		Enabled:           enabled,
		ClientMaxBodySize: clientMax,
		PHPTimeRead:       phpRead,
		PHPTimeSend:       phpSend,
	})
	if err != nil {
		return store.Site{}, err
	}

	// Resolve owner username for chown of php overrides
	if ownerUser == "" {
		ownerUser = strings.TrimSpace(req.User)
	}
	webGroup := a.cfg.Hosting.WebGroup
	if webGroup == "" {
		webGroup = "www-data"
	}

	// Persist overrides if caller provided it (nil means "don't touch")
	if mode == "php" && req.PHPIniOverrides != nil {
		if err := writePHPOverridesFile(webroot, ownerUser, webGroup, *req.PHPIniOverrides); err != nil {
			// keep site save OK; surface later if you want via warnings
			_ = err
		}
	}

	if req.ApplyNow {
		_, _ = a.Apply(context.Background(), ApplyRequest{Domain: d})
	}

	return updated, nil
}

func (a *App) siteDNSInputFromParts(domain, parent string) appdns.SiteDNSInput {
	in := appdns.SiteDNSInput{
		FQDN:         normalizeDomain(domain),
		ParentDomain: normalizeDomain(parent),
		Template:     a.cfg.DNS.DefaultTemplate,
		DefaultIPv4:  strings.TrimSpace(a.cfg.DNS.DefaultIPv4),
		DefaultIPv6:  strings.TrimSpace(a.cfg.DNS.DefaultIPv6),
	}
	if in.ParentDomain == "" {
		in.SiteKind = appdns.SiteKindRoot
	} else {
		in.SiteKind = appdns.SiteKindSubdomain
	}
	return in
}

func (a *App) SiteList(ctx context.Context) ([]SiteListItem, error) {
	_ = ctx
	sites, err := a.st.ListSites()
	if err != nil {
		return nil, err
	}
	out := make([]SiteListItem, 0, len(sites))
	for _, s := range sites {
		state, last := computeSiteState(s)
		dnsStatus := "disabled"
		if a.dns != nil {
			parent := ""
			if s.ParentDomain != nil {
				parent = strings.TrimSpace(*s.ParentDomain)
			}
			entry, err := a.dns.GetSiteEntry(ctx, a.siteDNSInputFromParts(s.Domain, parent))
			if err != nil {
				dnsStatus = "error"
			} else {
				dnsStatus = entry.Status
			}
		}
		out = append(out, SiteListItem{Site: s, State: state, Last: last, DNSStatus: dnsStatus})
	}
	return out, nil
}

func (a *App) DNSList(ctx context.Context, domainFilter string) ([]appdns.DNSEntry, error) {
	sites, err := a.st.ListSites()
	if err != nil {
		return nil, err
	}
	out := make([]appdns.DNSEntry, 0, len(sites))
	filter := normalizeDomain(domainFilter)
	for _, s := range sites {
		if filter != "" && normalizeDomain(s.Domain) != filter {
			continue
		}
		parent := ""
		if s.ParentDomain != nil {
			parent = strings.TrimSpace(*s.ParentDomain)
		}
		in := a.siteDNSInputFromParts(s.Domain, parent)
		if a.dns == nil {
			zone := in.ParentDomain
			if zone == "" {
				zone = in.FQDN
			}
			out = append(out, appdns.DNSEntry{
				FQDN:   normalizeDomain(s.Domain),
				Zone:   zone,
				Kind:   string(in.SiteKind),
				Status: "disabled",
			})
			continue
		}
		entry, err := a.dns.GetSiteEntry(ctx, in)
		if err != nil {
			out = append(out, appdns.DNSEntry{
				FQDN:     normalizeDomain(s.Domain),
				Zone:     in.ParentDomain,
				Kind:     string(in.SiteKind),
				Status:   "error",
				ZoneFile: entry.ZoneFile,
			})
			continue
		}
		out = append(out, entry)
	}
	return out, nil
}

func (a *App) SiteGet(ctx context.Context, domain string) (store.Site, error) {
	_ = ctx
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return store.Site{}, fmt.Errorf("domain is required")
	}
	return a.st.GetSiteByDomain(d)
}

func computeSiteState(s store.Site) (state string, last string) {
	last = "-"
	if s.LastAppliedAt != nil {
		last = s.LastAppliedAt.Format("2006-01-02 15:04")
	}

	if !s.Enabled {
		return "DISABLED", last
	}
	if s.LastApplyStatus == "fail" {
		return "ERROR", last
	}
	if siteNeedsApply(s) {
		return "PENDING", last
	}
	if s.LastApplyStatus == "ok" {
		return "OK", last
	}
	return "PENDING", last
}
