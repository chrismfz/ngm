package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mynginx/internal/config"
	"mynginx/internal/nginx"
	"mynginx/internal/store"
	storesqlite "mynginx/internal/store/sqlite"
	"mynginx/internal/util"

	"mynginx/internal/app"

	"mynginx/internal/web"

	"golang.org/x/crypto/bcrypt"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "c", "config.yaml", "Path to config.yaml")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	paths := cfg.ResolvePaths()

	// Open store early (for CLI commands)
	st, err := storesqlite.Open(cfg.Storage.SQLitePath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(); err != nil {
		log.Fatalf("store migrate: %v", err)
	}

	args := flag.Args()
	if len(args) == 0 {
		runStatus(cfg, paths)
		return
	}

	switch args[0] {
	case "serve":
		if err := cmdServe(st, cfg, paths); err != nil {
			log.Fatalf("serve: %v", err)
		}

	case "site":
		if err := cmdSite(st, cfg, paths, args[1:]); err != nil {
			log.Fatalf("site: %v", err)
		}
	case "apply":
		if err := cmdApply(st, cfg, paths, args[1:]); err != nil {
			log.Fatalf("apply: %v", err)
		}

	case "cert":
		if err := cmdCert(st, cfg, paths, args[1:]); err != nil {
			log.Fatalf("cert: %v", err)
		}

	case "panel-user":
		if err := cmdPanelUser(st, args[1:]); err != nil {
			log.Fatalf("panel-user: %v", err)
		}

	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		fmt.Println("Commands:")
		fmt.Println("  serve                                (start local UI on cfg.api.listen)")
		fmt.Println("  site add --user <u> --domain <d> [--mode php|proxy|static] [--php 8.3] [--webroot <path>] [--http3=true|false] [--skip-cert] [--apply-now=true|false]")
		fmt.Println("  site edit --domain <d> [--user <u>] [--mode php|proxy|static] [--php 8.3] [--webroot <path>] [--http3=true|false] [--enabled=true|false] [--apply-now=true|false]")
		fmt.Println("  site list")
		fmt.Println("  site rm --domain <d>")
		fmt.Println("  apply [--domain <d>] [--all] [--dry-run] [--limit N]")
		fmt.Println("  cert list                          (show all certificates)")
		fmt.Println("  cert info --domain <d>             (show cert details)")
		fmt.Println("  cert issue --domain <d>            (issue/renew certificate)")
		fmt.Println("  cert renew [--domain <d>] [--all] (renew expiring certs)")
		fmt.Println("  cert check [--days 30]             (check expiring soon)")
		fmt.Println("  panel-user add --user <u> --pass <p> [--role admin] [--enabled=true|false]")
		os.Exit(2)
	}
}


func cmdServe(st store.SiteStore, cfg *config.Config, paths config.Paths) error {
	srv, err := web.New(cfg, paths, st)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Println("NGM UI listening on:", cfg.API.Listen)
	fmt.Println("Open: http://" + cfg.API.Listen + "/ui/login")
	return srv.Serve(ctx, cfg.API.Listen)
}

func cmdPanelUser(st store.SiteStore, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: panel-user add --user <u> --pass <p> [--role admin] [--enabled=true|false]")
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("panel-user add", flag.ContinueOnError)
		user := fs.String("user", "", "Username")
		pass := fs.String("pass", "", "Password")
		role := fs.String("role", "admin", "Role")
		enabled := fs.Bool("enabled", true, "Enabled")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*user) == "" || *pass == "" {
			return fmt.Errorf("required: --user and --pass")
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(*pass), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		pu, err := st.CreatePanelUser(strings.TrimSpace(*user), string(hash), strings.TrimSpace(*role), *enabled)
		if err != nil {
			return err
		}
		fmt.Println("OK: panel user saved:", pu.Username)
		return nil
	default:
		return fmt.Errorf("unknown panel-user subcommand: %s", args[0])
	}
}




func runStatus(cfg *config.Config, paths config.Paths) {
	fmt.Println("NGM config loaded OK")
	fmt.Printf("Version: %s  BuildTime: %s\n", Version, BuildTime)

	fmt.Println("---- Nginx ----")
	fmt.Printf("root        : %s\n", paths.NginxRoot)
	fmt.Printf("bin         : %s\n", paths.NginxBin)
	fmt.Printf("main_conf   : %s\n", paths.NginxMainConf)
	fmt.Printf("sites_dir   : %s\n", paths.NginxSitesDir)
	fmt.Printf("staging_dir : %s\n", paths.NginxStageDir)
	fmt.Printf("backup_dir  : %s\n", paths.NginxBackupDir)

	mgr := nginx.NewManager(paths.NginxRoot, paths.NginxBin, paths.NginxMainConf, paths.NginxSitesDir, paths.NginxStageDir, paths.NginxBackupDir)
	if err := mgr.EnsureLayout(); err != nil {
		log.Fatalf("nginx layout: %v", err)
	}
	fmt.Println("---- Layout ----")
	fmt.Println("nginx directories ensured (sites/staging/backup)")

	fmt.Println("---- Nginx Test ----")
	if err := mgr.TestConfig(); err != nil {
		log.Fatalf("nginx test: %v", err)
	}
	fmt.Println("nginx config test OK")

	fmt.Println("---- API ----")
	fmt.Printf("listen      : %s\n", cfg.API.Listen)
	fmt.Printf("allow_ips   : %v\n", cfg.API.AllowIPs)

	fmt.Println("---- Certs ----")
	fmt.Printf("mode        : %s\n", cfg.Certs.Mode)
	fmt.Printf("certbot_bin : %s\n", paths.CertbotBin)
	fmt.Printf("webroot     : %s\n", paths.ACMEWebroot)
	fmt.Printf("live_dir    : %s\n", paths.LetsEncryptLive)

	fmt.Println("---- PHP-FPM ----")
	fmt.Printf("default     : %s\n", cfg.PHPFPM.DefaultVersion)
	fmt.Printf("versions    : %d\n", len(cfg.PHPFPM.Versions))
}

func cmdSite(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: site <add|list|rm> ...")
	}

	core, err := app.New(cfg, paths, st)
	if err != nil {
		return err
	}

	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("site add", flag.ContinueOnError)
		var (
			user      = fs.String("user", "", "Owner username")
			domain    = fs.String("domain", "", "Domain (e.g. example.com)")
			mode      = fs.String("mode", "php", "Mode: php|proxy|static")
			phpv      = fs.String("php", cfg.PHPFPM.DefaultVersion, "PHP version (e.g. 8.3)")
			webroot   = fs.String("webroot", "", "Webroot path (optional; default derived from user+domain)")
			http3     = fs.Bool("http3", true, "Enable HTTP/3")
			provision = fs.Bool("provision", true, "Create linux user (if missing) + create site dirs")
			skipCert  = fs.Bool("skip-cert", false, "Skip automatic certificate issuance")
			applyNow  = fs.Bool("apply-now", true, "Apply this vhost immediately (needed for HTTP-01)")
			clientMax = fs.String("client-max-body-size", "", "Nginx client_max_body_size (e.g. 32M, 128M)")
			phpRead   = fs.String("php-time-read", "", "Nginx fastcgi_read_timeout (e.g. 60s, 300s)")
			phpSend   = fs.String("php-time-send", "", "Nginx fastcgi_send_timeout (e.g. 60s, 300s)")

		)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *user == "" || *domain == "" {
			return fmt.Errorf("required: --user and --domain")
		}

		res, err := core.SiteAdd(context.Background(), app.SiteAddRequest{
			User:      *user,
			Domain:    *domain,
			Mode:      *mode,
			PHP:       *phpv,
			Webroot:   *webroot,
			HTTP3:     *http3,
			Provision: *provision,
			SkipCert:  *skipCert,
			ApplyNow:  *applyNow,
			ClientMaxBodySize: *clientMax,
			PHPTimeRead:       *phpRead,
			PHPTimeSend:       *phpSend,
		})
		if err != nil {
			return err
		}

		s := res.Site
		fmt.Println("OK: site saved")
		fmt.Printf("  domain : %s\n", s.Domain)
		fmt.Printf("  user_id: %d\n", s.UserID)
		fmt.Printf("  mode   : %s\n", s.Mode)
		fmt.Printf("  webroot: %s\n", s.Webroot)
		fmt.Printf("  php    : %s\n", s.PHPVersion)
		fmt.Printf("  http3  : %v\n", s.EnableHTTP3)
		for _, w := range res.Warnings {
			fmt.Println("WARNING:", w)
		}
		return nil











	case "list":
		items, err := core.SiteList(context.Background())
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Println("(no sites)")
			return nil
		}

		fmt.Printf("%-25s  %-6s  %-5s  %-9s  %-10s  %-20s  %-40s  %s\n",
			"DOMAIN", "MODE", "HTTP3", "ENABLED", "STATE", "LAST_APPLIED", "WEBROOT", "PHP")

		for _, it := range items {
			s := it.Site
			enabledStr := "yes"
			if !s.Enabled {
				enabledStr = "no"
			}
			fmt.Printf("%-25s  %-6s  %-5v  %-9s  %-10s  %-20s  %-40s  %s\n",
				s.Domain, s.Mode, s.EnableHTTP3, enabledStr, it.State, it.Last, trimLen(s.Webroot, 40), s.PHPVersion)
		}
		return nil




	case "rm":
		fs := flag.NewFlagSet("site rm", flag.ContinueOnError)
		var domain = fs.String("domain", "", "Domain to remove (soft delete)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *domain == "" {
			return fmt.Errorf("required: --domain")
		}
		if err := core.SiteDisable(context.Background(), *domain); err != nil { return err }
                d := strings.ToLower(strings.TrimSpace(*domain))
                fmt.Println("OK: site disabled (pending delete):", d)
		return nil



	case "edit":
		fs := flag.NewFlagSet("site edit", flag.ContinueOnError)
		var (
			domain  = fs.String("domain", "", "Domain (required)")
			user    = fs.String("user", "", "Owner username (optional)")
			mode    = fs.String("mode", "", "Mode: php|proxy|static (optional)")
			phpv    = fs.String("php", "", "PHP version (optional)")
			webroot = fs.String("webroot", "", "Webroot (optional)")
			http3S  = fs.String("http3", "", "Enable HTTP/3: true|false (optional)")
			enS     = fs.String("enabled", "", "Enabled: true|false (optional)")
			applyNow = fs.Bool("apply-now", false, "Apply immediately after edit")
			clientMax = fs.String("client-max-body-size", "", "Nginx client_max_body_size (e.g. 32M, 128M)")
			phpRead   = fs.String("php-time-read", "", "Nginx fastcgi_read_timeout (e.g. 60s, 300s)")
			phpSend   = fs.String("php-time-send", "", "Nginx fastcgi_send_timeout (e.g. 60s, 300s)")
		)
		if err := fs.Parse(args[1:]); err != nil { return err }
		if strings.TrimSpace(*domain) == "" { return fmt.Errorf("required: --domain") }

		var http3 *bool
		if strings.TrimSpace(*http3S) != "" {
			v := strings.EqualFold(strings.TrimSpace(*http3S), "true") || strings.TrimSpace(*http3S) == "1"
			http3 = &v
		}
		var enabled *bool
		if strings.TrimSpace(*enS) != "" {
			v := strings.EqualFold(strings.TrimSpace(*enS), "true") || strings.TrimSpace(*enS) == "1"
			enabled = &v
		}

		updated, err := core.SiteEdit(context.Background(), app.SiteEditRequest{
			Domain: *domain,
			User: *user,
			Mode: *mode,
			PHP: *phpv,
			Webroot: *webroot,
			HTTP3: http3,
			Enabled: enabled,
			ApplyNow: *applyNow,
			ClientMaxBodySize: *clientMax,
			PHPTimeRead:       *phpRead,
			PHPTimeSend:       *phpSend,
		})
		if err != nil { return err }
		fmt.Println("OK: site updated")
		fmt.Printf("  domain : %s\n", updated.Domain)
		fmt.Printf("  user_id: %d\n", updated.UserID)
		fmt.Printf("  mode   : %s\n", updated.Mode)
		fmt.Printf("  webroot: %s\n", updated.Webroot)
		fmt.Printf("  php    : %s\n", updated.PHPVersion)
		fmt.Printf("  http3  : %v\n", updated.EnableHTTP3)
		fmt.Printf("  enabled: %v\n", updated.Enabled)

		return nil





	default:
		return fmt.Errorf("unknown site subcommand: %s", args[0])
	}
}

func cmdCert(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cert <list|info|issue|renew|check> ...")
	}

	core, err := app.New(cfg, paths, st)
	if err != nil { return err }

	switch args[0] {
	case "list":
		certList, err := core.CertList()
		if err != nil {
			return err
		}
		if len(certList) == 0 {
			fmt.Println("(no certificates)")
			return nil
		}

		fmt.Printf("%-30s  %-12s  %-20s  %-20s\n", "DOMAIN", "DAYS LEFT", "NOT BEFORE", "NOT AFTER")
		for _, c := range certList {
			status := fmt.Sprintf("%d days", c.DaysLeft)
			if c.DaysLeft < 0 {
				status = "EXPIRED"
			} else if c.DaysLeft <= 7 {
				status = fmt.Sprintf("%d days (!)", c.DaysLeft)
			}
			fmt.Printf("%-30s  %-12s  %-20s  %-20s\n",
				c.Domain,
				status,
				c.NotBefore.Format("2006-01-02 15:04"),
				c.NotAfter.Format("2006-01-02 15:04"),
			)
		}
		return nil

	case "info":
		fs := flag.NewFlagSet("cert info", flag.ContinueOnError)
		domain := fs.String("domain", "", "Domain")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *domain == "" {
			return fmt.Errorf("required: --domain")
		}

		info, err := core.CertInfo(*domain)
		if err != nil {
			return err
		}

                if info == nil || !info.Exists {
			fmt.Printf("Certificate does not exist for: %s\n", *domain)
			return nil
		}

		fmt.Printf("Domain      : %s\n", info.Domain)
		fmt.Printf("Cert Path   : %s\n", info.CertPath)
		fmt.Printf("Key Path    : %s\n", info.KeyPath)
		fmt.Printf("Not Before  : %s\n", info.NotBefore.Format(time.RFC3339))
		fmt.Printf("Not After   : %s\n", info.NotAfter.Format(time.RFC3339))
		fmt.Printf("Days Left   : %d\n", info.DaysLeft)
		if info.DaysLeft < 0 {
			fmt.Println("Status      : EXPIRED")
		} else if info.DaysLeft <= 7 {
			fmt.Println("Status      : EXPIRING SOON")
		} else if info.DaysLeft <= 30 {
			fmt.Println("Status      : RENEWAL DUE")
		} else {
			fmt.Println("Status      : OK")
		}
		return nil

	case "issue":
		fs := flag.NewFlagSet("cert issue", flag.ContinueOnError)
		domain := fs.String("domain", "", "Domain")
		applyNow := fs.Bool("apply", true, "Re-apply nginx config for this domain after successful issuance")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *domain == "" {
			return fmt.Errorf("required: --domain")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		fmt.Printf("Issuing certificate for %s...\n", *domain)
		if err := core.CertIssue(ctx, *domain, *applyNow); err != nil { return err }
		fmt.Println("Certificate issued successfully!")

		return nil

	case "renew":
		fs := flag.NewFlagSet("cert renew", flag.ContinueOnError)
		domain := fs.String("domain", "", "Domain (optional, renews all if not specified)")
		all := fs.Bool("all", false, "Renew all certificates")
		applyNow := fs.Bool("apply", true, "Reload nginx after renewal")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		if err := core.CertRenew(ctx, strings.TrimSpace(*domain), *all, *applyNow); err != nil { return err }
		fmt.Println("Renewal complete!")
		return nil

	case "check":
		fs := flag.NewFlagSet("cert check", flag.ContinueOnError)
		days := fs.Int("days", 30, "Check for certs expiring within N days")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}

		expiring, err := core.CertCheck(*days)
		if err != nil {
			return err
		}

		if len(expiring) == 0 {
			fmt.Printf("No certificates expiring within %d days.\n", *days)
			return nil
		}

		fmt.Printf("Certificates expiring within %d days:\n\n", *days)
		fmt.Printf("%-30s  %-12s  %-20s\n", "DOMAIN", "DAYS LEFT", "EXPIRES")
		for _, c := range expiring {
			fmt.Printf("%-30s  %-12d  %-20s\n",
				c.Domain,
				c.DaysLeft,
				c.NotAfter.Format("2006-01-02 15:04"),
			)
		}
		return nil

	default:
		return fmt.Errorf("unknown cert subcommand: %s", args[0])
	}
}

func siteState(s store.Site) (state string, last string) {
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

func trimLen(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ensureSelfSignedCert creates a per-domain self-signed cert used only as a bootstrap fallback
// so nginx can start before Let's Encrypt files exist.
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

	// openssl req -x509 -nodes -newkey rsa:2048 -days 7 -subj "/CN=domain" ...
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

	// nginx master is typically root, so 0600 key is OK; cert can be world-readable.
	_ = os.Chmod(certPath, 0644)
	_ = os.Chmod(keyPath, 0600)
	return nil
}

func cmdApply(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	var (
		domain = fs.String("domain", "", "Apply only this domain (optional)")
		all    = fs.Bool("all", false, "Apply all enabled sites (not only pending)")
		dry    = fs.Bool("dry-run", false, "Show what would be applied, do nothing")
		limit  = fs.Int("limit", 0, "Max number of sites to apply (0 = unlimited)")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}



	core, err := app.New(cfg, paths, st)
	if err != nil {
		return err
	}

	res, applyErr := core.Apply(context.Background(), app.ApplyRequest{
		Domain: *domain,
		All:    *all,
		DryRun: *dry,
		Limit:  *limit,
	})

	// CLI-friendly output (kept simple; API/UI will just use the returned structs)
	if *dry {
		for _, r := range res.Domains {
			switch r.Action {
			case "apply":
				fmt.Println("dry-run apply:", r.Domain)
			case "delete":
				fmt.Println("dry-run delete:", r.Domain)
			}
		}
		fmt.Println("dry-run done.")
		return nil
	}

	// Show per-domain failures (if any) before returning error
	for _, r := range res.Domains {
		if r.Status == "fail" {
			fmt.Println("FAIL:", r.Domain, "-", r.Error)
		}
	}

	if applyErr != nil {
		return applyErr
	}

	if len(res.Changed) == 0 {
		fmt.Println("Nothing to apply (no pending changes).")
		return nil
	}

	fmt.Printf("Applied OK (%d): %s\n", len(res.Changed), strings.Join(res.Changed, ", "))
	return nil









}

func applySingle(
	mgr *nginx.Manager,
	st store.SiteStore,
	cfg *config.Config,
	sqlSt *storesqlite.Store,
	buildTD func(store.Site, string) (nginx.SiteTemplateData, error),
	domain string,
	dry bool,
) error {
	d := strings.ToLower(strings.TrimSpace(domain))
	s, err := st.GetSiteByDomain(d)
	if err != nil {
		return fmt.Errorf("get site: %w", err)
	}

	if dry {
		if !s.Enabled {
			fmt.Println("dry-run delete:", d)
			return nil
		}
		fmt.Println("dry-run apply:", d)
		return nil
	}

	if !s.Enabled {
		ok, err := stageDeleteLiveConf(mgr, d, false)
		if err != nil {
			if sqlSt != nil {
				_ = sqlSt.UpdateApplyResult(d, "fail", "delete live conf failed: "+err.Error(), "")
			}
			return err
		}
		if !ok {
			fmt.Println("Nothing to delete for:", d)
			return nil
		}

		if err := mgr.TestConfig(); err != nil {
			rollbackFromBackup(mgr, []string{d})
			_ = mgr.Reload()
			if sqlSt != nil {
				_ = sqlSt.UpdateApplyResult(d, "fail", "nginx -t failed (rolled back): "+err.Error(), "")
			}
			return fmt.Errorf("nginx -t failed (rolled back): %w", err)
		}

		if err := mgr.Reload(); err != nil {
			rollbackFromBackup(mgr, []string{d})
			_ = mgr.Reload()
			if sqlSt != nil {
				_ = sqlSt.UpdateApplyResult(d, "fail", "nginx reload failed (rolled back): "+err.Error(), "")
			}
			return fmt.Errorf("nginx reload failed (rolled back): %w", err)
		}

		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "ok", "", "")
		}
		fmt.Println("deleted OK:", d)
		return nil
	}

	td, err := buildTD(s, d)
	if err != nil {
		return err
	}

	outPath, content, err := mgr.RenderSiteToStaging(td)
	renderHash := ""
	if content != nil {
		renderHash = util.Sha256Hex(content)
	}
	if err != nil {
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", err.Error(), renderHash)
		}
		return fmt.Errorf("render: %w", err)
	}
	fmt.Println("rendered:", outPath)

	changedNow, err := mgr.Publish(d)
	if err != nil {
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", err.Error(), renderHash)
		}
		return fmt.Errorf("publish: %w", err)
	}

	if !changedNow {
		// Nothing changed on disk; we can safely mark as applied without an nginx reload.
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "ok", "", renderHash)
		}
		fmt.Println("applied OK (no changes):", d)
		return nil
	}

	// Single-domain apply must also validate and reload nginx (same as bulk apply).
	if err := mgr.TestConfig(); err != nil {
		rollbackFromBackup(mgr, []string{d})
		_ = mgr.Reload()
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", "nginx -t failed (rolled back): "+err.Error(), renderHash)
		}
		return fmt.Errorf("nginx -t failed (rolled back): %w", err)
	}

	if err := mgr.Reload(); err != nil {
		rollbackFromBackup(mgr, []string{d})
		_ = mgr.Reload()
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", "nginx reload failed (rolled back): "+err.Error(), renderHash)
		}
		return fmt.Errorf("nginx reload failed (rolled back): %w", err)
	}

	if err := mgr.Reload(); err != nil {
		rollbackFromBackup(mgr, []string{d})
		_ = mgr.Reload()
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", "nginx reload failed (rolled back): "+err.Error(), renderHash)
		}
		return fmt.Errorf("nginx reload failed (rolled back): %w", err)
	}

	if sqlSt != nil {
		_ = sqlSt.UpdateApplyResult(d, "ok", "", renderHash)
	}
	fmt.Println("applied OK:", d)
	return nil
}

func stageDeleteLiveConf(mgr *nginx.Manager, domain string, dry bool) (bool, error) {
	live := filepath.Join(mgr.SitesDir, domain+".conf")
	bak := filepath.Join(mgr.BackupDir, domain+".conf.bak")

	if _, err := os.Stat(live); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	if dry {
		return true, nil
	}

	data, err := os.ReadFile(live)
	if err != nil {
		return false, err
	}
	if err := util.WriteFileAtomic(bak, data, 0644); err != nil {
		return false, err
	}
	if err := os.Remove(live); err != nil {
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

func reloadNginx(paths config.Paths) error {
	mgr := nginx.NewManager(
		paths.NginxRoot,
		paths.NginxBin,
		paths.NginxMainConf,
		paths.NginxSitesDir,
		paths.NginxStageDir,
		paths.NginxBackupDir,
	)
	if err := mgr.EnsureLayout(); err != nil {
		return fmt.Errorf("nginx layout: %w", err)
	}
	if err := mgr.TestConfig(); err != nil {
		return fmt.Errorf("nginx -t failed: %w", err)
	}
	if err := mgr.Reload(); err != nil {
		return fmt.Errorf("nginx reload failed: %w", err)
	}
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
