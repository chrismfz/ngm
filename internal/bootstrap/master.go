package bootstrap

import (
	"bytes"
	"embed"
	"fmt"
	"path/filepath"
	"text/template"

	"mynginx/internal/config"
	"mynginx/internal/util"
)

//go:embed templates/nginx.conf.tmpl
var templateFS embed.FS

type masterTemplateData struct {
	NginxUser        string
	TLSCert          string
	TLSKey           string
	SitesIncludeGlob string
}

func RenderMasterConfig(cfg *config.Config, paths config.Paths, certPath, keyPath string) ([]byte, error) {
	tpl, err := template.ParseFS(templateFS, "templates/nginx.conf.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse master template: %w", err)
	}

	data := masterTemplateData{
		NginxUser:        cfg.Nginx.User,
		TLSCert:          certPath,
		TLSKey:           keyPath,
		SitesIncludeGlob: filepath.Join(paths.NginxSitesDir, "*.conf"),
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render master template: %w", err)
	}
	return buf.Bytes(), nil
}

func InstallMasterConfig(cfg *config.Config, paths config.Paths, certPath, keyPath string) error {
	b, err := RenderMasterConfig(cfg, paths, certPath, keyPath)
	if err != nil {
		return err
	}
	if err := util.WriteFileAtomic(paths.NginxMainConf, b, 0644); err != nil {
		return fmt.Errorf("write main nginx config %s: %w", paths.NginxMainConf, err)
	}
	return nil
}
