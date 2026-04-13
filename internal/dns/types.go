package dns

import "fmt"

type SiteKind string

const (
	SiteKindRoot      SiteKind = "root"
	SiteKindSubdomain SiteKind = "subdomain"
)

type SiteDNSInput struct {
	FQDN         string
	ParentDomain string
	Template     string
	DefaultIPv4  string
	DefaultIPv6  string
	SiteKind     SiteKind
}

type DNSEntry struct {
	FQDN       string
	Zone       string
	Kind       string
	Status     string
	RecordText []string
	ZoneFile   string
}

func (in SiteDNSInput) Validate() error {
	if in.FQDN == "" {
		return fmt.Errorf("fqdn is required")
	}
	if in.SiteKind != SiteKindRoot && in.SiteKind != SiteKindSubdomain {
		return fmt.Errorf("invalid site kind %q", in.SiteKind)
	}
	if in.SiteKind == SiteKindSubdomain && in.ParentDomain == "" {
		return fmt.Errorf("parent_domain is required for subdomain")
	}
	return nil
}
