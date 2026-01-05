package web

import (
	"context"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
        "regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"mynginx/internal/app"
	"mynginx/internal/config"
	"mynginx/internal/store"
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
		http.Redirect(w, r, "/ui/sites", http.StatusFound)
	})

	// auth
	mux.HandleFunc("/ui/login", s.handleLogin)
	mux.HandleFunc("/ui/logout", s.requireAuth(s.handleLogout))

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

		u, err := s.st.GetPanelUserByUsername(username)
		if err != nil || !u.Enabled {
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": "Invalid credentials"})
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass)) != nil {
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": "Invalid credentials"})
			return
		}

		sess, err := s.sessions.New(u.ID, u.Username, u.Role)
		if err != nil {
			_ = s.tpl.ExecuteTemplate(w, "login", map[string]any{"Error": "Login failed"})
			return
		}

		_ = s.st.UpdatePanelUserLastLogin(u.ID)
		s.setSessionCookie(w, r, sess.Token)
		http.Redirect(w, r, "/ui/sites", http.StatusFound)
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
	items, err := s.core.SiteList(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
        // Optional enrich for UI: owner username + cert info
        owners := map[string]string{}
        certs := map[string]any{} // domain -> *certs.CertInfo (stored as interface for templates)
        for _, it := range items {
                if it.Site.UserID != 0 {
                        if u, err := s.st.GetUserByID(it.Site.UserID); err == nil {
                                owners[it.Site.Domain] = u.Username
                        }
                }
                if ci, err := s.core.CertInfo(it.Site.Domain); err == nil && ci != nil && ci.Exists {
                        certs[it.Site.Domain] = ci
                }
        }

        s.render(w, r, "Sites", "sites", map[string]any{
                "Items":  items,
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
				"http3":     "true",
				"provision": "true",
				"applynow":  "true",
                                "targets":   "",
                                // Leave blank = use defaults in template render.
                                "clientmax": "",
                                "phpread":   "",
                                "phpsend":   "",
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

                if errMsg := validateNginxKnobs(clientMax, phpRead, phpSend); errMsg != "" {
                        s.render(w, r, "Add Site", "site_form", map[string]any{
                                "Mode":  "new",
                                "Error": errMsg,
                                "Form": map[string]any{
                                        "user":      strings.TrimSpace(r.FormValue("user")),
                                        "domain":    strings.TrimSpace(r.FormValue("domain")),
                                        "mode":      strings.TrimSpace(r.FormValue("mode")),
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
                                },
                        })
                        return
                }


		req := app.SiteAddRequest{
			User:      strings.TrimSpace(r.FormValue("user")),
			Domain:    strings.TrimSpace(r.FormValue("domain")),
			Mode:      strings.TrimSpace(r.FormValue("mode")),
			PHP:       strings.TrimSpace(r.FormValue("php")),
			Webroot:   strings.TrimSpace(r.FormValue("webroot")),
			HTTP3:     parseBool(r.FormValue("http3"), true),
			Provision: parseBool(r.FormValue("provision"), true),
			SkipCert:  parseBool(r.FormValue("skipcert"), false),
			ApplyNow:  parseBool(r.FormValue("applynow"), true),
                        ProxyTargets: targets,
                        ClientMaxBodySize: clientMax,
                        PHPTimeRead:       phpRead,
                        PHPTimeSend:       phpSend,
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
					"php":       req.PHP,
					"webroot":   req.Webroot,
					"http3":     boolStr(req.HTTP3),
					"provision": boolStr(req.Provision),
					"skipcert":  boolStr(req.SkipCert),
					"applynow":  boolStr(req.ApplyNow),
                                        "targets":   targetsRaw,
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

		s.render(w, r, "Edit Site", "site_form", map[string]any{
			"Mode": "edit",
			"Form": map[string]any{
				"domain":   cur.Domain,
                                "user":     owner,
				"mode":     cur.Mode,
				"php":      cur.PHPVersion,
				"webroot":  cur.Webroot,
				"http3":    boolStr(cur.EnableHTTP3),
				"enabled":  boolStr(cur.Enabled),
				"applynow": "false",
                                "clientmax": cur.ClientMaxBodySize,
                                "phpread":   cur.PHPTimeRead,
                                "phpsend":   cur.PHPTimeSend,
			},
		})
		return

	case http.MethodPost:
		_ = r.ParseForm()

		domain := strings.TrimSpace(r.FormValue("domain"))
		http3 := parseBool(r.FormValue("http3"), true)
		enabled := parseBool(r.FormValue("enabled"), true)
		applyNow := parseBool(r.FormValue("applynow"), false)

                clientMax := strings.TrimSpace(r.FormValue("clientmax"))
                phpRead := strings.TrimSpace(r.FormValue("phpread"))
                phpSend := strings.TrimSpace(r.FormValue("phpsend"))

                if errMsg := validateNginxKnobs(clientMax, phpRead, phpSend); errMsg != "" {
                        s.render(w, r, "Edit Site", "site_form", map[string]any{
                                "Mode":  "edit",
                                "Error": errMsg,
                                "Form": map[string]any{
                                        "domain":   domain,
                                        "user":     strings.TrimSpace(r.FormValue("user")),
                                        "mode":     strings.TrimSpace(r.FormValue("mode")),
                                        "php":      strings.TrimSpace(r.FormValue("php")),
                                        "webroot":  strings.TrimSpace(r.FormValue("webroot")),
                                        "http3":    boolStr(http3),
                                        "enabled":  boolStr(enabled),
                                        "applynow": boolStr(applyNow),
                                        "clientmax": clientMax,
                                        "phpread":   phpRead,
                                        "phpsend":   phpSend,
                                },
                        })
                        return
                }


		req := app.SiteEditRequest{
			Domain:   domain,
			User:     strings.TrimSpace(r.FormValue("user")),
			Mode:     strings.TrimSpace(r.FormValue("mode")),
			PHP:      strings.TrimSpace(r.FormValue("php")),
			Webroot:  strings.TrimSpace(r.FormValue("webroot")),
			HTTP3:    &http3,
			Enabled:  &enabled,
			ApplyNow: applyNow,
                        ClientMaxBodySize: clientMax,
                        PHPTimeRead:       phpRead,
                        PHPTimeSend:       phpSend,
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
							"domain":   req.Domain,
							"user":     req.User,
							"mode":     req.Mode,
							"php":      req.PHP,
							"webroot":  req.Webroot,
							"http3":    boolStr(http3),
							"enabled":  boolStr(enabled),
							"applynow": boolStr(applyNow),
                                                        "clientmax": clientMax,
                                                        "phpread":   phpRead,
                                                        "phpsend":   phpSend,
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
					"domain":   req.Domain,
					"user":     req.User,
					"mode":     req.Mode,
					"php":      req.PHP,
					"webroot":  req.Webroot,
					"http3":    boolStr(http3),
					"enabled":  boolStr(enabled),
					"applynow": boolStr(applyNow),
                                        "clientmax": clientMax,
                                        "phpread":   phpRead,
                                        "phpsend":   phpSend,
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
		domain := strings.TrimSpace(r.FormValue("domain"))
		all := parseBool(r.FormValue("all"), false)
		dry := parseBool(r.FormValue("dry"), false)
		limit, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("limit")))

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
	s.render(w, r, "Certificates", "certs", map[string]any{"Items": items})
}

func (s *Server) handleCertInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	d := strings.TrimSpace(r.URL.Query().Get("domain"))
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
	s.render(w, r, "Cert Check", "cert_check", map[string]any{
		"Days":  days,
		"Items": items,
	})
}

// ---------------- helpers ----------------

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
    <a href="/ui/sites">Sites</a>
    <a href="/ui/sites/new">Add Site</a>
    <a href="/ui/apply">Apply</a>
    <a href="/ui/certs">Certificates</a>

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

const sitesHTML = `{{define "sites"}}
  <h2 style="margin:0 0 10px 0;">Sites</h2>
  <p style="opacity:.8; margin-top:0;">Manage sites and apply nginx changes.</p>

  <table cellpadding="8" cellspacing="0" border="1" style="border-collapse:collapse; width:100%;">
    <thead>
      <tr>
        <th align="left">Domain</th>
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
            <option value="false" {{if eq (index .Form "applynow") "false"}}selected{{end}}>false</option>
            <option value="true" {{if eq (index .Form "applynow") "true"}}selected{{end}}>true</option>
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
