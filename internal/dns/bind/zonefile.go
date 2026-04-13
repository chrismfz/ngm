package bind

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/miekg/dns"
	"mynginx/internal/util"
)

type zoneModel struct {
	Origin string
	TTL    uint32
	RRs    []dns.RR
}

func (p *Provider) zonePath(zone string) string {
	return filepath.Join(p.cfg.Bind.ZonesDir, zone+p.cfg.Bind.ZoneFileSuffix)
}

func (p *Provider) loadZone(zone string) (zoneModel, error) {
	origin := dns.Fqdn(strings.ToLower(strings.TrimSpace(zone)))
	path := p.zonePath(zone)
	b, err := os.ReadFile(path)
	if err != nil {
		return zoneModel{}, err
	}
	z := zoneModel{Origin: origin, TTL: p.templateFor("default").TTL}
	parser := dns.NewZoneParser(strings.NewReader(string(b)), origin, path)
	for rr, ok := parser.Next(); ok; rr, ok = parser.Next() {
		if rr == nil {
			continue
		}
		if z.TTL == 0 {
			z.TTL = rr.Header().Ttl
		}
		z.RRs = append(z.RRs, rr)
	}
	if err := parser.Err(); err != nil {
		return zoneModel{}, fmt.Errorf("parse zone %s: %w", zone, err)
	}
	if z.TTL == 0 {
		z.TTL = 3600
	}
	return z, nil
}

func (p *Provider) writeZone(zone string, z zoneModel) error {
	if z.TTL == 0 {
		z.TTL = 3600
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("$ORIGIN %s\n", dns.Fqdn(zone)))
	b.WriteString(fmt.Sprintf("$TTL %d\n", z.TTL))
	for _, rr := range orderedRRs(z.RRs) {
		b.WriteString(rr.String())
		b.WriteString("\n")
	}
	return util.WriteFileAtomic(p.zonePath(zone), []byte(b.String()), 0644)
}

func orderedRRs(rrs []dns.RR) []dns.RR {
	out := append([]dns.RR(nil), rrs...)
	sort.SliceStable(out, func(i, j int) bool {
		hi, hj := out[i].Header(), out[j].Header()
		if hi.Name != hj.Name {
			return hi.Name < hj.Name
		}
		if hi.Rrtype != hj.Rrtype {
			return hi.Rrtype < hj.Rrtype
		}
		return out[i].String() < out[j].String()
	})
	return out
}

func ensureRR(rrs []dns.RR, wanted dns.RR) []dns.RR {
	ws := wanted.String()
	for _, rr := range rrs {
		if rr.String() == ws {
			return rrs
		}
	}
	return append(rrs, wanted)
}

func removeRROnNameAndTypeAndData(rrs []dns.RR, name string, rrType uint16, dataMatch func(dns.RR) bool) []dns.RR {
	out := rrs[:0]
	for _, rr := range rrs {
		h := rr.Header()
		if h.Name == name && h.Rrtype == rrType && dataMatch(rr) {
			continue
		}
		out = append(out, rr)
	}
	return out
}
