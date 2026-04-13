package app

import (
	"context"

	"mynginx/internal/bootstrap"
	"mynginx/internal/certs"
)

func (a *App) certMgr() *certs.CertbotManager {
	return certs.NewCertbotManager(
		a.paths.CertbotBin,
		a.paths.ACMEWebroot,
		a.paths.LetsEncryptLive,
		a.cfg.Certs.Email,
	)
}

func (a *App) CertList() ([]*certs.CertInfo, error) {
	return a.certMgr().ListCerts()
}

func (a *App) CertInfo(domain string) (*certs.CertInfo, error) {
	return a.certMgr().GetCertInfo(domain)
}

func (a *App) CertIssue(ctx context.Context, domain string, applyAfter bool) error {
	if err := bootstrap.EnsureProvisionReady(a.paths); err != nil {
		return err
	}
	m := a.certMgr()
	if err := m.IssueCert(ctx, domain); err != nil {
		return err
	}
	if applyAfter {
		_, err := a.Apply(context.Background(), ApplyRequest{Domain: domain})
		return err
	}
	return nil
}

func (a *App) CertRenew(ctx context.Context, domain string, all bool, applyAfter bool) error {
	m := a.certMgr()
	if all || domain == "" {
		if err := m.RenewAll(ctx); err != nil {
			return err
		}
	} else {
		if err := m.RenewCert(ctx, domain); err != nil {
			return err
		}
	}
	if applyAfter {
		_, err := a.Apply(context.Background(), ApplyRequest{All: true})
		return err
	}
	return nil
}

func (a *App) CertCheck(days int) ([]*certs.CertInfo, error) {
	return a.certMgr().CheckExpiringSoon(days)
}
