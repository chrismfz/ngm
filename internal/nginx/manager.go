package nginx

import (
	"bytes"
	_ "embed"
	"fmt"
	"mynginx/internal/util"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"
)

type Manager struct {
	Root        string
	Bin         string
	MainConf    string
	SitesDir    string
	StageDir    string
	BackupDir   string
	ReloadMode  string
	ServiceName string
}

func NewManager(root, bin, mainConf, sitesDir, stageDir, backupDir string) *Manager {
	return &Manager{
		Root:        root,
		Bin:         bin,
		MainConf:    mainConf,
		SitesDir:    sitesDir,
		StageDir:    stageDir,
		BackupDir:   backupDir,
		ReloadMode:  "signal",
		ServiceName: "nginx",
	}
}

func (m *Manager) SetControlMode(mode, serviceName string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "signal"
	}
	m.ReloadMode = mode
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "nginx"
	}
	m.ServiceName = serviceName
}

// EnsureLayout creates the required directories for generated configs.
// It does NOT write configs yet.
func (m *Manager) EnsureLayout() error {
	dirs := []string{
		m.SitesDir,
		m.StageDir,
		m.BackupDir,
	}

	for _, d := range dirs {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}

	// Optional: create a dedicated staging sites directory so we can stage safely later
	stageSites := filepath.Join(m.StageDir, "sites")
	if err := os.MkdirAll(stageSites, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", stageSites, err)
	}

	return nil
}

type CmdOutputError struct {
	Cmd    string
	Stdout string
	Stderr string
	Err    error
}

type PortListener struct {
	Port    int
	Process string
	PID     int
	Command string
}

func (e *CmdOutputError) Error() string {
	// keep it readable in UI and logs
	out := strings.TrimSpace(e.Stdout)
	er := strings.TrimSpace(e.Stderr)
	msg := e.Cmd + ": " + e.Err.Error()
	if er != "" {
		msg += "\n--- stderr ---\n" + er
	}
	if out != "" {
		msg += "\n--- stdout ---\n" + out
	}
	return msg
}

// apply test config
func (m *Manager) TestConfig() error {
	// Use -c explicitly to avoid relying on cwd/defaults.
	res, err := util.Run(10*time.Second, m.Bin, "-t", "-c", m.MainConf)

	if err != nil {
		return &CmdOutputError{
			Cmd:    m.Bin + " -t -c " + m.MainConf,
			Stdout: res.Stdout,
			Stderr: res.Stderr,
			Err:    err,
		}
	}
	return nil
}

// RemoveLiveSite removes the live vhost file and keeps a backup in BackupDir.
// It does NOT reload. Batch apply will Test+Reload once at the end.
func (m *Manager) RemoveLiveSite(domain string) error {
	dst := filepath.Join(m.SitesDir, domain+".conf")
	bak := filepath.Join(m.BackupDir, domain+".conf.bak")

	// nothing to remove
	if _, err := os.Stat(dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat live %s: %w", dst, err)
	}

	// backup existing
	old, err := os.ReadFile(dst)
	if err != nil {
		return fmt.Errorf("read live %s: %w", dst, err)
	}
	if err := util.WriteFileAtomic(bak, old, 0644); err != nil {
		return fmt.Errorf("write backup %s: %w", bak, err)
	}

	// remove live
	if err := os.Remove(dst); err != nil {
		return fmt.Errorf("remove live %s: %w", dst, err)
	}
	return nil
}

func (m *Manager) RenderSiteToStaging(site SiteTemplateData) (string, []byte, error) {
	if site.Domain == "" {
		return "", nil, fmt.Errorf("site.Domain is required")
	}
	if site.Mode == "" {
		site.Mode = "php"
	}
	if site.ACMEWebroot == "" {
		return "", nil, fmt.Errorf("site.ACMEWebroot is required")
	}
	if site.Webroot == "" {
		return "", nil, fmt.Errorf("site.Webroot is required")
	}
	if site.TLSCert == "" || site.TLSKey == "" {
		return "", nil, fmt.Errorf("site TLSCert/TLSKey are required")
	}

	site.UpstreamKey = MakeUpstreamKey(site.Domain)

	tpl, err := template.New("site.tmpl").Parse(siteTemplate)
	if err != nil {
		return "", nil, fmt.Errorf("parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, site); err != nil {
		return "", nil, fmt.Errorf("execute template: %w", err)
	}

	outDir := filepath.Join(m.StageDir, "sites")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", nil, fmt.Errorf("mkdir %s: %w", outDir, err)
	}

	outPath := filepath.Join(outDir, site.Domain+".conf")
	if err := util.WriteFileAtomic(outPath, buf.Bytes(), 0644); err != nil {
		return "", nil, err
	}
	return outPath, buf.Bytes(), nil
}

//go:embed templates/site.tmpl
var siteTemplate string

// Publish copies a staged site config into the live sites directory.
// It creates/updates a backup file when the live file exists.
// It returns changed=false if the live file already matches the staged content.
func (m *Manager) Publish(domain string) (bool, error) {
	if domain == "" {
		return false, fmt.Errorf("domain is required")
	}

	src := filepath.Join(m.StageDir, "sites", domain+".conf")
	dst := filepath.Join(m.SitesDir, domain+".conf")
	bak := filepath.Join(m.BackupDir, domain+".conf.bak")

	data, err := os.ReadFile(src)
	if err != nil {
		return false, fmt.Errorf("read staging %s: %w", src, err)
	}

	// If live exists and content is identical, skip publish.
	if live, err := os.ReadFile(dst); err == nil {
		if bytes.Equal(live, data) {
			return false, nil
		}
	}

	// Backup current live file (if exists)
	if _, err := os.Stat(dst); err == nil {
		old, err := os.ReadFile(dst)
		if err != nil {
			return false, fmt.Errorf("read live %s: %w", dst, err)
		}
		if err := util.WriteFileAtomic(bak, old, 0644); err != nil {
			return false, fmt.Errorf("write backup %s: %w", bak, err)
		}
	}

	// Publish new file atomically
	if err := util.WriteFileAtomic(dst, data, 0644); err != nil {
		return false, fmt.Errorf("publish %s: %w", dst, err)
	}

	return true, nil
}

func (m *Manager) Reload() error {
	switch m.ReloadMode {
	case "systemd":
		return m.reloadSystemd()
	default:
		return m.reloadSignal()
	}
}

func (m *Manager) Start() error {
	switch m.ReloadMode {
	case "systemd":
		return m.startSystemd()
	default:
		return m.startSignal()
	}
}

func (m *Manager) ReloadOrStart() error {
	switch m.ReloadMode {
	case "systemd":
		active, err := m.systemdIsActive()
		if err != nil {
			return err
		}
		if active {
			return m.reloadSystemd()
		}
		fmt.Printf("nginx service %q inactive, attempting start...\n", m.ServiceName)
		return m.startSystemd()
	default:
		if err := m.reloadSignal(); err != nil {
			if !isReloadRecoverable(err.Error()) {
				return err
			}
			fmt.Println("nginx reload failed; service inactive, attempting start...")
			if startErr := m.startSignal(); startErr != nil {
				return fmt.Errorf("nginx start failed after reload fallback: %w", startErr)
			}
		}
		return nil
	}
}

func (m *Manager) reloadSignal() error {
	// Try reload first.
	res, err := util.Run(10*time.Second, m.Bin, "-s", "reload")
	if res.Stdout != "" {
		fmt.Print(res.Stdout)
	}
	if res.Stderr != "" {
		fmt.Print(res.Stderr)
	}
	if err == nil {
		return nil
	}
	return &CmdOutputError{
		Cmd:    m.Bin + " -s reload",
		Stdout: res.Stdout,
		Stderr: res.Stderr,
		Err:    err,
	}
}

func (m *Manager) startSignal() error {
	if err := m.preflightUnmanagedNginxListeners(); err != nil {
		return err
	}
	res, err := util.Run(10*time.Second, m.Bin, "-c", m.MainConf)
	if res.Stdout != "" {
		fmt.Print(res.Stdout)
	}
	if res.Stderr != "" {
		fmt.Print(res.Stderr)
	}
	if err != nil {
		return &CmdOutputError{
			Cmd:    m.Bin + " -c " + m.MainConf,
			Stdout: res.Stdout,
			Stderr: res.Stderr,
			Err:    err,
		}
	}
	return nil
}

func (m *Manager) reloadSystemd() error {
	res, err := util.Run(10*time.Second, "systemctl", "reload", m.ServiceName)
	if res.Stdout != "" {
		fmt.Print(res.Stdout)
	}
	if res.Stderr != "" {
		fmt.Print(res.Stderr)
	}
	if err != nil {
		return &CmdOutputError{
			Cmd:    "systemctl reload " + m.ServiceName,
			Stdout: res.Stdout,
			Stderr: res.Stderr,
			Err:    fmt.Errorf("systemctl reload %s failed: %w", m.ServiceName, err),
		}
	}
	return nil
}

func (m *Manager) startSystemd() error {
	if err := m.preflightUnmanagedNginxListeners(); err != nil {
		return err
	}
	res, err := util.Run(10*time.Second, "systemctl", "start", m.ServiceName)
	if res.Stdout != "" {
		fmt.Print(res.Stdout)
	}
	if res.Stderr != "" {
		fmt.Print(res.Stderr)
	}
	if err != nil {
		return &CmdOutputError{
			Cmd:    "systemctl start " + m.ServiceName,
			Stdout: res.Stdout,
			Stderr: res.Stderr,
			Err:    fmt.Errorf("systemctl start %s failed: %w", m.ServiceName, err),
		}
	}
	return nil
}

func (m *Manager) systemdIsActive() (bool, error) {
	res, err := util.Run(10*time.Second, "systemctl", "is-active", m.ServiceName)
	out := strings.ToLower(strings.TrimSpace(res.Stdout + "\n" + res.Stderr))
	if err == nil {
		return strings.Contains(out, "active"), nil
	}
	if strings.Contains(out, "inactive") || strings.Contains(out, "failed") || strings.Contains(out, "unknown") {
		return false, nil
	}
	return false, &CmdOutputError{
		Cmd:    "systemctl is-active " + m.ServiceName,
		Stdout: res.Stdout,
		Stderr: res.Stderr,
		Err:    err,
	}
}

func isReloadRecoverable(msg string) bool {
	s := strings.ToLower(msg)
	return strings.Contains(s, "invalid pid number") ||
		(strings.Contains(s, "open()") && strings.Contains(s, "pid")) ||
		(strings.Contains(s, "pid") && strings.Contains(s, "no such file or directory"))
}

func (m *Manager) preflightUnmanagedNginxListeners() error {
	listeners, err := m.listCriticalPortListeners()
	if err != nil {
		return err
	}
	for _, l := range listeners {
		if strings.EqualFold(strings.TrimSpace(l.Process), "nginx") && l.PID > 0 {
			cmd := strings.TrimSpace(l.Command)
			if cmd == "" || cmd == l.Process {
				if procCmd := readProcessCommand(l.PID); procCmd != "" {
					cmd = procCmd
				}
			}
			if cmd == "" {
				cmd = l.Process
			}
			return fmt.Errorf(
				"detected unmanaged nginx listener on :%d (pid=%d, command=%q). Stop unmanaged nginx instance and start via systemd only.",
				l.Port, l.PID, cmd,
			)
		}
	}
	return nil
}

func (m *Manager) listCriticalPortListeners() ([]PortListener, error) {
	if out, err := util.Run(10*time.Second, "ss", "-ltnp"); err == nil {
		return parseSSListeners(out.Stdout), nil
	}

	out, err := util.Run(10*time.Second, "lsof", "-nP", "-iTCP:80", "-iTCP:443", "-sTCP:LISTEN")
	if err != nil {
		return nil, fmt.Errorf("preflight listener check failed: ss and lsof unavailable")
	}
	return parseLSOFListeners(out.Stdout), nil
}

func parseSSListeners(stdout string) []PortListener {
	var out []PortListener
	seen := map[string]bool{}
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		ln := strings.TrimSpace(line)
		if ln == "" || strings.HasPrefix(ln, "State") {
			continue
		}
		if !strings.HasPrefix(ln, "LISTEN") {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) < 5 {
			continue
		}
		port := parsePort(fields[3])
		if port != 80 && port != 443 {
			continue
		}

		rest := strings.Join(fields[4:], " ")
		proc, pid, cmd := parseUsersProcess(rest)
		if proc == "" {
			continue
		}
		key := fmt.Sprintf("%d:%d:%s", port, pid, proc)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, PortListener{Port: port, Process: proc, PID: pid, Command: cmd})
	}
	return out
}

func parseLSOFListeners(stdout string) []PortListener {
	var out []PortListener
	seen := map[string]bool{}
	lines := strings.Split(stdout, "\n")
	for _, line := range lines {
		ln := strings.TrimSpace(line)
		if ln == "" || strings.HasPrefix(ln, "COMMAND") {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) < 9 {
			continue
		}
		proc := fields[0]
		pid, _ := strconv.Atoi(fields[1])
		port := parsePort(fields[8])
		if port != 80 && port != 443 {
			continue
		}
		key := fmt.Sprintf("%d:%d:%s", port, pid, proc)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, PortListener{Port: port, Process: proc, PID: pid, Command: proc})
	}
	return out
}

func parsePort(addr string) int {
	addr = strings.TrimSpace(addr)
	i := strings.LastIndex(addr, ":")
	if i == -1 || i+1 >= len(addr) {
		return 0
	}
	p, _ := strconv.Atoi(addr[i+1:])
	return p
}

func parseUsersProcess(raw string) (string, int, string) {
	start := strings.Index(raw, "users:((")
	if start == -1 {
		return "", 0, ""
	}
	segment := raw[start+len("users:(("):]
	end := strings.Index(segment, "))")
	if end == -1 {
		return "", 0, ""
	}
	segment = segment[:end]
	parts := strings.Split(segment, "\",")
	if len(parts) == 0 {
		return "", 0, ""
	}
	proc := strings.Trim(parts[0], "\" ")
	pid := 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "pid=") {
			pid, _ = strconv.Atoi(strings.TrimPrefix(p, "pid="))
		}
	}
	return proc, pid, proc
}

func readProcessCommand(pid int) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return ""
	}
	cmd := strings.ReplaceAll(string(b), "\x00", " ")
	return strings.TrimSpace(cmd)
}
