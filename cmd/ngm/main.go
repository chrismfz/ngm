package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"mynginx/internal/config"
	"mynginx/internal/nginx"
	"mynginx/internal/store"
	_ "mynginx/internal/store/mysql"
	storesqlite "mynginx/internal/store/sqlite"
	"mynginx/internal/users"
	"mynginx/internal/util"

	"mynginx/internal/app"
	"mynginx/internal/backup"
	"mynginx/internal/bootstrap"

	"mynginx/internal/web"

	"golang.org/x/crypto/bcrypt"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
)

func isHelpArg(s string) bool {
	return s == "-h" || s == "--help"
}

func shouldPrintHelp(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "help" {
		return true
	}
	return len(args) == 1 && isHelpArg(args[0])
}

func printHelp(args []string) {
	if len(args) == 0 {
		printUsage()
		return
	}
	if isHelpArg(args[0]) {
		printUsage()
		return
	}
	if args[0] != "help" {
		printUsage()
		return
	}
	if len(args) == 1 {
		printUsage()
		return
	}
	switch args[1] {
	case "site":
		printSiteUsage()
	case "dns":
		printDNSUsage()
	case "cert":
		printCertUsage()
	case "panel-user":
		printPanelUserUsage()
	case "package":
		printPackageUsage()
	case "backup":
		printBackupUsage()
	case "restore":
		printRestoreUsage()
	case "provision":
		printProvisionUsage()
	case "admin":
		printAdminUsage()
	default:
		fmt.Printf("Unknown help topic: %s\n\n", args[1])
		printUsage()
	}
}

func printUsage() {
	fmt.Println("NGM CLI")
	fmt.Println("")
	fmt.Println("Usage:")
	fmt.Println("  ngm [global flags] <command> [subcommand] [flags]")
	fmt.Println("")
	fmt.Println("Global flags:")
	fmt.Println("  -c <path>                           Path to config.yaml (default: config.yaml)")
	fmt.Println("  -h, --help                          Show help")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  serve                               Start local UI/API server")
	fmt.Println("  site add|edit|list|rm               Site management")
	fmt.Println("  dns list                            DNS visibility")
	fmt.Println("  apply                               Apply nginx changes")
	fmt.Println("  cert list|info|issue|renew|check    Certificate management")
	fmt.Println("  panel-user add|list|edit|del        Manage admin/reseller/user panel users")
	fmt.Println("  admin add|list|edit|del             Admin alias for panel-user")
	fmt.Println("  package add|list|show|edit|del      Hosting package management")
	fmt.Println("  backup user|reseller|all            Create backups")
	fmt.Println("  restore --file <archive.tar.gz> [--new-user <username>]")
	fmt.Println("  provision init|test                 Bootstrap/test nginx master config")
	fmt.Println("")
	fmt.Println("Roles:")
	fmt.Println("  admin")
	fmt.Println("  reseller")
	fmt.Println("  user")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  ngm panel-user add --user alice --pass secret --role admin")
	fmt.Println("  ngm admin list")
	fmt.Println("  ngm panel-user list --role reseller")
	fmt.Println("  ngm site add --user alice --domain example.com")
	fmt.Println("  ngm backup user --user alice --include-certs")
	fmt.Println("")
	fmt.Println("Run `ngm help <command>` for detailed help.")
}

func printSiteUsage() {
	fmt.Println("Usage: ngm site <add|edit|list|rm> [flags]")
	fmt.Println("Subcommands:")
	fmt.Println("  add     Create a site (root domain or subdomain)")
	fmt.Println("  edit    Update owner/mode/php/webroot/http3/enabled")
	fmt.Println("  list    List sites with parent, mode, state, dns, php and webroot")
	fmt.Println("  rm      Disable a site (soft remove; config removed on apply)")
	fmt.Println("")
	fmt.Println("Domain model:")
	fmt.Println("  root domain: ngm site add --user alice --domain example.com")
	fmt.Println("  subdomain : ngm site add --user alice --domain blog.example.com --parent example.com")
	fmt.Println("")
	fmt.Println("Modes:")
	fmt.Println("  php    = php-fpm upstream for the selected --php version")
	fmt.Println("  proxy  = reverse proxy (add targets during create or later in UI Targets page)")
	fmt.Println("  static = static files only (no php-fpm pool)")
	fmt.Println("")
	fmt.Println("Important flags:")
	fmt.Println("  add  --user --domain [--parent] [--mode php|proxy|static] [--php] [--webroot]")
	fmt.Println("       [--http3] [--provision] [--apply-now] [--skip-cert]")
	fmt.Println("       [--client-max-body-size] [--php-time-read] [--php-time-send]")
	fmt.Println("  edit --domain [--user] [--parent] [--mode] [--php] [--enabled true|false]")
	fmt.Println("       [--http3 true|false] [--apply-now] [--webroot] [--client-max-body-size]")
	fmt.Println("")
	fmt.Println("Examples:")
	fmt.Println("  ngm site add --user alice --domain example.com --mode php --php 8.3")
	fmt.Println("  ngm site add --user alice --domain blog.example.com --parent example.com --mode php --php 8.3")
	fmt.Println("  ngm site add --user alice --domain app.example.com --mode proxy --skip-cert --apply-now=false")
	fmt.Println("  ngm site add --user alice --domain static.example.com --mode static --skip-cert")
	fmt.Println("  ngm site edit --domain example.com --enabled=false")
	fmt.Println("  ngm site list")
	fmt.Println("")
	fmt.Println("Apply/cert guidance:")
	fmt.Println("  If DNS/webroot is not ready yet, prefer --skip-cert or --apply-now=false.")
	fmt.Println("  For proxy mode, ensure at least one target exists before apply-now.")
}

func printDNSUsage() {
	fmt.Println("Usage: ngm dns list [--domain <domain>]")
	fmt.Println("Subcommands:")
	fmt.Println("  list    List DNS status for managed sites")
	fmt.Println("Examples:")
	fmt.Println("  ngm dns list")
	fmt.Println("  ngm dns list --domain example.com")
}

func printCertUsage() {
	fmt.Println("Usage: ngm cert <list|info|issue|renew|check> [flags]")
	fmt.Println("Subcommands: list, info, issue, renew, check")
	fmt.Println("Important flags:")
	fmt.Println("  info  --domain <domain>")
	fmt.Println("  issue --domain <domain> [--apply=true|false]")
	fmt.Println("  renew [--domain <domain>] [--all] [--apply=true|false]")
	fmt.Println("  check [--days 30]")
	fmt.Println("Examples:")
	fmt.Println("  ngm cert list")
	fmt.Println("  ngm cert issue --domain example.com")
	fmt.Println("  ngm cert check --days 14")
}

func printPanelUserUsage() {
	fmt.Println("Usage: ngm panel-user <add|list|edit|del> [flags]")
	fmt.Println("Manages panel users for roles: admin, reseller, user.")
	fmt.Println("Important flags:")
	fmt.Println("  add  --user --pass [--role admin|reseller|user] [--enabled] [--package]")
	fmt.Println("  list [--role admin|reseller|user] [--enabled true|false]")
	fmt.Println("  edit --user [--pass] [--enabled true|false] [--role admin|reseller|user] [--package]")
	fmt.Println("  del  --user")
	fmt.Println("Examples:")
	fmt.Println("  ngm panel-user add --user rootpanel --pass secret --role admin")
	fmt.Println("  ngm panel-user add --user reseller1 --pass secret --role reseller")
	fmt.Println("  ngm panel-user add --user alice --pass secret --role user")
}

func printPackageUsage() {
	fmt.Println("Usage: ngm package <add|list|show|edit|del> [flags]")
	fmt.Println("Subcommands: add, list, show, edit, del")
	fmt.Println("Important flags:")
	fmt.Println("  add  --name <name>")
	fmt.Println("  show --name <name>")
	fmt.Println("  edit --name <name> [--new-name <name>]")
	fmt.Println("  del  --name <name>")
	fmt.Println("Examples:")
	fmt.Println("  ngm package add --name starter")
	fmt.Println("  ngm package list")
	fmt.Println("  ngm package edit --name starter --new-name basic")
}

func printBackupUsage() {
	fmt.Println("Usage: ngm backup <user|reseller|all> [flags]")
	fmt.Println("Scopes: user, reseller, all")
	fmt.Println("Important flags:")
	fmt.Println("  --user <username>        Required for user/reseller scopes")
	fmt.Println("  --output <file.tar.gz>   Optional output path")
	fmt.Println("  --include-certs          Include certificate files")
	fmt.Println("Examples:")
	fmt.Println("  ngm backup user --user alice --include-certs")
	fmt.Println("  ngm backup reseller --user reseller1")
	fmt.Println("  ngm backup all")
}

func printRestoreUsage() {
	fmt.Println("Usage: ngm restore --file <archive.tar.gz> [--new-user <username>]")
	fmt.Println("Important flags:")
	fmt.Println("  --file <archive.tar.gz>  Backup archive to restore (required)")
	fmt.Println("  --new-user <username>    Override username for user-scoped backups")
	fmt.Println("Examples:")
	fmt.Println("  ngm restore --file ngm-backup-user-alice-20260101T120000Z.tar.gz")
	fmt.Println("  ngm restore --file backup.tar.gz --new-user alice2")
}

func printAdminUsage() {
	fmt.Println("Usage: ngm admin <add|list|edit|del> [flags]")
	fmt.Println("Thin alias around `panel-user` with role=admin expectations.")
	fmt.Println("Important flags:")
	fmt.Println("  add  --user --pass [--enabled] [--package]")
	fmt.Println("  list [--enabled true|false]")
	fmt.Println("  edit --user [--pass] [--enabled true|false] [--package]")
	fmt.Println("  del  --user")
	fmt.Println("Examples:")
	fmt.Println("  ngm admin add --user rootpanel --pass secret")
	fmt.Println("  ngm admin list")
	fmt.Println("  ngm admin edit --user rootpanel --enabled=false")
}

func printProvisionUsage() {
	fmt.Println("Usage: ngm provision <init|test>")
	fmt.Println("Subcommands: init, test")
	fmt.Println("Examples:")
	fmt.Println("  ngm provision init")
	fmt.Println("  ngm provision test")
}

func main() {
	os.Exit(run())
}

func run() int {
	var cfgPath string
	fs := flag.NewFlagSet("ngm", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfgPath, "c", "config.yaml", "Path to config.yaml")
	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == flag.ErrHelp {
			printUsage()
			return 0
		}
		fmt.Fprintf(os.Stderr, "flags: %v\n\n", err)
		printUsage()
		return 2
	}
	args := fs.Args()
	if shouldPrintHelp(args) {
		printHelp(args)
		return 0
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	paths := cfg.ResolvePaths()

	if len(args) > 0 && args[0] == "provision" {
		if err := cmdProvision(cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "provision: %v\n", err)
			return 1
		}
		return 0
	}

	// Open store early (for CLI commands)
	st, err := store.Open(cfg.Storage.Driver, cfg.Storage.DSN)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := st.Migrate(); err != nil {
		fmt.Fprintf(os.Stderr, "store migrate: %v\n", err)
		return 1
	}

	if len(args) == 0 {
		runStatus(cfg, paths)
		return 0
	}

	switch args[0] {
	case "serve":
		if err := cmdServe(st, cfg, paths); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			return 1
		}

	case "site":
		if err := cmdSite(st, cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "site: %v\n", err)
			return 1
		}
	case "apply":
		if err := cmdApply(st, cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "apply: %v\n", err)
			return 1
		}

	case "cert":
		if err := cmdCert(st, cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "cert: %v\n", err)
			return 1
		}
	case "dns":
		if err := cmdDNS(st, cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "dns: %v\n", err)
			return 1
		}

	case "panel-user":
		if err := cmdPanelUser(st, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "panel-user: %v\n", err)
			return 1
		}
	case "admin":
		if err := cmdAdmin(st, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "admin: %v\n", err)
			return 1
		}
	case "package":
		if err := cmdPackage(st, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "package: %v\n", err)
			return 1
		}
	case "backup":
		if err := cmdBackup(st, cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "backup: %v\n", err)
			return 1
		}
	case "restore":
		if err := cmdRestore(st, cfg, paths, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "restore: %v\n", err)
			return 1
		}

	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", args[0])
		printUsage()
		return 2
	}
	return 0
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

func cmdProvision(cfg *config.Config, paths config.Paths, args []string) error {
	if len(args) == 0 {
		printProvisionUsage()
		return nil
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printProvisionUsage()
		return nil
	}
	switch args[0] {
	case "init":
		return bootstrap.Init(cfg, paths)
	case "test":
		return bootstrap.Test(cfg, paths)
	default:
		return fmt.Errorf("unknown provision subcommand: %s", args[0])
	}
}

func cmdPanelUser(st store.SiteStore, args []string) error {
	if len(args) == 0 {
		printPanelUserUsage()
		return nil
	}
	switch args[0] {
	case "add":
		return cmdPanelUserAdd(st, args[1:], "")
	case "list":
		return cmdPanelUserList(st, args[1:], "")
	case "edit":
		return cmdPanelUserEdit(st, args[1:], "", false)
	case "del":
		return cmdPanelUserDel(st, args[1:], "", false)
	case "help", "-h", "--help":
		printPanelUserUsage()
		return nil
	default:
		return fmt.Errorf("unknown panel-user subcommand: %s", args[0])
	}
}

func cmdAdmin(st store.SiteStore, args []string) error {
	if len(args) == 0 {
		printAdminUsage()
		return nil
	}
	switch args[0] {
	case "add":
		return cmdPanelUserAdd(st, args[1:], "admin")
	case "list":
		return cmdPanelUserList(st, args[1:], "admin")
	case "edit":
		return cmdPanelUserEdit(st, args[1:], "admin", true)
	case "del":
		return cmdPanelUserDel(st, args[1:], "admin", true)
	case "help", "-h", "--help":
		printAdminUsage()
		return nil
	default:
		return fmt.Errorf("unknown admin subcommand: %s", args[0])
	}
}

func cmdPanelUserAdd(st store.SiteStore, args []string, forcedRole string) error {
	fs := flag.NewFlagSet("panel-user add", flag.ContinueOnError)
	user := fs.String("user", "", "Username")
	pass := fs.String("pass", "", "Password")
	role := fs.String("role", "admin", "Role (admin|reseller|user)")
	enabled := fs.Bool("enabled", true, "Enabled")
	reseller := fs.Int64("reseller", 0, "Reseller panel user ID")
	pkgID := fs.Int64("package", 0, "Package ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*user) == "" || *pass == "" {
		return fmt.Errorf("required: --user and --pass")
	}

	finalRole := strings.ToLower(strings.TrimSpace(*role))
	if forcedRole != "" {
		finalRole = forcedRole
	}
	if err := validatePanelRole(finalRole); err != nil {
		return err
	}

	passwordHash := "$SHADOW$"
	if finalRole != "user" {
		hash, err := bcrypt.GenerateFromPassword([]byte(*pass), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		passwordHash = string(hash)
	}
	pu, err := st.CreatePanelUser(strings.TrimSpace(*user), passwordHash, finalRole, *enabled)
	if err != nil {
		return err
	}
	if finalRole == "user" {
		pu.SystemUser = pu.Username
		if *reseller > 0 {
			pu.ResellerID = reseller
			pu.OwnerID = reseller
		}
		if _, err := st.UpdatePanelUser(pu); err != nil {
			return err
		}
		home := filepath.Join("/home", pu.Username)
		if err := users.CreateSystemUser(pu.Username, home); err != nil {
			return err
		}
		if err := users.SetSystemPassword(pu.Username, *pass); err != nil {
			return err
		}
	}
	if *pkgID > 0 {
		if err := st.AssignPackage(pu.ID, *pkgID, pu.ID); err != nil {
			return err
		}
	}
	fmt.Println("OK: panel user saved:", pu.Username)
	return nil
}

func cmdPanelUserList(st store.SiteStore, args []string, forcedRole string) error {
	fs := flag.NewFlagSet("panel-user list", flag.ContinueOnError)
	role := fs.String("role", "", "Filter by role: admin|reseller|user")
	enabled := fs.String("enabled", "", "Filter by enabled: true|false")
	if err := fs.Parse(args); err != nil {
		return err
	}
	roleFilter := strings.ToLower(strings.TrimSpace(*role))
	if forcedRole != "" {
		roleFilter = forcedRole
	}
	if roleFilter != "" {
		if err := validatePanelRole(roleFilter); err != nil {
			return err
		}
	}
	var enabledFilter *bool
	if strings.TrimSpace(*enabled) != "" {
		v, err := parseBoolArg(*enabled)
		if err != nil {
			return err
		}
		enabledFilter = &v
	}

	items, err := st.ListPanelUsers()
	if err != nil {
		return err
	}
	for _, u := range items {
		if roleFilter != "" && u.Role != roleFilter {
			continue
		}
		if enabledFilter != nil && u.Enabled != *enabledFilter {
			continue
		}
		fmt.Printf("%d\t%s\t%s\tenabled=%v\n", u.ID, u.Username, u.Role, u.Enabled)
	}
	return nil
}

func cmdPanelUserEdit(st store.SiteStore, args []string, forcedRole string, requireExistingRole bool) error {
	fs := flag.NewFlagSet("panel-user edit", flag.ContinueOnError)
	user := fs.String("user", "", "Username")
	pass := fs.String("pass", "", "Password")
	enabled := fs.String("enabled", "", "true|false")
	role := fs.String("role", "", "Role: admin|reseller|user")
	pkgID := fs.Int64("package", 0, "Package ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *user == "" {
		return fmt.Errorf("--user is required")
	}
	pu, err := st.GetPanelUserByUsername(*user)
	if err != nil {
		return err
	}
	if requireExistingRole && pu.Role != forcedRole {
		return fmt.Errorf("panel user %q is role=%s (expected %s)", pu.Username, pu.Role, forcedRole)
	}
	if *enabled != "" {
		v, err := parseBoolArg(*enabled)
		if err != nil {
			return err
		}
		pu.Enabled = v
	}

	targetRole := pu.Role
	if strings.TrimSpace(*role) != "" {
		targetRole = strings.ToLower(strings.TrimSpace(*role))
	}
	if forcedRole != "" {
		targetRole = forcedRole
	}
	if err := validatePanelRole(targetRole); err != nil {
		return err
	}
	if pu.Role != targetRole {
		if pu.Role == "user" && targetRole != "user" {
			return fmt.Errorf("role transition %s -> %s is not supported by this patch; user system accounts are preserved", pu.Role, targetRole)
		}
		if pu.Role != "user" && targetRole == "user" {
			systemUser := strings.TrimSpace(pu.SystemUser)
			if systemUser == "" {
				systemUser = pu.Username
			}
			if !users.UserExists(systemUser) {
				return fmt.Errorf("cannot change %s -> user for %q: linux account %q does not exist", pu.Role, pu.Username, systemUser)
			}
			pu.SystemUser = systemUser
			pu.PasswordHash = "$SHADOW$"
		}
		pu.Role = targetRole
	}

	if *pass != "" {
		if pu.Role == "user" {
			sysUser := strings.TrimSpace(pu.SystemUser)
			if sysUser == "" {
				sysUser = pu.Username
			}
			if err := users.SetSystemPassword(sysUser, *pass); err != nil {
				return err
			}
			pu.SystemUser = sysUser
			pu.PasswordHash = "$SHADOW$"
		} else {
			hash, err := bcrypt.GenerateFromPassword([]byte(*pass), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			pu.PasswordHash = string(hash)
		}
	}
	if _, err := st.UpdatePanelUser(pu); err != nil {
		return err
	}
	if *pkgID > 0 {
		if err := st.AssignPackage(pu.ID, *pkgID, pu.ID); err != nil {
			return err
		}
	}
	fmt.Println("OK: panel user updated:", pu.Username)
	return nil
}

func cmdPanelUserDel(st store.SiteStore, args []string, forcedRole string, requireExistingRole bool) error {
	fs := flag.NewFlagSet("panel-user del", flag.ContinueOnError)
	user := fs.String("user", "", "Username")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*user) == "" {
		return fmt.Errorf("--user is required")
	}
	pu, err := st.GetPanelUserByUsername(*user)
	if err != nil {
		return err
	}
	if requireExistingRole && pu.Role != forcedRole {
		return fmt.Errorf("panel user %q is role=%s (expected %s)", pu.Username, pu.Role, forcedRole)
	}
	_ = st.UnassignPackage(pu.ID)
	if err := st.DeletePanelUser(pu.ID); err != nil {
		return err
	}
	if pu.Role == "user" && pu.SystemUser != "" {
		_ = users.DeleteSystemUser(pu.SystemUser)
	}
	fmt.Println("OK: panel user deleted:", pu.Username)
	return nil
}

func parseBoolArg(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q, expected true|false", v)
	}
}

func validatePanelRole(role string) error {
	switch role {
	case "admin", "reseller", "user":
		return nil
	default:
		return fmt.Errorf("invalid role %q, expected admin|reseller|user", role)
	}
}

func cmdPackage(st store.SiteStore, args []string) error {
	if len(args) == 0 {
		printPackageUsage()
		return nil
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printPackageUsage()
		return nil
	}
	switch args[0] {
	case "list":
		items, err := st.ListPackages()
		if err != nil {
			return err
		}
		for _, p := range items {
			fmt.Printf("%d\t%s\n", p.ID, p.Name)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("package add", flag.ContinueOnError)
		name := fs.String("name", "", "Package name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		p, err := st.CreatePackage(store.Package{Name: *name, MaxDomains: 5, MaxSubdomains: 20, MaxDiskMB: 1024, MaxPHPWorkers: 5, MaxMySQLDBs: 1, MaxMySQLUsers: 2})
		if err != nil {
			return err
		}
		fmt.Println("OK: package added:", p.Name)
		return nil
	case "show":
		fs := flag.NewFlagSet("package show", flag.ContinueOnError)
		name := fs.String("name", "", "Package name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		items, _ := st.ListPackages()
		for _, p := range items {
			if p.Name == *name {
				fmt.Printf("%+v\n", p)
			}
		}
		return nil
	case "edit":
		fs := flag.NewFlagSet("package edit", flag.ContinueOnError)
		name := fs.String("name", "", "Package name")
		newName := fs.String("new-name", "", "New package name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		items, _ := st.ListPackages()
		for _, p := range items {
			if p.Name == *name {
				if *newName != "" {
					p.Name = *newName
				}
				_, err := st.UpdatePackage(p)
				return err
			}
		}
		return fmt.Errorf("package not found")
	case "del":
		fs := flag.NewFlagSet("package del", flag.ContinueOnError)
		name := fs.String("name", "", "Package name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		items, _ := st.ListPackages()
		for _, p := range items {
			if p.Name == *name {
				return st.DeletePackage(p.ID)
			}
		}
		return fmt.Errorf("package not found")
	default:
		return fmt.Errorf("unknown package subcommand: %s", args[0])
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
	mgr.SetControlMode(cfg.Nginx.Apply.ReloadMode, cfg.Nginx.ServiceName)
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
		printSiteUsage()
		return nil
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printSiteUsage()
		return nil
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
			parent    = fs.String("parent", "", "Parent/root domain for subdomain sites")
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
		fs.Usage = func() {
			fmt.Println("Usage: ngm site add --user <username> --domain <domain> [flags]")
			fmt.Println("Flags:")
			fs.PrintDefaults()
		}
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		if *user == "" || *domain == "" {
			return fmt.Errorf("required: --user and --domain")
		}

		res, err := core.SiteAdd(context.Background(), app.SiteAddRequest{
			User:              *user,
			Domain:            *domain,
			ParentDomain:      *parent,
			Mode:              *mode,
			PHP:               *phpv,
			Webroot:           *webroot,
			HTTP3:             *http3,
			Provision:         *provision,
			SkipCert:          *skipCert,
			ApplyNow:          *applyNow,
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

		fmt.Printf("%-25s  %-25s  %-8s  %-6s  %-5s  %-9s  %-10s  %-20s  %-40s  %s\n",
			"DOMAIN", "PARENT", "DNS", "MODE", "HTTP3", "ENABLED", "STATE", "LAST_APPLIED", "WEBROOT", "PHP")

		for _, it := range items {
			s := it.Site
			enabledStr := "yes"
			if !s.Enabled {
				enabledStr = "no"
			}
			parent := "-"
			if s.ParentDomain != nil && strings.TrimSpace(*s.ParentDomain) != "" {
				parent = *s.ParentDomain
			}
			fmt.Printf("%-25s  %-25s  %-8s  %-6s  %-5v  %-9s  %-10s  %-20s  %-40s  %s\n",
				s.Domain, parent, it.DNSStatus, s.Mode, s.EnableHTTP3, enabledStr, it.State, it.Last, trimLen(s.Webroot, 40), s.PHPVersion)
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
		if err := core.SiteDisable(context.Background(), *domain); err != nil {
			return err
		}
		d := strings.ToLower(strings.TrimSpace(*domain))
		fmt.Println("OK: site disabled (pending delete):", d)
		return nil

	case "edit":
		fs := flag.NewFlagSet("site edit", flag.ContinueOnError)
		var (
			domain    = fs.String("domain", "", "Domain (required)")
			user      = fs.String("user", "", "Owner username (optional)")
			parent    = fs.String("parent", "", "Parent/root domain (optional, empty clears to root)")
			mode      = fs.String("mode", "", "Mode: php|proxy|static (optional)")
			phpv      = fs.String("php", "", "PHP version (optional)")
			webroot   = fs.String("webroot", "", "Webroot (optional)")
			http3S    = fs.String("http3", "", "Enable HTTP/3: true|false (optional)")
			enS       = fs.String("enabled", "", "Enabled: true|false (optional)")
			applyNow  = fs.Bool("apply-now", false, "Apply immediately after edit")
			clientMax = fs.String("client-max-body-size", "", "Nginx client_max_body_size (e.g. 32M, 128M)")
			phpRead   = fs.String("php-time-read", "", "Nginx fastcgi_read_timeout (e.g. 60s, 300s)")
			phpSend   = fs.String("php-time-send", "", "Nginx fastcgi_send_timeout (e.g. 60s, 300s)")
		)
		fs.Usage = func() {
			fmt.Println("Usage: ngm site edit --domain <domain> [flags]")
			fmt.Println("Flags:")
			fs.PrintDefaults()
		}
		if err := fs.Parse(args[1:]); err != nil {
			if err == flag.ErrHelp {
				return nil
			}
			return err
		}
		if strings.TrimSpace(*domain) == "" {
			return fmt.Errorf("required: --domain")
		}

		var http3 *bool
		if strings.TrimSpace(*http3S) != "" {
			v := strings.EqualFold(strings.TrimSpace(*http3S), "true") || strings.TrimSpace(*http3S) == "1"
			http3 = &v
		}
		var enabled *bool
		parentSet := false
		if strings.TrimSpace(*enS) != "" {
			v := strings.EqualFold(strings.TrimSpace(*enS), "true") || strings.TrimSpace(*enS) == "1"
			enabled = &v
		}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "parent" {
				parentSet = true
			}
		})

		updated, err := core.SiteEdit(context.Background(), app.SiteEditRequest{
			Domain:            *domain,
			User:              *user,
			ParentDomain:      *parent,
			ParentDomainSet:   parentSet,
			Mode:              *mode,
			PHP:               *phpv,
			Webroot:           *webroot,
			HTTP3:             http3,
			Enabled:           enabled,
			ApplyNow:          *applyNow,
			ClientMaxBodySize: *clientMax,
			PHPTimeRead:       *phpRead,
			PHPTimeSend:       *phpSend,
		})
		if err != nil {
			return err
		}
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

func cmdDNS(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	if len(args) == 0 {
		printDNSUsage()
		return nil
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printDNSUsage()
		return nil
	}
	core, err := app.New(cfg, paths, st)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("dns list", flag.ContinueOnError)
		domain := fs.String("domain", "", "Optional domain filter")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		entries, err := core.DNSList(context.Background(), *domain)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("(no dns entries)")
			return nil
		}
		fmt.Printf("%-32s %-25s %-18s %-10s %-20s %s\n", "FQDN", "ZONE", "KIND", "STATUS", "SUMMARY", "ZONE_FILE")
		for _, e := range entries {
			summary := "-"
			if len(e.RecordText) > 0 {
				summary = strings.Join(e.RecordText, "; ")
			}
			fmt.Printf("%-32s %-25s %-18s %-10s %-20s %s\n", trimLen(e.FQDN, 32), e.Zone, e.Kind, e.Status, trimLen(summary, 20), trimLen(e.ZoneFile, 80))
		}
		return nil
	default:
		return fmt.Errorf("unknown dns subcommand: %s", args[0])
	}
}

func cmdCert(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	if len(args) == 0 {
		printCertUsage()
		return nil
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printCertUsage()
		return nil
	}

	core, err := app.New(cfg, paths, st)
	if err != nil {
		return err
	}

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
		if err := core.CertIssue(ctx, *domain, *applyNow); err != nil {
			return err
		}
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

		if err := core.CertRenew(ctx, strings.TrimSpace(*domain), *all, *applyNow); err != nil {
			return err
		}
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
			_ = mgr.ReloadOrStart()
			if sqlSt != nil {
				_ = sqlSt.UpdateApplyResult(d, "fail", "nginx -t failed (rolled back): "+err.Error(), "")
			}
			return fmt.Errorf("nginx -t failed (rolled back): %w", err)
		}

		if err := mgr.ReloadOrStart(); err != nil {
			rollbackFromBackup(mgr, []string{d})
			_ = mgr.ReloadOrStart()
			if sqlSt != nil {
				_ = sqlSt.UpdateApplyResult(d, "fail", "nginx reload/start failed (rolled back): "+err.Error(), "")
			}
			return fmt.Errorf("nginx reload/start failed (rolled back): %w", err)
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
		_ = mgr.ReloadOrStart()
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", "nginx -t failed (rolled back): "+err.Error(), renderHash)
		}
		return fmt.Errorf("nginx -t failed (rolled back): %w", err)
	}

	if err := mgr.ReloadOrStart(); err != nil {
		rollbackFromBackup(mgr, []string{d})
		_ = mgr.ReloadOrStart()
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", "nginx reload/start failed (rolled back): "+err.Error(), renderHash)
		}
		return fmt.Errorf("nginx reload/start failed (rolled back): %w", err)
	}

	if err := mgr.ReloadOrStart(); err != nil {
		rollbackFromBackup(mgr, []string{d})
		_ = mgr.ReloadOrStart()
		if sqlSt != nil {
			_ = sqlSt.UpdateApplyResult(d, "fail", "nginx reload/start failed (rolled back): "+err.Error(), renderHash)
		}
		return fmt.Errorf("nginx reload/start failed (rolled back): %w", err)
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
	if err := mgr.ReloadOrStart(); err != nil {
		return fmt.Errorf("nginx reload/start failed: %w", err)
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

func cmdBackup(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	if len(args) == 0 {
		printBackupUsage()
		return nil
	}
	if isHelpArg(args[0]) || args[0] == "help" {
		printBackupUsage()
		return nil
	}
	sub := args[0]
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	username := fs.String("user", "", "Username for user/reseller scope")
	output := fs.String("output", "", "Output tar.gz file")
	includeCerts := fs.Bool("include-certs", false, "Include certificate files")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	scope := backup.ScopeAll
	switch sub {
	case "user":
		scope = backup.ScopeUser
		if strings.TrimSpace(*username) == "" {
			return fmt.Errorf("--user is required for backup user")
		}
	case "reseller":
		scope = backup.ScopeReseller
		if strings.TrimSpace(*username) == "" {
			return fmt.Errorf("--user is required for backup reseller")
		}
	case "all":
		scope = backup.ScopeAll
	default:
		return fmt.Errorf("unknown backup scope: %s", sub)
	}
	outPath := strings.TrimSpace(*output)
	if outPath == "" {
		ts := time.Now().UTC().Format("20060102T150405Z")
		subject := "all"
		if scope != backup.ScopeAll {
			subject = strings.TrimSpace(*username)
		}
		outPath = fmt.Sprintf("ngm-backup-%s-%s-%s.tar.gz", scope, subject, ts)
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	host, _ := os.Hostname()
	_, err = backup.Create(st, backup.BackupOptions{
		Scope:        scope,
		Username:     strings.TrimSpace(*username),
		IncludeCerts: *includeCerts,
		NodeID:       host,
		Driver:       cfg.Storage.Driver,
		HomeRoot:     cfg.Hosting.HomeRoot,
		CertsRoot:    paths.LetsEncryptLive,
		Now:          time.Now().UTC(),
	}, f)
	if err != nil {
		return err
	}
	fmt.Println("Backup created:", outPath)
	return nil
}

func cmdRestore(st store.SiteStore, cfg *config.Config, paths config.Paths, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	file := fs.String("file", "", "Backup file")
	newUser := fs.String("new-user", "", "Override username for user-scoped backup")
	if len(args) > 0 && (isHelpArg(args[0]) || args[0] == "help") {
		printRestoreUsage()
		return nil
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*file) == "" {
		return fmt.Errorf("--file is required")
	}
	core, err := app.New(cfg, paths, st)
	if err != nil {
		return err
	}
	res, err := backup.Restore(st, backup.RestoreOptions{
		FilePath:  strings.TrimSpace(*file),
		NewUser:   strings.TrimSpace(*newUser),
		HomeRoot:  cfg.Hosting.HomeRoot,
		CertsRoot: paths.LetsEncryptLive,
	}, func() error {
		_, err := core.Apply(context.Background(), app.ApplyRequest{All: true})
		return err
	})
	if err != nil {
		return err
	}
	fmt.Printf("Restore complete: users=%d sites=%d files=%d certs=%d warnings=%d\n", res.Users, res.Sites, res.SiteFileCount, res.CertFileCount, len(res.Warnings))
	for _, w := range res.Warnings {
		fmt.Println("WARN:", w)
	}
	return nil
}
