package nginx

import (
	"regexp"
	"strings"
)

type CacheCfg struct {
	Enabled bool
	Zone    string
	TTL200  string
}

type FastCGICfg struct {
	Pass  string
	Cache CacheCfg

	// Nginx <-> php-fpm timeouts
	TimeRead string
	TimeSend string
}

type UpstreamTarget struct {
	Addr   string // "10.0.0.10:8080" or "unix:/run/app.sock"
	Weight int
	Backup  bool
	Enabled bool
}

type ProxyCfg struct {
	LB         string
	Targets    []UpstreamTarget
	Websockets bool
	PassHost   bool

	TimeConnect string
	TimeRead    string
	TimeSend    string

	Microcache CacheCfg
        StaticCache CacheCfg
}

type SiteTemplateData struct {
	Domain         string
	Mode           string // "php" | "proxy" | "static"
	Webroot        string
	ACMEWebroot    string
	EnableHTTP3    bool
	TLSCert        string
	TLSKey         string
	FrontController bool

	// Nginx request body limit (eg uploads)
	ClientMaxBodySize string

	// Per-site logs (recommended)
	AccessLog string
	ErrorLog  string

	PHP   FastCGICfg
	Proxy ProxyCfg

	UpstreamKey string
}

var nonIdent = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func MakeUpstreamKey(domain string) string {
	s := strings.ToLower(domain)
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "-", "_")
	s = nonIdent.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "site"
	}
	return s
}
