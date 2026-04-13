package bootstrap

import (
	"fmt"

	"mynginx/internal/config"
	"mynginx/internal/nginx"
)

func Init(cfg *config.Config, paths config.Paths) error {
	mgr := nginx.NewManager(paths.NginxRoot, paths.NginxBin, paths.NginxMainConf, paths.NginxSitesDir, paths.NginxStageDir, paths.NginxBackupDir)
	mgr.SetControlMode(cfg.Nginx.Apply.ReloadMode, cfg.Nginx.ServiceName)
	if err := mgr.EnsureLayout(); err != nil {
		return fmt.Errorf("ensure nginx layout: %w", err)
	}
	if err := EnsureNginxCacheDirs(cfg, paths); err != nil {
		return fmt.Errorf("ensure nginx cache dirs: %w", err)
	}

	certPath, keyPath, err := EnsureGlobalSelfSigned(cfg, paths)
	if err != nil {
		return err
	}
	if err := InstallMasterConfig(cfg, paths, certPath, keyPath); err != nil {
		return err
	}
	if err := mgr.TestConfig(); err != nil {
		return err
	}

	fmt.Println("Provision init complete: active global listener cert = self-signed fallback")
	if err := WriteProvisionReadyMarker(paths); err != nil {
		return err
	}
	fmt.Printf("Provision marker: %s\n", provisionReadyMarkerPath)
	fmt.Printf("Master config: %s\n", paths.NginxMainConf)
	return nil
}

func Test(cfg *config.Config, paths config.Paths) error {
	mgr := nginx.NewManager(paths.NginxRoot, paths.NginxBin, paths.NginxMainConf, paths.NginxSitesDir, paths.NginxStageDir, paths.NginxBackupDir)
	mgr.SetControlMode(cfg.Nginx.Apply.ReloadMode, cfg.Nginx.ServiceName)
	if err := mgr.EnsureLayout(); err != nil {
		return fmt.Errorf("ensure nginx layout: %w", err)
	}
	if err := mgr.TestConfig(); err != nil {
		return err
	}
	fmt.Printf("OK: nginx config test passed (%s -t -c %s)\n", paths.NginxBin, paths.NginxMainConf)
	return nil
}
