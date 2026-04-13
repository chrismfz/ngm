package dns

import "context"

type Provider interface {
	EnsureRootSite(ctx context.Context, in SiteDNSInput) error
	EnsureSubdomainSite(ctx context.Context, in SiteDNSInput) error
	DeleteRootSite(ctx context.Context, in SiteDNSInput) error
	DeleteSubdomainSite(ctx context.Context, in SiteDNSInput) error
	GetSiteEntry(ctx context.Context, in SiteDNSInput) (DNSEntry, error)
}
