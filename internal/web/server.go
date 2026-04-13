package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"mynginx/internal/app"
	"mynginx/internal/auth"
	"mynginx/internal/backup"
	"mynginx/internal/config"
	"mynginx/internal/store"
	"mynginx/internal/users"
)

const cookieName = "ngm_session"

type ctxKey int

const ctxSession ctxKey = 1

type Server struct {
	cfg   *config.Config
	paths config.Paths
	st    store.SiteStore
	core  *app.App

	sessions *SessionStore
	tpl      *template.Template
}

func New(cfg *config.Config, paths config.Paths, st store.SiteStore) (*Server, error) {
	core, err := app.New(cfg, paths, st)
	if err != nil {
		return nil, err
	}

	tpl := template.New("root")
	template.Must(tpl.New("layout").Parse(layoutHTML))
	template.Must(tpl.New("menu").Parse(menuHTML))
	template.Must(tpl.New("content").Parse(contentHTML))
	template.Must(tpl.New("login").Parse(loginHTML))
	template.Must(tpl.New("sites").Parse(sitesHTML))
	template.Must(tpl.New("site_form").Parse(siteFormHTML))
	template.Must(tpl.New("proxy_targets").Parse(proxyTargetsHTML))
	template.Must(tpl.New("apply_form").Parse(applyFormHTML))
	template.Must(tpl.New("apply_result").Parse(applyResultHTML))
	template.Must(tpl.New("certs").Parse(certsHTML))
	template.Must(tpl.New("cert_info").Parse(certInfoHTML))
	template.Must(tpl.New("cert_check").Parse(certCheckHTML))
	template.Must(tpl.New("dashboard").Parse(dashboardHTML))
	template.Must(tpl.New("packages").Parse(packagesHTML))
	template.Must(tpl.New("package_form").Parse(packageFormHTML))
	template.Must(tpl.New("users").Parse(usersHTML))
	template.Must(tpl.New("user_form").Parse(userFormHTML))
	template.Must(tpl.New("resellers").Parse(resellersHTML))
	template.Must(tpl.New("reseller_form").Parse(resellerFormHTML))

	return &Server{
		cfg:      cfg,
		paths:    paths,
		st:       st,
		core:     core,
		sessions: NewSessionStore(12 * time.Hour),
		tpl:      tpl,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/dashboard", http.StatusFound)
	})

	// auth
	mux.HandleFunc("/ui/login", s.handleLogin)
	mux.HandleFunc("/ui/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("/ui/dashboard", s.requireAuth(s.handleDashboard))

	// sites
	mux.HandleFunc("/ui/sites", s.requireAuth(s.handleSites))
	mux.HandleFunc("/ui/sites/new", s.requireAuth(s.handleSiteNew))
	mux.HandleFunc("/ui/sites/edit", s.requireAuth(s.handleSiteEdit))
	mux.HandleFunc("/ui/sites/disable", s.requireAuth(s.handleSiteDisable))
	mux.HandleFunc("/ui/sites/enable", s.requireAuth(s.handleSiteEnable))
	mux.HandleFunc("/ui/sites/delete", s.requireAuth(s.handleSiteDelete))

	// proxy targets
	mux.HandleFunc("/ui/sites/targets", s.requireAuth(s.handleProxyTargets))
	mux.HandleFunc("/ui/sites/targets/add", s.requireAuth(s.handleProxyTargetAdd))
	mux.HandleFunc("/ui/sites/targets/del", s.requireAuth(s.handleProxyTargetDel))

	// apply
	mux.HandleFunc("/ui/apply", s.requireAuth(s.handleApply))

	// certs
	mux.HandleFunc("/ui/certs", s.requireAuth(s.handleCerts))
	mux.HandleFunc("/ui/cert/info", s.requireAuth(s.handleCertInfo))
	mux.HandleFunc("/ui/cert/issue", s.requireAuth(s.handleCertIssue))
	mux.HandleFunc("/ui/cert/renew", s.requireAuth(s.handleCertRenew))
	mux.HandleFunc("/ui/cert/check", s.requireAuth(s.handleCertCheck))
	mux.HandleFunc("/ui/packages", s.requireAuth(s.requireRole("admin", "reseller")(s.handlePackages)))
	mux.HandleFunc("/ui/packages/new", s.requireAuth(s.requireRole("admin", "reseller")(s.handlePackageNew)))
	mux.HandleFunc("/ui/packages/edit", s.requireAuth(s.requireRole("admin", "reseller")(s.handlePackageEdit)))
	mux.HandleFunc("/ui/packages/delete", s.requireAuth(s.requireRole("admin", "reseller")(s.handlePackageDelete)))
	mux.HandleFunc("/ui/users", s.requireAuth(s.requireRole("admin", "reseller")(s.handleUsers)))
	mux.HandleFunc("/ui/users/new", s.requireAuth(s.requireRole("admin", "reseller")(s.handleUserNew)))
	mux.HandleFunc("/ui/users/edit", s.requireAuth(s.requireRole("admin", "reseller")(s.handleUserEdit)))
	mux.HandleFunc("/ui/users/suspend", s.requireAuth(s.requireRole("admin", "reseller")(s.handleUserSuspend)))
	mux.HandleFunc("/ui/users/enable", s.requireAuth(s.requireRole("admin", "reseller")(s.handleUserEnable)))
	mux.HandleFunc("/ui/users/delete", s.requireAuth(s.requireRole("admin", "reseller")(s.handleUserDelete)))
	mux.HandleFunc("/ui/resellers", s.requireAuth(s.requireRole("admin")(s.handleResellers)))
	mux.HandleFunc("/ui/resellers/new", s.requireAuth(s.requireRole("admin")(s.handleResellerNew)))
	mux.HandleFunc("/ui/resellers/edit", s.requireAuth(s.requireRole("admin")(s.handleResellerEdit)))
	mux.HandleFunc("/ui/resellers/disable", s.requireAuth(s.requireRole("admin")(s.handleResellerDisable)))
	mux.HandleFunc("/ui/resellers/enable", s.requireAuth(s.requireRole("admin")(s.handleResellerEnable)))
	mux.HandleFunc("/ui/resellers/delete", s.requireAuth(s.requireRole("admin")(s.handleResellerDelete)))
	mux.HandleFunc("/ui/backup", s.requireAuth(s.handleBackup))

	return mux
}

func (s *Server) Serve(ctx context.Context, listen string) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	return srv.ListenAndServe()
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.currentSession(r)
		if !ok {
			http.Redirect(w, r, "/ui/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), ctxSession, sess)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) requireRole(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			sess, ok := s.sessionFromCtx(r)
			if !ok {
				http.Redirect(w, r, "/ui/login", http.StatusFound)
				return
			}
			for _, role := range roles {
				if sess.Role == role {
					next(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}
}

func (s *Server) sessionFromCtx(r *http.Request) (Session, bool) {
	v := r.Context().Value(ctxSession)
	if v == nil {
		return Session{}, false
	}
	sess, ok := v.(Session)
	return sess, ok
}

func (s *Server) currentSession(r *http.Request) (Session, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return Session{}, false
	}
	return s.sessions.Get(strings.TrimSpace(c.Value))
}

func (s *Server) siteVisibleToSession(ctx context.Context, sess Session, domain string) bool {
	if sess.Role == "admin" {
		return true
	}
	site, err := s.core.SiteGet(ctx, domain)
	if err != nil {
		return false
	}
	u, err := s.st.GetUserByID(site.UserID)
	if err != nil {
		return false
	}
	pu, err := s.st.GetPanelUserByUsername(u.Username)
	if err != nil {
		return false
	}
	if sess.Role == "user" {
		return pu.ID == sess.UserID
	}
	return pu.ResellerID != nil && *pu.ResellerID == sess.UserID
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, title, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Title"] = title
	data["Page"] = page
	if sess, ok := s.sessionFromCtx(r); ok {
		data["Authed"] = true
		data["Session"] = sess
	} else {
		data["Authed"] = false
	}
	_ = s.tpl.ExecuteTemplate(w, "layout", data)
}

// ---------------- auth ----------------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": ""})
		return

	case http.MethodPost:
		_ = r.ParseForm()
		username := strings.TrimSpace(r.FormValue("username"))
		pass := r.FormValue("password")
		msgInvalid := "Invalid credentials"
		msgLocked := "Account temporarily locked, try again later"

		u, err := s.st.GetPanelUserByUsername(username)
		if err != nil || !u.Enabled {
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": msgInvalid})
			return
		}
		if u.LockedUntil != nil && u.LockedUntil.After(time.Now().UTC()) {
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": msgLocked})
			return
		}
		var authErr error
		if u.Role == "user" {
			systemUser := strings.TrimSpace(u.SystemUser)
			if systemUser == "" {
				systemUser = u.Username
			}
			authErr = auth.VerifyShadowPassword(systemUser, pass)
		} else {
			authErr = bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass))
		}
		if authErr != nil {
			_ = s.st.IncrementFailedAttempts(u.ID)
			failed := u.FailedAttempts + 1
			if failed >= 5 {
				_ = s.st.LockPanelUser(u.ID, time.Now().UTC().Add(15*time.Minute))
				_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": msgLocked})
				return
			}
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": msgInvalid})
			return
		}

		sess, err := s.sessions.New(u.ID, u.Username, u.Role)
		if err != nil {
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": "Login failed"})
			return
		}

		_ = s.st.ResetFailedAttempts(u.ID)
		_ = s.st.UpdatePanelUserLastLogin(u.ID)
		s.setSessionCookie(w, r, sess.Token)
		http.Redirect(w, r, "/ui/dashboard", http.StatusFound)
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(cookieName)
	if err == nil && c != nil {
		s.sessions.Delete(c.Value)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/ui/login", http.StatusFound)
}

// ---------------- sites ----------------

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromCtx(r)
	items, err := s.core.SiteList(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Optional enrich for UI: owner username + cert info
	owners := map[string]string{}
	certs := map[string]any{} // domain -> *certs.CertInfo (stored as interface for templates)
	filtered := make([]app.SiteListItem, 0, len(items))
	for _, it := range items {
		if it.Site.UserID != 0 {
			if u, err := s.st.GetUserByID(it.Site.UserID); err == nil {
				owners[it.Site.Domain] = u.Username
				if sess.Role == "user" && u.Username != sess.Username {
					continue
				}
				if sess.Role == "reseller" {
					pu, err := s.st.GetPanelUserByUsername(u.Username)
					if err != nil || pu.ResellerID == nil || *pu.ResellerID != sess.UserID {
						continue
					}
				}
			}
		}
		if ci, err := s.core.CertInfo(it.Site.Domain); err == nil && ci != nil && ci.Exists {
			certs[it.Site.Domain] = ci
		}
		filtered = append(filtered, it)
	}

	s.render(w, r, "Sites", "sites", map[string]any{
		"Items":  filtered,
		"Owners": owners,
		"Certs":  certs,
	})
}

func (s *Server) handleSiteNew(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.render(w, r, "Add Site", "site_form", map[string]any{
			"Mode": "new",
			"Form": map[string]any{
				"mode":      "php",
				"parent":    "",
				"http3":     "true",
				"provision": "true",
				"applynow":  "true",
				"targets":   "",
				// Leave blank = use defaults in template render.
				"clientmax": "",
				"phpread":   "",
				"phpsend":   "",
				"phpini":    "",
			},
		})
		return

	case http.MethodPost:
		_ = r.ParseForm()
		targetsRaw := r.FormValue("targets")
		targets := splitLines(targetsRaw)

		clientMax := strings.TrimSpace(r.FormValue("clientmax"))
		phpRead := strings.TrimSpace(r.FormValue("phpread"))
		phpSend := strings.TrimSpace(r.FormValue("phpsend"))
		phpIni := strings.TrimSpace(r.FormValue("phpini"))

		if errMsg := validateNginxKnobs(clientMax, phpRead, phpSend); errMsg != "" {
			s.render(w, r, "Add Site", "site_form", map[string]any{
				"Mode":  "new",
				"Error": errMsg,
				"Form": map[string]any{
					"user":      strings.TrimSpace(r.FormValue("user")),
					"domain":    strings.TrimSpace(r.FormValue("domain")),
					"mode":      strings.TrimSpace(r.FormValue("mode")),
					"parent":    strings.TrimSpace(r.FormValue("parent")),
					"php":       strings.TrimSpace(r.FormValue("php")),
					"webroot":   strings.TrimSpace(r.FormValue("webroot")),
					"http3":     boolStr(parseBool(r.FormValue("http3"), true)),
					"provision": boolStr(parseBool(r.FormValue("provision"), true)),
					"skipcert":  boolStr(parseBool(r.FormValue("skipcert"), false)),
					"applynow":  boolStr(parseBool(r.FormValue("applynow"), true)),
					"targets":   targetsRaw,
					"clientmax": clientMax,
					"phpread":   phpRead,
					"phpsend":   phpSend,
					"phpini":    phpIni,
				},
			})
			return
		}

		mode := strings.TrimSpace(r.FormValue("mode"))

		req := app.SiteAddRequest{
			User:         strings.TrimSpace(r.FormValue("user")),
			Domain:       strings.TrimSpace(r.FormValue("domain")),
			Mode:         mode,
			ParentDomain: strings.TrimSpace(r.FormValue("parent")),
			PHP:          strings.TrimSpace(r.FormValue("php")),
			Webroot:      strings.TrimSpace(r.FormValue("webroot")),
			HTTP3:        parseBool(r.FormValue("http3"), true),
			Provision:    parseBool(r.FormValue("provision"), true),
			SkipCert:     parseBool(r.FormValue("skipcert"), false),
			ApplyNow:     parseBool(r.FormValue("applynow"), true),

			ProxyTargets:      targets,
			ClientMaxBodySize: clientMax,
			PHPTimeRead:       phpRead,
			PHPTimeSend:       phpSend,
			PHPIniOverrides:   "",
		}

		// Only store php.ini overrides when php mode
		if strings.TrimSpace(mode) == "php" {
			req.PHPIniOverrides = phpIni
		}

		// Avoid "apply-now failed" warnings for proxy mode.
		if strings.TrimSpace(req.Mode) == "proxy" && req.ApplyNow && len(req.ProxyTargets) == 0 {
			s.render(w, r, "Add Site", "site_form", map[string]any{
				"Mode":  "new",
				"Error": "Proxy mode requires at least 1 proxy target when Apply Now is enabled. Add targets or disable Apply Now.",
				"Form": map[string]any{
					"user":      req.User,
					"domain":    req.Domain,
					"mode":      req.Mode,
					"parent":    req.ParentDomain,
					"php":       req.PHP,
					"webroot":   req.Webroot,
					"http3":     boolStr(req.HTTP3),
					"provision": boolStr(req.Provision),
					"skipcert":  boolStr(req.SkipCert),
					"applynow":  boolStr(req.ApplyNow),
					"targets":   targetsRaw,
					"clientmax": clientMax,
					"phpread":   phpRead,
					"phpsend":   phpSend,
					"phpini":    phpIni,
				},
			})
			return
		}

		res, err := s.core.SiteAdd(r.Context(), req)
		if err != nil {
			s.render(w, r, "Add Site", "site_form", map[string]any{
				"Mode":  "new",
				"Error": err.Error(),
				"Form": map[string]any{
					"user":      req.User,
					"domain":    req.Domain,
					"mode":      req.Mode,
					"parent":    req.ParentDomain,
					"php":       req.PHP,
					"webroot":   req.Webroot,
					"http3":     boolStr(req.HTTP3),
					"provision": boolStr(req.Provision),
					"skipcert":  boolStr(req.SkipCert),
					"applynow":  boolStr(req.ApplyNow),
					"targets":   targetsRaw,
					"clientmax": clientMax,
					"phpread":   phpRead,
					"phpsend":   phpSend,
					"phpini":    phpIni,
				},
			})
			return
		}

		s.render(w, r, "Site Saved", "site_form", map[string]any{
			"Mode":     "result",
			"Site":     res.Site,
			"Warnings": res.Warnings,
		})
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSiteEdit(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		d := strings.TrimSpace(r.URL.Query().Get("domain"))
		sess, _ := s.sessionFromCtx(r)
		if !s.siteVisibleToSession(r.Context(), sess, d) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		cur, err := s.core.SiteGet(r.Context(), d)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		owner := ""
		if cur.UserID != 0 {
			if u, err := s.st.GetUserByID(cur.UserID); err == nil {
				owner = u.Username
			}
		}

		phpini := ""
		if strings.TrimSpace(cur.Mode) == "php" {
			phpini = readPHPOverridesFile(cur.Webroot)
		}

		s.render(w, r, "Edit Site", "site_form", map[string]any{
			"Mode": "edit",
			"Form": map[string]any{
				"domain":    cur.Domain,
				"user":      owner,
				"mode":      cur.Mode,
				"parent":    valueOrBlank(cur.ParentDomain),
				"php":       cur.PHPVersion,
				"webroot":   cur.Webroot,
				"http3":     boolStr(cur.EnableHTTP3),
				"enabled":   boolStr(cur.Enabled),
				"applynow":  "false",
				"clientmax": cur.ClientMaxBodySize,
				"phpread":   cur.PHPTimeRead,
				"phpsend":   cur.PHPTimeSend,
				"phpini":    phpini,
			},
		})
		return

	case http.MethodPost:
		_ = r.ParseForm()

		domain := strings.TrimSpace(r.FormValue("domain"))
		sess, _ := s.sessionFromCtx(r)
		if !s.siteVisibleToSession(r.Context(), sess, domain) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mode := strings.TrimSpace(r.FormValue("mode"))

		http3 := parseBool(r.FormValue("http3"), true)
		enabled := parseBool(r.FormValue("enabled"), true)
		applyNow := parseBool(r.FormValue("applynow"), false)

		clientMax := strings.TrimSpace(r.FormValue("clientmax"))
		phpRead := strings.TrimSpace(r.FormValue("phpread"))
		phpSend := strings.TrimSpace(r.FormValue("phpsend"))
		phpIni := strings.TrimSpace(r.FormValue("phpini"))

		if errMsg := validateNginxKnobs(clientMax, phpRead, phpSend); errMsg != "" {
			s.render(w, r, "Edit Site", "site_form", map[string]any{
				"Mode":  "edit",
				"Error": errMsg,
				"Form": map[string]any{
					"domain":    domain,
					"user":      strings.TrimSpace(r.FormValue("user")),
					"mode":      mode,
					"parent":    strings.TrimSpace(r.FormValue("parent")),
					"php":       strings.TrimSpace(r.FormValue("php")),
					"webroot":   strings.TrimSpace(r.FormValue("webroot")),
					"http3":     boolStr(http3),
					"enabled":   boolStr(enabled),
					"applynow":  boolStr(applyNow),
					"clientmax": clientMax,
					"phpread":   phpRead,
					"phpsend":   phpSend,
					"phpini":    phpIni,
				},
			})
			return
		}

		req := app.SiteEditRequest{
			Domain:            domain,
			User:              strings.TrimSpace(r.FormValue("user")),
			ParentDomain:      strings.TrimSpace(r.FormValue("parent")),
			ParentDomainSet:   true,
			Mode:              mode,
			PHP:               strings.TrimSpace(r.FormValue("php")),
			Webroot:           strings.TrimSpace(r.FormValue("webroot")),
			HTTP3:             &http3,
			Enabled:           &enabled,
			ApplyNow:          applyNow,
			ClientMaxBodySize: clientMax,
			PHPTimeRead:       phpRead,
			PHPTimeSend:       phpSend,
		}

		// Only pass overrides pointer in php mode
		if strings.TrimSpace(mode) == "php" {
			req.PHPIniOverrides = &phpIni
		} else {
			req.PHPIniOverrides = nil
		}

		// If user asks ApplyNow in proxy mode, ensure at least 1 enabled target exists.
		if strings.TrimSpace(req.Mode) == "proxy" && req.ApplyNow {
			cur, err := s.core.SiteGet(r.Context(), req.Domain)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			tgs, err := s.st.ListProxyTargetsBySiteID(cur.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			enabledCount := 0
			for _, t := range tgs {
				if t.Enabled {
					enabledCount++
				}
			}
			if enabledCount == 0 {
				s.render(w, r, "Edit Site", "site_form", map[string]any{
					"Mode":  "edit",
					"Error": "Proxy mode requires at least 1 enabled proxy target to Apply Now. Go to Targets and add one first.",
					"Form": map[string]any{
						"domain":    req.Domain,
						"user":      req.User,
						"mode":      req.Mode,
						"parent":    req.ParentDomain,
						"php":       req.PHP,
						"webroot":   req.Webroot,
						"http3":     boolStr(http3),
						"enabled":   boolStr(enabled),
						"applynow":  boolStr(applyNow),
						"clientmax": clientMax,
						"phpread":   phpRead,
						"phpsend":   phpSend,
						"phpini":    phpIni,
					},
				})
				return
			}
		}

		updated, err := s.core.SiteEdit(r.Context(), req)
		if err != nil {
			s.render(w, r, "Edit Site", "site_form", map[string]any{
				"Mode":  "edit",
				"Error": err.Error(),
				"Form": map[string]any{
					"domain":    req.Domain,
					"user":      req.User,
					"mode":      req.Mode,
					"parent":    req.ParentDomain,
					"php":       req.PHP,
					"webroot":   req.Webroot,
					"http3":     boolStr(http3),
					"enabled":   boolStr(enabled),
					"applynow":  boolStr(applyNow),
					"clientmax": clientMax,
					"phpread":   phpRead,
					"phpsend":   phpSend,
					"phpini":    phpIni,
				},
			})
			return
		}

		s.render(w, r, "Site Updated", "site_form", map[string]any{
			"Mode": "result",
			"Site": updated,
		})
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSiteDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	domain := strings.TrimSpace(r.FormValue("domain"))
	sess, _ := s.sessionFromCtx(r)
	if !s.siteVisibleToSession(r.Context(), sess, domain) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.core.SiteDisable(r.Context(), domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/sites", http.StatusFound)
}

func (s *Server) handleSiteEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	domain := strings.TrimSpace(r.FormValue("domain"))
	sess, _ := s.sessionFromCtx(r)
	if !s.siteVisibleToSession(r.Context(), sess, domain) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.core.SiteEnable(r.Context(), domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/sites", http.StatusFound)
}

func (s *Server) handleSiteDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	domain := strings.TrimSpace(r.FormValue("domain"))
	sess, _ := s.sessionFromCtx(r)
	if !s.siteVisibleToSession(r.Context(), sess, domain) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := s.core.SiteDelete(r.Context(), domain); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/sites", http.StatusFound)
}

// ---------------- proxy targets ----------------

func (s *Server) handleProxyTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	sess, _ := s.sessionFromCtx(r)
	if !s.siteVisibleToSession(r.Context(), sess, domain) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}

	site, err := s.core.SiteGet(r.Context(), domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(site.Mode) != "proxy" {
		http.Error(w, "site is not in proxy mode", http.StatusBadRequest)
		return
	}

	targets, err := s.st.ListProxyTargetsBySiteID(site.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.render(w, r, "Proxy Targets", "proxy_targets", map[string]any{
		"Site":    site,
		"Targets": targets,
	})
}

func (s *Server) handleProxyTargetAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	domain := strings.TrimSpace(r.FormValue("domain"))
	target := strings.TrimSpace(r.FormValue("target"))
	weight, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("weight")))
	backup := parseBool(r.FormValue("backup"), false)
	enabled := parseBool(r.FormValue("enabled"), true)

	if domain == "" || target == "" {
		http.Error(w, "domain and target are required", http.StatusBadRequest)
		return
	}

	site, err := s.core.SiteGet(r.Context(), domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(site.Mode) != "proxy" {
		http.Error(w, "site is not in proxy mode", http.StatusBadRequest)
		return
	}

	if err := s.st.UpsertProxyTarget(site.ID, target, weight, backup, enabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/sites/targets?domain="+url.QueryEscape(domain), http.StatusFound)
}

func (s *Server) handleProxyTargetDel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	domain := strings.TrimSpace(r.FormValue("domain"))
	target := strings.TrimSpace(r.FormValue("target"))
	if domain == "" || target == "" {
		http.Error(w, "domain and target are required", http.StatusBadRequest)
		return
	}

	site, err := s.core.SiteGet(r.Context(), domain)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.st.DisableProxyTarget(site.ID, target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/sites/targets?domain="+url.QueryEscape(domain), http.StatusFound)
}

// ---------------- apply ----------------

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.render(w, r, "Apply", "apply_form", map[string]any{})
		return

	case http.MethodPost:
		_ = r.ParseForm()
		sess, _ := s.sessionFromCtx(r)
		domain := strings.TrimSpace(r.FormValue("domain"))
		all := parseBool(r.FormValue("all"), false)
		dry := parseBool(r.FormValue("dry"), false)
		limit, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("limit")))
		if sess.Role != "admin" {
			all = false
			if domain == "" {
				http.Error(w, "domain is required", http.StatusBadRequest)
				return
			}
			if !s.siteVisibleToSession(r.Context(), sess, domain) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}

		res, err := s.core.Apply(r.Context(), app.ApplyRequest{
			Domain: domain,
			All:    all,
			DryRun: dry,
			Limit:  limit,
		})
		if err != nil {
			s.render(w, r, "Apply Result", "apply_result", map[string]any{
				"Result": res,
				"Error":  err.Error(),
			})
			return
		}

		s.render(w, r, "Apply Result", "apply_result", map[string]any{
			"Result": res,
		})
		return

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---------------- certs ----------------

func (s *Server) handleCerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	items, err := s.core.CertList()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess, _ := s.sessionFromCtx(r)
	if sess.Role != "admin" {
		filtered := items[:0]
		for _, it := range items {
			if s.siteVisibleToSession(r.Context(), sess, it.Domain) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	s.render(w, r, "Certificates", "certs", map[string]any{"Items": items})
}

func (s *Server) handleCertInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d := strings.TrimSpace(r.URL.Query().Get("domain"))
	sess, _ := s.sessionFromCtx(r)
	if !s.siteVisibleToSession(r.Context(), sess, d) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	info, err := s.core.CertInfo(d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.render(w, r, "Certificate Info", "cert_info", map[string]any{"Info": info})
}

func (s *Server) handleCertIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	d := strings.TrimSpace(r.FormValue("domain"))
	sess, _ := s.sessionFromCtx(r)
	if !s.siteVisibleToSession(r.Context(), sess, d) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if d == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	if err := s.core.CertIssue(ctx, d, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/certs", http.StatusFound)
}

func (s *Server) handleCertRenew(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	d := strings.TrimSpace(r.FormValue("domain"))
	all := parseBool(r.FormValue("all"), false)
	sess, _ := s.sessionFromCtx(r)
	if sess.Role != "admin" {
		all = false
		if d == "" || !s.siteVisibleToSession(r.Context(), sess, d) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	if err := s.core.CertRenew(ctx, d, all, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/ui/certs", http.StatusFound)
}

func (s *Server) handleCertCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	daysStr := strings.TrimSpace(r.URL.Query().Get("days"))
	days := 30
	if daysStr != "" {
		if v, err := strconv.Atoi(daysStr); err == nil && v > 0 {
			days = v
		}
	}

	items, err := s.core.CertCheck(days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess, _ := s.sessionFromCtx(r)
	if sess.Role != "admin" {
		filtered := items[:0]
		for _, it := range items {
			if s.siteVisibleToSession(r.Context(), sess, it.Domain) {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	s.render(w, r, "Cert Check", "cert_check", map[string]any{
		"Days":  days,
		"Items": items,
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromCtx(r)
	allUsers, _ := s.st.ListPanelUsers()
	items, _ := s.core.SiteList(r.Context())
	data := map[string]any{"Role": sess.Role}
	if sess.Role == "admin" {
		var resellers, users int
		for _, u := range allUsers {
			if u.Role == "reseller" {
				resellers++
			}
			if u.Role == "user" {
				users++
			}
		}
		data["Resellers"] = resellers
		data["Users"] = users
		data["Domains"] = len(items)
	} else if sess.Role == "reseller" {
		countUsers, countDomains := 0, 0
		for _, pu := range allUsers {
			if pu.Role == "user" && pu.ResellerID != nil && *pu.ResellerID == sess.UserID {
				countUsers++
			}
		}
		for _, it := range items {
			if s.siteVisibleToSession(r.Context(), sess, it.Site.Domain) {
				countDomains++
			}
		}
		data["Users"] = countUsers
		data["Domains"] = countDomains
	} else {
		countDomains := 0
		for _, it := range items {
			if s.siteVisibleToSession(r.Context(), sess, it.Site.Domain) {
				countDomains++
			}
		}
		data["Domains"] = countDomains
	}
	s.render(w, r, "Dashboard", "dashboard", data)
}

func (s *Server) handlePackages(w http.ResponseWriter, r *http.Request) {
	pkgs, err := s.st.ListPackages()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess, _ := s.sessionFromCtx(r)
	if sess.Role == "reseller" {
		filtered := pkgs[:0]
		for _, p := range pkgs {
			if p.CreatedBy != nil && *p.CreatedBy == sess.UserID {
				filtered = append(filtered, p)
			}
		}
		pkgs = filtered
	}
	s.render(w, r, "Packages", "packages", map[string]any{"Items": pkgs})
}

func (s *Server) handlePackageNew(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, r, "New Package", "package_form", map[string]any{"Mode": "new"})
		return
	}
	_ = r.ParseForm()
	sess, _ := s.sessionFromCtx(r)
	p := store.Package{Name: strings.TrimSpace(r.FormValue("name")), CreatedBy: &sess.UserID, MaxDomains: 5, MaxSubdomains: 20, MaxDiskMB: 1024, MaxPHPWorkers: 5, MaxMySQLDBs: 1, MaxMySQLUsers: 2}
	_, err := s.st.CreatePackage(p)
	if err != nil {
		s.render(w, r, "New Package", "package_form", map[string]any{"Mode": "new", "Error": err.Error()})
		return
	}
	http.Redirect(w, r, "/ui/packages", http.StatusFound)
}

func (s *Server) handlePackageEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	p, err := s.st.GetPackageByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.Name = strings.TrimSpace(r.FormValue("name"))
	if _, err := s.st.UpdatePackage(p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/packages", http.StatusFound)
}

func (s *Server) handlePackageDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err := s.st.DeletePackage(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/packages", http.StatusFound)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	items, err := s.st.ListPanelUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sess, _ := s.sessionFromCtx(r)
	pkgByUser, _ := s.userPackageMap()
	nameByID := map[int64]string{}
	for _, u := range items {
		nameByID[u.ID] = u.Username
	}
	var filtered []map[string]any
	for _, u := range items {
		if u.Role != "user" {
			continue
		}
		if sess.Role == "reseller" && (u.ResellerID == nil || *u.ResellerID != sess.UserID) {
			continue
		}
		owner := "-"
		if u.ResellerID != nil {
			if n, ok := nameByID[*u.ResellerID]; ok && n != "" {
				owner = n
			} else {
				owner = "#" + strconv.FormatInt(*u.ResellerID, 10)
			}
		}
		filtered = append(filtered, map[string]any{
			"User":        u,
			"PackageName": pkgByUser[u.ID],
			"Owner":       owner,
		})
	}
	s.render(w, r, "Users", "users", map[string]any{
		"Items": filtered,
		"Error": strings.TrimSpace(r.URL.Query().Get("error")),
		"Info":  strings.TrimSpace(r.URL.Query().Get("info")),
	})
}

func (s *Server) handleUserNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromCtx(r)
	pkgs, _ := s.listAssignablePackages(sess)
	allUsers, _ := s.st.ListPanelUsers()
	resellers := make([]store.PanelUser, 0)
	for _, u := range allUsers {
		if u.Role == "reseller" {
			resellers = append(resellers, u)
		}
	}
	if r.Method == http.MethodGet {
		s.render(w, r, "New User", "user_form", map[string]any{
			"Mode":       "new",
			"FormAction": "/ui/users/new",
			"Packages":   pkgs,
			"Resellers":  resellers,
			"IsAdmin":    sess.Role == "admin",
			"Form": map[string]string{
				"enabled": "true",
			},
		})
		return
	}
	_ = r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	pass := r.FormValue("password")
	if username == "" || pass == "" {
		s.render(w, r, "New User", "user_form", map[string]any{
			"Mode":       "new",
			"FormAction": "/ui/users/new",
			"Error":      "username and password are required",
			"Packages":   pkgs,
			"Resellers":  resellers,
			"IsAdmin":    sess.Role == "admin",
			"Form": map[string]string{
				"username":   username,
				"enabled":    r.FormValue("enabled"),
				"package_id": r.FormValue("package_id"),
				"reseller":   r.FormValue("reseller_id"),
			},
		})
		return
	}
	enabled := parseBool(r.FormValue("enabled"), true)
	pu, err := s.st.CreatePanelUser(username, "$SHADOW$", "user", enabled)
	if err != nil {
		s.render(w, r, "New User", "user_form", map[string]any{
			"Mode":       "new",
			"FormAction": "/ui/users/new",
			"Error":      err.Error(),
			"Packages":   pkgs,
			"Resellers":  resellers,
			"IsAdmin":    sess.Role == "admin",
		})
		return
	}
	pu.SystemUser = username
	pu.Enabled = enabled
	pu.CreatedBy = &sess.UserID
	if sess.Role == "reseller" {
		pu.ResellerID = &sess.UserID
		pu.OwnerID = &sess.UserID
	} else if rid, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("reseller_id")), 10, 64); rid > 0 {
		pu.ResellerID = &rid
		pu.OwnerID = &rid
	}
	if _, err := s.st.UpdatePanelUser(pu); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	home := filepath.Join(s.cfg.Hosting.HomeRoot, username)
	_ = users.CreateSystemUser(username, home)
	_ = users.SetSystemPassword(username, pass)
	if pkgID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("package_id")), 10, 64); pkgID > 0 {
		_ = s.st.AssignPackage(pu.ID, pkgID, sess.UserID)
	}
	if !enabled {
		_ = users.SuspendSystemUser(pu.SystemUser)
	}
	http.Redirect(w, r, "/ui/users", http.StatusFound)
}

func (s *Server) handleUserEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := s.sessionFromCtx(r)
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		username = strings.TrimSpace(r.FormValue("username"))
	}
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "user" || !s.canManagePanelUser(sess, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method == http.MethodGet {
		pkgs, _ := s.listAssignablePackages(sess)
		up, err := s.st.GetUserPackage(target.ID)
		packageID := int64(0)
		if err == nil {
			packageID = up.PackageID
		}
		s.render(w, r, "Edit User", "user_form", map[string]any{
			"Mode":       "edit",
			"FormAction": "/ui/users/edit",
			"Packages":   pkgs,
			"PanelUser":  target,
			"Form": map[string]string{
				"username":   target.Username,
				"enabled":    boolStr(target.Enabled),
				"package_id": strconv.FormatInt(packageID, 10),
			},
		})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	target.Enabled = parseBool(r.FormValue("enabled"), true)
	pass := strings.TrimSpace(r.FormValue("password"))
	sysUser := strings.TrimSpace(target.SystemUser)
	if sysUser == "" {
		sysUser = target.Username
	}
	if pass != "" {
		_ = users.SetSystemPassword(sysUser, pass)
	}
	if _, err := s.st.UpdatePanelUser(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if target.Enabled {
		_ = users.UnsuspendSystemUser(sysUser)
	} else {
		_ = users.SuspendSystemUser(sysUser)
	}
	pkgID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("package_id")), 10, 64)
	if pkgID > 0 {
		_ = s.st.AssignPackage(target.ID, pkgID, sess.UserID)
	} else {
		_ = s.st.UnassignPackage(target.ID)
	}
	http.Redirect(w, r, "/ui/users?info="+url.QueryEscape("user updated"), http.StatusFound)
}

func (s *Server) handleUserSuspend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	sess, _ := s.sessionFromCtx(r)
	username := strings.TrimSpace(r.FormValue("username"))
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "user" || !s.canManagePanelUser(sess, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	target.Enabled = false
	if _, err := s.st.UpdatePanelUser(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sysUser := strings.TrimSpace(target.SystemUser)
	if sysUser == "" {
		sysUser = target.Username
	}
	_ = users.SuspendSystemUser(sysUser)
	http.Redirect(w, r, "/ui/users", http.StatusFound)
}

func (s *Server) handleUserEnable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	sess, _ := s.sessionFromCtx(r)
	username := strings.TrimSpace(r.FormValue("username"))
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "user" || !s.canManagePanelUser(sess, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	target.Enabled = true
	if _, err := s.st.UpdatePanelUser(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sysUser := strings.TrimSpace(target.SystemUser)
	if sysUser == "" {
		sysUser = target.Username
	}
	_ = users.UnsuspendSystemUser(sysUser)
	http.Redirect(w, r, "/ui/users", http.StatusFound)
}
func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	sess, _ := s.sessionFromCtx(r)
	username := strings.TrimSpace(r.FormValue("username"))
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "user" || !s.canManagePanelUser(sess, target) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	_ = s.st.UnassignPackage(target.ID)
	if err := s.st.DeletePanelUser(target.ID); err != nil {
		http.Redirect(w, r, "/ui/users?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	sysUser := strings.TrimSpace(target.SystemUser)
	if sysUser == "" {
		sysUser = target.Username
	}
	_ = users.DeleteSystemUser(sysUser)
	http.Redirect(w, r, "/ui/users", http.StatusFound)
}
func (s *Server) handleResellers(w http.ResponseWriter, r *http.Request) {
	allUsers, err := s.st.ListPanelUsers()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	userCounts := map[int64]int{}
	var items []store.PanelUser
	for _, u := range allUsers {
		if u.Role == "user" && u.ResellerID != nil {
			userCounts[*u.ResellerID]++
		}
		if u.Role == "reseller" {
			items = append(items, u)
		}
	}
	s.render(w, r, "Resellers", "resellers", map[string]any{
		"Items":     items,
		"UserCount": userCounts,
		"Error":     strings.TrimSpace(r.URL.Query().Get("error")),
		"Info":      strings.TrimSpace(r.URL.Query().Get("info")),
	})
}
func (s *Server) handleResellerNew(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.render(w, r, "New Reseller", "reseller_form", map[string]any{
			"Mode":       "new",
			"FormAction": "/ui/resellers/new",
			"Form": map[string]string{
				"enabled": "true",
			},
		})
		return
	}
	_ = r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	pass := r.FormValue("password")
	if username == "" || pass == "" {
		s.render(w, r, "New Reseller", "reseller_form", map[string]any{
			"Mode":       "new",
			"FormAction": "/ui/resellers/new",
			"Error":      "username and password are required",
			"Form": map[string]string{
				"username": username,
				"enabled":  r.FormValue("enabled"),
			},
		})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	enabled := parseBool(r.FormValue("enabled"), true)
	if _, err := s.st.CreatePanelUser(username, string(hash), "reseller", enabled); err != nil {
		s.render(w, r, "New Reseller", "reseller_form", map[string]any{
			"Mode":       "new",
			"FormAction": "/ui/resellers/new",
			"Error":      err.Error(),
			"Form": map[string]string{
				"username": username,
				"enabled":  r.FormValue("enabled"),
			},
		})
		return
	}
	http.Redirect(w, r, "/ui/resellers?info="+url.QueryEscape("reseller created"), http.StatusFound)
}

func (s *Server) handleResellerEdit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		username = strings.TrimSpace(r.FormValue("username"))
	}
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "reseller" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		s.render(w, r, "Edit Reseller", "reseller_form", map[string]any{
			"Mode":       "edit",
			"FormAction": "/ui/resellers/edit",
			"PanelUser":  target,
			"Form": map[string]string{
				"username": target.Username,
				"enabled":  boolStr(target.Enabled),
			},
		})
		return
	}
	target.Enabled = parseBool(r.FormValue("enabled"), true)
	if pass := strings.TrimSpace(r.FormValue("password")); pass != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(pass), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		target.PasswordHash = string(hash)
	}
	if _, err := s.st.UpdatePanelUser(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/resellers?info="+url.QueryEscape("reseller updated"), http.StatusFound)
}

func (s *Server) handleResellerDisable(w http.ResponseWriter, r *http.Request) {
	s.setResellerEnabled(w, r, false)
}

func (s *Server) handleResellerEnable(w http.ResponseWriter, r *http.Request) {
	s.setResellerEnabled(w, r, true)
}

func (s *Server) handleResellerDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "reseller" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	allUsers, _ := s.st.ListPanelUsers()
	for _, u := range allUsers {
		if u.Role == "user" && u.ResellerID != nil && *u.ResellerID == target.ID {
			http.Redirect(w, r, "/ui/resellers?error="+url.QueryEscape("cannot delete reseller with owned users"), http.StatusFound)
			return
		}
	}
	if err := s.st.DeletePanelUser(target.ID); err != nil {
		http.Redirect(w, r, "/ui/resellers?error="+url.QueryEscape(err.Error()), http.StatusFound)
		return
	}
	http.Redirect(w, r, "/ui/resellers?info="+url.QueryEscape("reseller deleted"), http.StatusFound)
}

func (s *Server) setResellerEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_ = r.ParseForm()
	username := strings.TrimSpace(r.FormValue("username"))
	target, err := s.st.GetPanelUserByUsername(username)
	if err != nil || target.Role != "reseller" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	target.Enabled = enabled
	if _, err := s.st.UpdatePanelUser(target); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/ui/resellers", http.StatusFound)
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, _ := s.sessionFromCtx(r)
	scope := backup.BackupScope(strings.TrimSpace(r.URL.Query().Get("scope")))
	if scope == "" {
		if sess.Role == "user" {
			scope = backup.ScopeUser
		} else if sess.Role == "reseller" {
			scope = backup.ScopeReseller
		} else {
			scope = backup.ScopeAll
		}
	}
	user := strings.TrimSpace(r.URL.Query().Get("user"))
	if user == "" {
		user = sess.Username
	}
	switch scope {
	case backup.ScopeAll:
		if sess.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	case backup.ScopeReseller:
		if sess.Role != "admin" && !(sess.Role == "reseller" && user == sess.Username) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	case backup.ScopeUser:
		if sess.Role == "user" && user != sess.Username {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if sess.Role == "reseller" {
			pu, err := s.st.GetPanelUserByUsername(user)
			if err != nil || pu.ResellerID == nil || *pu.ResellerID != sess.UserID {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
	default:
		http.Error(w, "invalid scope", http.StatusBadRequest)
		return
	}
	includeCerts := parseBool(r.URL.Query().Get("include_certs"), false)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename=\"ngm-backup-"+string(scope)+"-"+user+".tar.gz\"")
	host, _ := os.Hostname()
	if _, err := backup.Create(s.st, backup.BackupOptions{
		Scope:        scope,
		Username:     user,
		IncludeCerts: includeCerts,
		NodeID:       host,
		Driver:       s.cfg.Storage.Driver,
		HomeRoot:     s.cfg.Hosting.HomeRoot,
		CertsRoot:    s.paths.LetsEncryptLive,
		Now:          time.Now().UTC(),
	}, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

// ---------------- helpers ----------------

func (s *Server) canManagePanelUser(sess Session, target store.PanelUser) bool {
	if sess.Role == "admin" {
		return true
	}
	if sess.Role == "reseller" && target.Role == "user" && target.ResellerID != nil && *target.ResellerID == sess.UserID {
		return true
	}
	return false
}

func (s *Server) listAssignablePackages(sess Session) ([]store.Package, error) {
	pkgs, err := s.st.ListPackages()
	if err != nil {
		return nil, err
	}
	if sess.Role != "reseller" {
		return pkgs, nil
	}
	filtered := make([]store.Package, 0, len(pkgs))
	for _, p := range pkgs {
		if p.CreatedBy != nil && *p.CreatedBy == sess.UserID {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

func (s *Server) userPackageMap() (map[int64]string, error) {
	items, err := s.st.ListPanelUsers()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(items))
	for _, u := range items {
		up, err := s.st.GetUserPackage(u.ID)
		if err != nil {
			continue
		}
		out[u.ID] = up.Package.Name
	}
	return out, nil
}

func parseBool(v string, def bool) bool {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func valueOrBlank(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

var (
	// nginx sizes like: 128m, 32M, 1g, 1024k (we keep it simple)
	reNginxSize = regexp.MustCompile(`^\d+[kKmMgG]?$`)
	// nginx times like: 60s, 300s, 5m, 250ms, 1h (simple subset)
	reNginxTime = regexp.MustCompile(`^\d+(ms|s|m|h)?$`)
)

func validateNginxKnobs(clientMax, phpRead, phpSend string) string {
	// empty is allowed => means "use defaults"
	if clientMax != "" && !reNginxSize.MatchString(clientMax) {
		return "Invalid Client Max Body Size. Examples: 32M, 128M, 1G (leave blank for default)."
	}
	if phpRead != "" && !reNginxTime.MatchString(phpRead) {
		return "Invalid PHP Read Timeout. Examples: 60s, 300s, 5m (leave blank for default)."
	}
	if phpSend != "" && !reNginxTime.MatchString(phpSend) {
		return "Invalid PHP Send Timeout. Examples: 60s, 300s, 5m (leave blank for default)."
	}
	return ""
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// ---- php.ini sidecar helpers (UI prefill) ----

func phpOverridesPathFromWebroot(webroot string) string {
	siteRoot := filepath.Dir(webroot) // .../<domain> because webroot ends with /public
	return filepath.Join(siteRoot, "php", "php.ini")
}

func readPHPOverridesFile(webroot string) string {
	p := phpOverridesPathFromWebroot(webroot)
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	raw := string(b)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	raw = strings.TrimRight(raw, "\n") // nicer textarea
	return raw
}

// ---------------- templates ----------------

const layoutHTML = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <title>{{.Title}}</title>
</head>
<body style="font-family:system-ui; margin:24px;">
  {{if .Authed}}{{template "menu" .}}{{end}}
  <div style="max-width:1100px;">
    {{template "content" .}}
  </div>
</body>
</html>`

const contentHTML = `{{define "content"}}
  {{- if eq .Page "sites" -}}
    {{template "sites" .}}
  {{- else if eq .Page "dashboard" -}}
    {{template "dashboard" .}}
  {{- else if eq .Page "packages" -}}
    {{template "packages" .}}
  {{- else if eq .Page "package_form" -}}
    {{template "package_form" .}}
  {{- else if eq .Page "users" -}}
    {{template "users" .}}
  {{- else if eq .Page "user_form" -}}
    {{template "user_form" .}}
  {{- else if eq .Page "resellers" -}}
    {{template "resellers" .}}
  {{- else if eq .Page "reseller_form" -}}
    {{template "reseller_form" .}}
  {{- else if eq .Page "site_form" -}}
    {{template "site_form" .}}
  {{- else if eq .Page "apply_form" -}}
    {{template "apply_form" .}}
  {{- else if eq .Page "apply_result" -}}
    {{template "apply_result" .}}
  {{- else if eq .Page "certs" -}}
    {{template "certs" .}}
  {{- else if eq .Page "cert_info" -}}
    {{template "cert_info" .}}
  {{- else if eq .Page "proxy_targets" -}}
    {{template "proxy_targets" .}}
  {{- else if eq .Page "cert_check" -}}
    {{template "cert_check" .}}
  {{- else -}}
    <h2>Unknown page</h2>
    <p>Page: <code>{{.Page}}</code></p>
  {{- end -}}
{{end}}`

const menuHTML = `{{define "menu"}}
  <div style="display:flex; gap:12px; align-items:center; margin-bottom:18px;">
    <div style="font-weight:700;">NGM</div>
    <a href="/ui/dashboard">Dashboard</a>
    {{if eq .Session.Role "admin"}}
      <a href="/ui/resellers">Resellers</a>
      <a href="/ui/users">Users</a>
      <a href="/ui/packages">Packages</a>
      <a href="/ui/sites">Sites</a>
      <a href="/ui/certs">Certificates</a>
      <a href="/ui/apply">Apply</a>
      <a href="/ui/backup">Backups</a>
    {{else if eq .Session.Role "reseller"}}
      <a href="/ui/users">My Users</a>
      <a href="/ui/packages">Packages</a>
      <a href="/ui/sites">Sites</a>
      <a href="/ui/certs">Certificates</a>
      <a href="/ui/backup">Backups</a>
    {{else}}
      <a href="/ui/sites">My Domains</a>
      <a href="/ui/certs">Certificates</a>
      <a href="/ui/backup">Backup</a>
    {{end}}

    <div style="margin-left:auto; display:flex; gap:10px; align-items:center;">
      <div style="opacity:.75;">{{.Session.Username}}</div>
      <a href="/ui/logout">Logout</a>
    </div>
  </div>
{{end}}`

const loginHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>NGM Login</title></head>
<body style="font-family:system-ui; max-width:520px; margin:40px auto;">
  <h2>NGM Panel Login</h2>
  {{if .Error}}<p style="color:#b00;">{{.Error}}</p>{{end}}
  <form method="post" action="/ui/login">
    <div style="margin:10px 0;">
      <label>Username</label><br/>
      <input name="username" style="width:100%; padding:8px;" />
    </div>
    <div style="margin:10px 0;">
      <label>Password</label><br/>
      <input type="password" name="password" style="width:100%; padding:8px;" />
    </div>
    <button style="padding:10px 14px;">Login</button>
  </form>
</body></html>`

const dashboardHTML = `{{define "dashboard"}}
<h2>Dashboard</h2>
{{if eq .Role "admin"}}
<ul><li>Resellers: {{.Resellers}}</li><li>Users: {{.Users}}</li><li>Domains: {{.Domains}}</li></ul>
{{else if eq .Role "reseller"}}
<ul><li>My users: {{.Users}}</li><li>My domains: {{.Domains}}</li></ul>
{{else}}
<ul><li>My domains: {{.Domains}}</li></ul>
{{end}}
{{end}}`

const packagesHTML = `{{define "packages"}}
<h2>Packages</h2>
<p><a href="/ui/packages/new">New package</a></p>
<table border="1" cellpadding="6" cellspacing="0">
<tr><th>ID</th><th>Name</th><th>Actions</th></tr>
{{range .Items}}<tr><td>{{.ID}}</td><td>{{.Name}}</td><td>
<form method="post" action="/ui/packages/edit" style="display:inline"><input type="hidden" name="id" value="{{.ID}}"><input name="name" value="{{.Name}}"><button>Rename</button></form>
<form method="post" action="/ui/packages/delete" style="display:inline"><input type="hidden" name="id" value="{{.ID}}"><button>Delete</button></form>
</td></tr>{{end}}
</table>
{{end}}`

const packageFormHTML = `{{define "package_form"}}
<h2>New package</h2>
{{if .Error}}<p style="color:#b00">{{.Error}}</p>{{end}}
<form method="post" action="/ui/packages/new">
<input name="name" placeholder="package name"><button>Create</button>
</form>{{end}}`

const usersHTML = `{{define "users"}}
<h2>Users</h2>
{{if .Error}}<p style="color:#b00">{{.Error}}</p>{{end}}
{{if .Info}}<p style="color:#060">{{.Info}}</p>{{end}}
<p><a href="/ui/users/new">New user</a></p>
<table border="1" cellpadding="6" cellspacing="0">
<tr><th>ID</th><th>Username</th><th>Enabled</th><th>Package</th><th>Owner</th><th>Actions</th></tr>
{{range .Items}}<tr>
<td>{{.User.ID}}</td>
<td>{{.User.Username}}</td>
<td>{{if .User.Enabled}}yes{{else}}no{{end}}</td>
<td>{{if .PackageName}}{{.PackageName}}{{else}}-{{end}}</td>
<td>{{.Owner}}</td>
<td>
  <a href="/ui/users/edit?username={{.User.Username}}">Edit</a>
  {{if .User.Enabled}}
  <form method="post" action="/ui/users/suspend" style="display:inline"><input type="hidden" name="username" value="{{.User.Username}}"><button>Suspend</button></form>
  {{else}}
  <form method="post" action="/ui/users/enable" style="display:inline"><input type="hidden" name="username" value="{{.User.Username}}"><button>Enable</button></form>
  {{end}}
  <form method="post" action="/ui/users/delete" style="display:inline" onsubmit="return confirm('Delete user {{.User.Username}}?');"><input type="hidden" name="username" value="{{.User.Username}}"><button>Delete</button></form>
</td>
</tr>{{end}}
</table>{{end}}`

const userFormHTML = `{{define "user_form"}}
<h2>{{if eq .Mode "edit"}}Edit user{{else}}New user{{end}}</h2>
{{if .Error}}<p style="color:#b00">{{.Error}}</p>{{end}}
<form method="post" action="{{.FormAction}}">
<table cellpadding="6">
<tr><td>Username</td><td><input name="username" value="{{index .Form "username"}}" {{if eq .Mode "edit"}}readonly{{end}}></td></tr>
<tr><td>Password {{if eq .Mode "edit"}}(optional reset){{end}}</td><td><input type="password" name="password"></td></tr>
<tr><td>Enabled</td><td><label><input type="checkbox" name="enabled" value="true" {{if ne (index .Form "enabled") "false"}}checked{{end}}> enabled</label></td></tr>
<tr><td>Package</td><td><select name="package_id"><option value="">-- none --</option>{{range .Packages}}<option value="{{.ID}}" {{if eq (printf "%d" .ID) (index $.Form "package_id")}}selected{{end}}>{{.Name}}</option>{{end}}</select></td></tr>
{{if .IsAdmin}}<tr><td>Reseller (optional)</td><td><select name="reseller_id"><option value="">-- direct user --</option>{{range .Resellers}}<option value="{{.ID}}" {{if eq (printf "%d" .ID) (index $.Form "reseller")}}selected{{end}}>{{.Username}}</option>{{end}}</select></td></tr>{{end}}
{{if .PanelUser}}<tr><td>System user</td><td>{{.PanelUser.SystemUser}}</td></tr><tr><td>Role</td><td>{{.PanelUser.Role}}</td></tr>{{end}}
</table>
<button>{{if eq .Mode "edit"}}Save{{else}}Create{{end}}</button>
</form>{{end}}`

const resellersHTML = `{{define "resellers"}}
<h2>Resellers</h2>
{{if .Error}}<p style="color:#b00">{{.Error}}</p>{{end}}
{{if .Info}}<p style="color:#060">{{.Info}}</p>{{end}}
<p><a href="/ui/resellers/new">New reseller</a></p>
<table border="1" cellpadding="6" cellspacing="0">
<tr><th>ID</th><th>Username</th><th>Enabled</th><th>Owned users</th><th>Actions</th></tr>
{{range .Items}}<tr>
<td>{{.ID}}</td><td>{{.Username}}</td><td>{{if .Enabled}}yes{{else}}no{{end}}</td><td>{{index $.UserCount .ID}}</td>
<td>
<a href="/ui/resellers/edit?username={{.Username}}">Edit</a>
{{if .Enabled}}
<form method="post" action="/ui/resellers/disable" style="display:inline"><input type="hidden" name="username" value="{{.Username}}"><button>Disable</button></form>
{{else}}
<form method="post" action="/ui/resellers/enable" style="display:inline"><input type="hidden" name="username" value="{{.Username}}"><button>Enable</button></form>
{{end}}
<form method="post" action="/ui/resellers/delete" style="display:inline" onsubmit="return confirm('Delete reseller {{.Username}}? This is blocked if owned users exist.');"><input type="hidden" name="username" value="{{.Username}}"><button>Delete</button></form>
</td></tr>{{end}}
</table>{{end}}`
const resellerFormHTML = `{{define "reseller_form"}}
<h2>{{if eq .Mode "edit"}}Edit reseller{{else}}New reseller{{end}}</h2>
{{if .Error}}<p style="color:#b00">{{.Error}}</p>{{end}}
<form method="post" action="{{.FormAction}}">
<table cellpadding="6">
<tr><td>Username</td><td><input name="username" value="{{index .Form "username"}}" {{if eq .Mode "edit"}}readonly{{end}}></td></tr>
<tr><td>Password {{if eq .Mode "edit"}}(optional reset){{end}}</td><td><input type="password" name="password"></td></tr>
<tr><td>Enabled</td><td><label><input type="checkbox" name="enabled" value="true" {{if ne (index .Form "enabled") "false"}}checked{{end}}> enabled</label></td></tr>
<tr><td>Role</td><td>reseller</td></tr>
</table>
<button>{{if eq .Mode "edit"}}Save{{else}}Create{{end}}</button>
</form>{{end}}`

const sitesHTML = `{{define "sites"}}
  <h2 style="margin:0 0 10px 0;">Sites</h2>
  <p style="opacity:.8; margin-top:0;">Manage sites and apply nginx changes.</p>
  <p><a href="/ui/sites/new">New site</a></p>

  <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%;">
    <thead>
      <tr>
        <th align="left">Domain</th>
        <th align="left">Parent</th>
        <th align="center">Type</th>
        <th>Owner</th>
        <th>Mode</th>
        <th>Enabled</th>
        <th>TLS</th>
        <th>State</th>
        <th>Last Applied</th>
        <th>PHP</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody>
    {{range .Items}}
      <tr>
        <td>{{.Site.Domain}}</td>
        <td>{{if .Site.ParentDomain}}{{.Site.ParentDomain}}{{else}}-{{end}}</td>
        <td align="center">{{if .Site.ParentDomain}}subdomain{{else}}root{{end}}</td>
        <td align="center">{{index $.Owners .Site.Domain}}</td>
        <td align="center">{{.Site.Mode}}</td>
        <td align="center">{{if .Site.Enabled}}yes{{else}}no{{end}}</td>
        <td align="center">
          {{ $ci := index $.Certs .Site.Domain }}
          {{ if $ci }}
            yes ({{ $ci.DaysLeft }}d)
          {{ else }}
            no
          {{ end }}
        </td>
        <td align="center">{{.State}}</td>
        <td align="center">{{.Last}}</td>
        <td align="center">{{.Site.PHPVersion}}</td>
        <td align="center" style="white-space:nowrap;">
          <form method="post" action="/ui/apply" style="display:inline;">
            <input type="hidden" name="domain" value="{{.Site.Domain}}">
            <button>Apply</button>
          </form>
          {{if eq .Site.Mode "proxy"}}
            <a href="/ui/sites/targets?domain={{.Site.Domain}}" style="margin-left:8px;">Targets</a>
          {{end}}
          <a href="/ui/sites/edit?domain={{.Site.Domain}}" style="margin-left:8px;">Edit</a>

{{if .Site.Enabled}}
            <form method="post" action="/ui/sites/disable" style="display:inline; margin-left:8px;"
                  onsubmit="return confirm('Disable {{.Site.Domain}} ?');">
              <input type="hidden" name="domain" value="{{.Site.Domain}}">
              <button>Disable</button>
            </form>
          {{else}}
            <form method="post" action="/ui/sites/enable" style="display:inline; margin-left:8px;"
                  onsubmit="return confirm('Enable {{.Site.Domain}} ?');">
              <input type="hidden" name="domain" value="{{.Site.Domain}}">
              <button>Enable</button>
            </form>
            <form method="post" action="/ui/sites/delete" style="display:inline; margin-left:8px;"
                  onsubmit="return confirm('DELETE {{.Site.Domain}} permanently? This cannot be undone.');">
              <input type="hidden" name="domain" value="{{.Site.Domain}}">
              <button>Delete</button>
            </form>
          {{end}}

        </td>
      </tr>
    {{end}}
    </tbody>
  </table>
{{end}}`

const siteFormHTML = `{{define "site_form"}}
  {{if eq .Mode "new"}}<h2>Add Site</h2>{{end}}
  {{if eq .Mode "edit"}}<h2>Edit Site</h2>{{end}}
  {{if eq .Mode "result"}}<h2>Result</h2>{{end}}

  {{if .Error}}<p style="color:#b00;">{{.Error}}</p>{{end}}
  {{if .Warnings}}
    <div style="border:1px solid #cc0; padding:10px; margin:10px 0;">
      <div style="font-weight:700;">Warnings</div>
      <ul>
        {{range .Warnings}}<li>{{.}}</li>{{end}}
      </ul>
    </div>
  {{end}}

  {{if eq .Mode "result"}}
    <pre style="background:#f6f6f6; padding:12px; overflow:auto;">{{printf "%+v" .Site}}</pre>
    <p><a href="/ui/sites">Back to Sites</a></p>
  {{else}}
    <form method="post" action="{{if eq .Mode "new"}}/ui/sites/new{{else}}/ui/sites/edit{{end}}">
      <div style="display:grid; grid-template-columns: 180px 1fr; gap:10px; max-width:820px;">
        <label>Domain</label>
        <input name="domain" value="{{index .Form "domain"}}" style="padding:8px;" {{if eq .Mode "edit"}}readonly{{end}}>

        <label>Parent Domain</label>
        <input name="parent" value="{{index .Form "parent"}}" style="padding:8px;" placeholder="optional root domain (e.g. example.com)">

        <label>User (owner)</label>
        <input name="user" value="{{index .Form "user"}}" style="padding:8px;" placeholder="e.g. chris">

        <label>Mode</label>
        <select name="mode" style="padding:8px;">
          <option value="php" {{if eq (index .Form "mode") "php"}}selected{{end}}>php</option>
          <option value="proxy" {{if eq (index .Form "mode") "proxy"}}selected{{end}}>proxy</option>
          <option value="static" {{if eq (index .Form "mode") "static"}}selected{{end}}>static</option>
        </select>

        <label>PHP Version</label>
        <input name="php" value="{{index .Form "php"}}" style="padding:8px;" placeholder="e.g. 8.4">

        <label>Webroot</label>
        <input name="webroot" value="{{index .Form "webroot"}}" style="padding:8px;" placeholder="optional">

        <label>Client Max Body Size</label>
        <input name="clientmax" value="{{index .Form "clientmax"}}" style="padding:8px;"
               placeholder="leave blank = default (e.g. 32M). Example: 128M">

        <label>PHP Read Timeout</label>
        <input name="phpread" value="{{index .Form "phpread"}}" style="padding:8px;"
               placeholder="php mode only. leave blank = default (e.g. 60s). Example: 300s">

        <label>PHP Send Timeout</label>
        <input name="phpsend" value="{{index .Form "phpsend"}}" style="padding:8px;"
               placeholder="php mode only. leave blank = default (e.g. 60s). Example: 300s">

        <label>PHP ini overrides (one per line)</label>
        <textarea name="phpini" style="padding:8px; min-height:140px;"
          placeholder="max_execution_time = 600&#10;memory_limit = 1024M&#10;upload_max_filesize = 1024M&#10;&#10;# optional: value:KEY = ... for php_value&#10;value:session.gc_maxlifetime = 1440">{{index .Form "phpini"}}</textarea>
        <div style="grid-column: 1 / span 2; opacity:.75; font-size:13px;">
          Stored as <code>php/php.ini</code> inside the site folder (not in SQLite). Leave empty to clear.
        </div>

        <label>HTTP/3</label>
        <select name="http3" style="padding:8px;">
          <option value="true" {{if eq (index .Form "http3") "true"}}selected{{end}}>true</option>
          <option value="false" {{if eq (index .Form "http3") "false"}}selected{{end}}>false</option>
        </select>

        {{if eq .Mode "new"}}
          <label>Proxy Targets (one per line)</label>
          <textarea name="targets" style="padding:8px; min-height:90px;"
            placeholder="127.0.0.1:8080&#10;10.0.0.2:8080 50 (optional weight)">{{index .Form "targets"}}</textarea>

          <div style="grid-column: 1 / span 2; opacity:.75; font-size:13px;">
            Used only when Mode=proxy. If empty, create site first, then add targets from the Targets page.
          </div>

          <label>Provision</label>
          <select name="provision" style="padding:8px;">
            <option value="true" {{if eq (index .Form "provision") "true"}}selected{{end}}>true</option>
            <option value="false" {{if eq (index .Form "provision") "false"}}selected{{end}}>false</option>
          </select>

          <label>Apply Now</label>
          <select name="applynow" style="padding:8px;">
            <option value="true" {{if eq (index .Form "applynow") "true"}}selected{{end}}>true</option>
            <option value="false" {{if eq (index .Form "applynow") "false"}}selected{{end}}>false</option>
          </select>

          <label>Skip Cert</label>
          <select name="skipcert" style="padding:8px;">
            <option value="false" {{if eq (index .Form "skipcert") "false"}}selected{{end}}>false</option>
            <option value="true" {{if eq (index .Form "skipcert") "true"}}selected{{end}}>true</option>
          </select>
        {{else}}
          <label>Enabled</label>
          <select name="enabled" style="padding:8px;">
            <option value="true" {{if eq (index .Form "enabled") "true"}}selected{{end}}>true</option>
            <option value="false" {{if eq (index .Form "enabled") "false"}}selected{{end}}>false</option>
          </select>

          <label>Apply Now</label>
          <select name="applynow" style="padding:8px;">
            <option value="true" {{if eq (index .Form "applynow") "true"}}selected{{end}}>true</option>
            <option value="false" {{if eq (index .Form "applynow") "false"}}selected{{end}}>false</option>
          </select>
        {{end}}
      </div>

      <div style="margin-top:14px;">
        <button style="padding:10px 14px;">Save</button>
        <a href="/ui/sites" style="margin-left:10px;">Cancel</a>
      </div>
    </form>
  {{end}}
{{end}}`

const proxyTargetsHTML = `{{define "proxy_targets"}}
  <h2>Proxy Targets: {{.Site.Domain}}</h2>
  <p style="opacity:.8; margin-top:0;">
    Manage upstream targets for this proxy site.
  </p>

  <div style="margin:10px 0; display:flex; gap:10px; align-items:center;">
    <form method="post" action="/ui/apply" style="display:inline;">
      <input type="hidden" name="domain" value="{{.Site.Domain}}">
      <button style="padding:8px 10px;">Apply</button>
    </form>
    <a href="/ui/sites">Back to Sites</a>
  </div>

  <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%; max-width:900px;">
    <thead>
      <tr>
        <th align="left">Target</th>
        <th>Weight</th>
        <th>Backup</th>
        <th>Enabled</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody>
    {{range .Targets}}
      <tr>
        <td>{{.Addr}}</td>
        <td align="center">{{.Weight}}</td>
        <td align="center">{{if .Backup}}yes{{else}}no{{end}}</td>
        <td align="center">{{if .Enabled}}yes{{else}}no{{end}}</td>
        <td align="center">
          <form method="post" action="/ui/sites/targets/del" style="display:inline;"
                onsubmit="return confirm('Disable target {{.Addr}} ?');">
            <input type="hidden" name="domain" value="{{$.Site.Domain}}">
            <input type="hidden" name="target" value="{{.Addr}}">
            <button>Disable</button>
          </form>
        </td>
      </tr>
    {{end}}
    </tbody>
  </table>

  <h3 style="margin-top:18px;">Add / Update target</h3>
  <form method="post" action="/ui/sites/targets/add" style="max-width:900px;">
    <input type="hidden" name="domain" value="{{.Site.Domain}}">
    <div style="display:grid; grid-template-columns: 180px 1fr; gap:10px;">
      <label>Target</label>
      <input name="target" style="padding:8px;" placeholder="127.0.0.1:8080 or unix:/run/app.sock">

      <label>Weight</label>
      <input name="weight" style="padding:8px;" value="100">

      <label>Backup</label>
      <select name="backup" style="padding:8px;">
        <option value="false" selected>false</option>
        <option value="true">true</option>
      </select>

      <label>Enabled</label>
      <select name="enabled" style="padding:8px;">
        <option value="true" selected>true</option>
        <option value="false">false</option>
      </select>
    </div>
    <div style="margin-top:12px;">
      <button style="padding:10px 14px;">Save Target</button>
    </div>
  </form>
{{end}}`

const applyFormHTML = `{{define "apply_form"}}
  <h2>Apply</h2>
  <p style="opacity:.8;">Renders/publishes nginx vhosts and reloads when needed.</p>

  <form method="post" action="/ui/apply" style="max-width:720px;">
    <div style="display:grid; grid-template-columns: 180px 1fr; gap:10px;">
      <label>Domain (optional)</label>
      <input name="domain" style="padding:8px;" placeholder="example.com">

      <label>All (apply even if not pending)</label>
      <select name="all" style="padding:8px;">
        <option value="false" selected>false</option>
        <option value="true">true</option>
      </select>

      <label>Dry run</label>
      <select name="dry" style="padding:8px;">
        <option value="false" selected>false</option>
        <option value="true">true</option>
      </select>

      <label>Limit (0 = unlimited)</label>
      <input name="limit" style="padding:8px;" value="0">
    </div>

    <div style="margin-top:14px;">
      <button style="padding:10px 14px;">Run Apply</button>
    </div>
  </form>
{{end}}`

const applyResultHTML = `{{define "apply_result"}}
  <h2>Apply Result</h2>
  {{if .Error}}<p style="color:#b00;">{{.Error}}</p>{{end}}

  {{with .Result}}
    <p style="opacity:.8;">
      Reloaded: <b>{{.Reloaded}}</b>
      &nbsp; Changed: <b>{{len .Changed}}</b>
    </p>

    <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%;">
      <thead>
        <tr>
          <th align="left">Domain</th>
          <th>Action</th>
          <th>Status</th>
          <th>Changed</th>
          <th align="left">Error</th>
        </tr>
      </thead>
      <tbody>
      {{range .Domains}}
        <tr>
          <td>{{.Domain}}</td>
          <td align="center">{{.Action}}</td>
          <td align="center">{{.Status}}</td>
          <td align="center">{{if .Changed}}yes{{else}}no{{end}}</td>
          <td><pre style="white-space:pre-wrap; margin:0;">{{.Error}}</pre></td>
        </tr>
      {{end}}
      </tbody>
    </table>
  {{end}}

  <p style="margin-top:14px;">
    <a href="/ui/sites">Back to Sites</a>
    &nbsp;|&nbsp;
    <a href="/ui/apply">Apply again</a>
  </p>
{{end}}`

const certsHTML = `{{define "certs"}}
  <h2>Certificates</h2>

  <div style="margin:10px 0; padding:10px; border:1px solid #ddd;">
    <form method="get" action="/ui/cert/check" style="display:flex; gap:10px; align-items:center;">
      <div>Check expiring within</div>
      <input name="days" value="30" style="padding:6px; width:80px;">
      <div>days</div>
      <button style="padding:8px 10px;">Check</button>
    </form>
  </div>

  <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%;">
    <thead>
      <tr>
        <th align="left">Domain</th>
        <th>Days Left</th>
        <th>Not Before</th>
        <th>Not After</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody>
    {{range .Items}}
      <tr>
        <td>{{.Domain}}</td>
        <td align="center">{{.DaysLeft}}</td>
        <td align="center">{{.NotBefore.Format "2006-01-02 15:04"}}</td>
        <td align="center">{{.NotAfter.Format "2006-01-02 15:04"}}</td>
        <td align="center" style="white-space:nowrap;">
          <a href="/ui/cert/info?domain={{.Domain}}">Info</a>
          <form method="post" action="/ui/cert/issue" style="display:inline; margin-left:8px;"
                onsubmit="return confirm('Issue/renew certificate for {{.Domain}} ?');">
            <input type="hidden" name="domain" value="{{.Domain}}">
            <button>Issue</button>
          </form>
        </td>
      </tr>
    {{end}}
    </tbody>
  </table>

  <div style="margin-top:14px; padding:10px; border:1px solid #ddd;">
    <form method="post" action="/ui/cert/renew" onsubmit="return confirm('Renew ALL certificates?');">
      <input type="hidden" name="all" value="true">
      <button style="padding:10px 14px;">Renew All</button>
    </form>
  </div>
{{end}}`

const certInfoHTML = `{{define "cert_info"}}
  <h2>Certificate Info</h2>

  {{if or (not .Info) (not .Info.Exists)}}
    <p>Certificate does not exist.</p>
  {{else}}
    <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%; max-width:900px;">
      <tr><td><b>Domain</b></td><td>{{.Info.Domain}}</td></tr>
      <tr><td><b>Cert Path</b></td><td>{{.Info.CertPath}}</td></tr>
      <tr><td><b>Key Path</b></td><td>{{.Info.KeyPath}}</td></tr>
      <tr><td><b>Not Before</b></td><td>{{.Info.NotBefore.Format "2006-01-02 15:04:05"}}</td></tr>
      <tr><td><b>Not After</b></td><td>{{.Info.NotAfter.Format "2006-01-02 15:04:05"}}</td></tr>
      <tr><td><b>Days Left</b></td><td>{{.Info.DaysLeft}}</td></tr>
    </table>

    <div style="margin-top:12px;">
      <form method="post" action="/ui/cert/issue" style="display:inline;"
            onsubmit="return confirm('Issue/renew certificate for {{.Info.Domain}} ?');">
        <input type="hidden" name="domain" value="{{.Info.Domain}}">
        <button style="padding:10px 14px;">Issue / Renew</button>
      </form>

      <form method="post" action="/ui/cert/renew" style="display:inline; margin-left:10px;">
        <input type="hidden" name="domain" value="{{.Info.Domain}}">
        <button style="padding:10px 14px;">Renew (single)</button>
      </form>
    </div>
  {{end}}

  <p style="margin-top:14px;"><a href="/ui/certs">Back to Certificates</a></p>
{{end}}`

const certCheckHTML = `{{define "cert_check"}}
  <h2>Certificates expiring within {{.Days}} days</h2>

  {{if not .Items}}
    <p>No certificates expiring soon.</p>
  {{else}}
    <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%;">
      <thead>
        <tr>
          <th align="left">Domain</th>
          <th>Days Left</th>
          <th>Expires</th>
        </tr>
      </thead>
      <tbody>
      {{range .Items}}
        {{if le .DaysLeft $.Days}}
          <tr>
            <td>{{.Domain}}</td>
            <td align="center">{{.DaysLeft}}</td>
            <td align="center">{{.NotAfter.Format "2006-01-02 15:04"}}</td>
          </tr>
        {{end}}
      {{end}}
      </tbody>
    </table>
  {{end}}

  <p style="margin-top:14px;"><a href="/ui/certs">Back to Certificates</a></p>
{{end}}`
