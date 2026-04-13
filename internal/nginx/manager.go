package nginx

import (
	"bytes"
	_ "embed"
	"fmt"
	"mynginx/internal/util"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

type Manager struct {
	Root      string
	Bin       string
	MainConf  string
	SitesDir  string
	StageDir  string
	BackupDir string
}

func NewManager(root, bin, mainConf, sitesDir, stageDir, backupDir string) *Manager {
	return &Manager{
		Root:      root,
		Bin:       bin,
		MainConf:  mainConf,
		SitesDir:  sitesDir,
		StageDir:  stageDir,
		BackupDir: backupDir,
	}
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

	// Reload can fail when nginx is not running or PID file is stale/invalid.
	// In that case, start nginx with the configured main config and only fail
	// if startup also fails.
	if !isReloadRecoverable(res.Stderr) {
		return err
	}

	startRes, startErr := util.Run(10*time.Second, m.Bin, "-c", m.MainConf)
	if startRes.Stdout != "" {
		fmt.Print(startRes.Stdout)
	}
	if startRes.Stderr != "" {
		fmt.Print(startRes.Stderr)
	}
	if startErr != nil {
		return &CmdOutputError{
			Cmd:    m.Bin + " -c " + m.MainConf,
			Stdout: startRes.Stdout,
			Stderr: startRes.Stderr,
			Err:    startErr,
		}
	}
	return nil
}

func isReloadRecoverable(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "invalid pid number") ||
		(strings.Contains(s, "open()") && strings.Contains(s, "pid")) ||
		(strings.Contains(s, "pid") && strings.Contains(s, "no such file or directory"))
}
