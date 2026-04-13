package bind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	dnslib "github.com/miekg/dns"
	"mynginx/internal/config"
	appdns "mynginx/internal/dns"
	"mynginx/internal/util"
)

type Provider struct {
	cfg config.DNSConfig
}

func New(cfg config.DNSConfig) (*Provider, error) {
	p := &Provider{cfg: cfg}
	if p.cfg.Bind.ZoneFileSuffix == "" {
		p.cfg.Bind.ZoneFileSuffix = ".zone"
	}
	if err := os.MkdirAll(filepath.Dir(p.cfg.Bind.NamedConfInclude), 0755); err != nil {
		return nil, fmt.Errorf("create include parent dir: %w", err)
	}
	if err := os.MkdirAll(p.cfg.Bind.ZonesDir, 0755); err != nil {
		return nil, fmt.Errorf("create zones dir: %w", err)
	}
	return p, nil
}

func (p *Provider) EnsureRootSite(ctx context.Context, in appdns.SiteDNSInput) error {
	_ = ctx
	if err := in.Validate(); err != nil {
		return err
	}
	zone := normalizeDomain(in.FQDN)
	tpl := p.templateFor(in.Template)
	zm, err := p.loadZone(zone, tpl.TTL)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		zm = p.newZoneFromTemplate(zone, tpl)
	}
	p.ensureRootRecords(&zm, zone, tpl, in)
	if err := p.writeZone(zone, zm); err != nil {
		return err
	}
	if err := p.validateZone(zone); err != nil {
		return err
	}
	if err := p.writeIncludeFile(); err != nil {
		return err
	}
	if err := p.validateConf(); err != nil {
		return err
	}
	if err := p.rndcReconfig(); err != nil {
		return err
	}
	return p.rndcReload(zone)
}

func (p *Provider) EnsureSubdomainSite(ctx context.Context, in appdns.SiteDNSInput) error {
	_ = ctx
	if err := in.Validate(); err != nil {
		return err
	}
	zone := normalizeDomain(in.ParentDomain)
	fqdn := dnslib.Fqdn(normalizeDomain(in.FQDN))
	zm, err := p.loadZone(zone, p.defaultTTL())
	if err != nil {
		return fmt.Errorf("load parent zone %s: %w", zone, err)
	}
	if in.DefaultIPv4 != "" {
		rr, _ := dnslib.NewRR(fmt.Sprintf("%s %d IN A %s", fqdn, zm.TTL, in.DefaultIPv4))
		zm.RRs = ensureRR(zm.RRs, rr)
	}
	if in.DefaultIPv6 != "" {
		rr, _ := dnslib.NewRR(fmt.Sprintf("%s %d IN AAAA %s", fqdn, zm.TTL, in.DefaultIPv6))
		zm.RRs = ensureRR(zm.RRs, rr)
	}
	if err := p.writeZone(zone, zm); err != nil {
		return err
	}
	if err := p.validateZone(zone); err != nil {
		return err
	}
	return p.rndcReload(zone)
}

func (p *Provider) DeleteSubdomainSite(ctx context.Context, in appdns.SiteDNSInput) error {
	_ = ctx
	zone := normalizeDomain(in.ParentDomain)
	fqdn := dnslib.Fqdn(normalizeDomain(in.FQDN))
	zm, err := p.loadZone(zone, p.defaultTTL())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	zm.RRs = removeRROnNameAndType(zm.RRs, fqdn, dnslib.TypeA)
	zm.RRs = removeRROnNameAndType(zm.RRs, fqdn, dnslib.TypeAAAA)
	if err := p.writeZone(zone, zm); err != nil {
		return err
	}
	if err := p.validateZone(zone); err != nil {
		return err
	}
	return p.rndcReload(zone)
}

func (p *Provider) DeleteRootSite(ctx context.Context, in appdns.SiteDNSInput) error {
	_ = ctx
	zone := normalizeDomain(in.FQDN)
	if err := os.Remove(p.zonePath(zone)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := p.writeIncludeFile(); err != nil {
		return err
	}
	if err := p.validateConf(); err != nil {
		return err
	}
	return p.rndcReconfig()
}

func (p *Provider) templateFor(name string) config.DNSTemplateConfig {
	if name == "" {
		name = p.cfg.DefaultTemplate
	}
	if tpl, ok := p.cfg.Templates[name]; ok {
		return tpl
	}
	return p.cfg.Templates[p.cfg.DefaultTemplate]
}

func (p *Provider) newZoneFromTemplate(zone string, tpl config.DNSTemplateConfig) zoneModel {
	z := zoneModel{Origin: dnslib.Fqdn(zone), TTL: tpl.TTL}
	if z.TTL == 0 {
		z.TTL = 3600
	}
	return z
}

func (p *Provider) ensureRootRecords(z *zoneModel, zone string, tpl config.DNSTemplateConfig, in appdns.SiteDNSInput) {
	origin := dnslib.Fqdn(zone)
	serial := uint32(time.Now().Unix())
	z.RRs = removeRROnNameAndTypeAndData(z.RRs, origin, dnslib.TypeSOA, func(rr dnslib.RR) bool { return true })
	soaRR := &dnslib.SOA{Hdr: dnslib.RR_Header{Name: origin, Rrtype: dnslib.TypeSOA, Class: dnslib.ClassINET, Ttl: z.TTL},
		Ns: tpl.SOA.MName, Mbox: tpl.SOA.RName, Serial: serial, Refresh: tpl.SOA.Refresh, Retry: tpl.SOA.Retry, Expire: tpl.SOA.Expire, Minttl: tpl.SOA.Minimum}
	z.RRs = ensureRR(z.RRs, soaRR)
	for _, ns := range tpl.Nameservers {
		nsRR := &dnslib.NS{Hdr: dnslib.RR_Header{Name: origin, Rrtype: dnslib.TypeNS, Class: dnslib.ClassINET, Ttl: z.TTL}, Ns: ns}
		z.RRs = ensureRR(z.RRs, nsRR)
	}
	if in.DefaultIPv4 != "" {
		rr, _ := dnslib.NewRR(fmt.Sprintf("%s %d IN A %s", origin, z.TTL, in.DefaultIPv4))
		z.RRs = ensureRR(z.RRs, rr)
	}
	if in.DefaultIPv6 != "" {
		rr, _ := dnslib.NewRR(fmt.Sprintf("%s %d IN AAAA %s", origin, z.TTL, in.DefaultIPv6))
		z.RRs = ensureRR(z.RRs, rr)
	}
	for _, rec := range tpl.Records {
		name := rec.Name
		if name == "@" || name == "" {
			name = origin
		} else {
			name = dnslib.Fqdn(name + "." + zone)
		}
		ttl := rec.TTL
		if ttl == 0 {
			ttl = z.TTL
		}
		rr, err := dnslib.NewRR(fmt.Sprintf("%s %d IN %s %s", name, ttl, strings.ToUpper(rec.Type), rec.Value))
		if err == nil {
			z.RRs = ensureRR(z.RRs, rr)
		}
	}
}

func (p *Provider) validateZone(zone string) error {
	if p.cfg.Bind.CheckZoneBin == "" {
		return nil
	}
	res, err := util.Run(20*time.Second, p.cfg.Bind.CheckZoneBin, zone, p.zonePath(zone))
	if err != nil {
		return fmt.Errorf("named-checkzone failed: %w stderr=%s", err, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (p *Provider) validateConf() error {
	if p.cfg.Bind.CheckConfBin == "" || p.cfg.Bind.NamedConfPath == "" {
		return nil
	}
	res, err := util.Run(20*time.Second, p.cfg.Bind.CheckConfBin, p.cfg.Bind.NamedConfPath)
	if err != nil {
		return fmt.Errorf("named-checkconf failed: %w stderr=%s", err, strings.TrimSpace(res.Stderr))
	}
	return nil
}

func (p *Provider) rndcReload(zone string) error {
	if p.cfg.Bind.RNDCBin == "" {
		return nil
	}
	_, err := util.Run(20*time.Second, p.cfg.Bind.RNDCBin, "reload", zone)
	return err
}

func (p *Provider) rndcReconfig() error {
	if p.cfg.Bind.RNDCBin == "" {
		return nil
	}
	_, err := util.Run(20*time.Second, p.cfg.Bind.RNDCBin, "reconfig")
	return err
}

func normalizeDomain(v string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), ".")
}

func (p *Provider) defaultTTL() uint32 {
	tpl := p.templateFor(p.cfg.DefaultTemplate)
	if tpl.TTL == 0 {
		return 3600
	}
	return tpl.TTL
}

func (p *Provider) GetSiteEntry(ctx context.Context, in appdns.SiteDNSInput) (appdns.DNSEntry, error) {
	_ = ctx
	if err := in.Validate(); err != nil {
		return appdns.DNSEntry{}, err
	}
	zone := normalizeDomain(in.FQDN)
	if in.SiteKind == appdns.SiteKindSubdomain {
		zone = normalizeDomain(in.ParentDomain)
	}
	entry := appdns.DNSEntry{
		FQDN:     dnslib.Fqdn(normalizeDomain(in.FQDN)),
		Zone:     zone,
		ZoneFile: p.zonePath(zone),
	}
	if in.SiteKind == appdns.SiteKindRoot {
		entry.Kind = "root-zone"
		if _, err := os.Stat(entry.ZoneFile); err != nil {
			if os.IsNotExist(err) {
				entry.Status = "missing"
				return entry, nil
			}
			entry.Status = "error"
			return entry, err
		}
		zm, err := p.loadZone(zone, p.defaultTTL())
		if err != nil {
			entry.Status = "error"
			return entry, err
		}
		var soaCount, nsCount int
		for _, rr := range zm.RRs {
			switch rr.Header().Rrtype {
			case dnslib.TypeSOA:
				soaCount++
			case dnslib.TypeNS:
				nsCount++
			}
		}
		entry.Status = "zone"
		entry.RecordText = []string{fmt.Sprintf("SOA:%d NS:%d", soaCount, nsCount)}
		return entry, nil
	}
	entry.Kind = "subdomain-record"
	zm, err := p.loadZone(zone, p.defaultTTL())
	if err != nil {
		if os.IsNotExist(err) {
			entry.Status = "missing"
			return entry, nil
		}
		entry.Status = "error"
		return entry, err
	}
	fqdn := dnslib.Fqdn(normalizeDomain(in.FQDN))
	var aCount, aaaaCount int
	for _, rr := range zm.RRs {
		h := rr.Header()
		if h.Name != fqdn {
			continue
		}
		switch h.Rrtype {
		case dnslib.TypeA:
			aCount++
		case dnslib.TypeAAAA:
			aaaaCount++
		}
	}
	if aCount == 0 && aaaaCount == 0 {
		entry.Status = "missing"
		entry.RecordText = []string{"A:0 AAAA:0"}
		return entry, nil
	}
	entry.Status = "record"
	entry.RecordText = []string{fmt.Sprintf("A:%d AAAA:%d", aCount, aaaaCount)}
	return entry, nil
}
