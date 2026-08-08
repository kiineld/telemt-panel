// Package web is the panel's HTTP server: router, session-cookie auth
// middleware, server-rendered templates and the SSE feed that keeps the
// proxy list's counters live.
package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	webassets "github.com/kiineld/telemt-panel/web"
)

const sessionCookie = "mtpanel_session"

type ServerDeps struct {
	Auth   *Auth
	Proxy  *proxy.Service
	Poller *poller.Poller
	Cfg    config.Config
}

type server struct {
	ServerDeps
	tmpl map[string]*template.Template
}

// page is what every template receives.
type page struct {
	Title string
	Admin *store.Admin
	Error string
	Host  string
	Rows  []row
	Proxy *store.Proxy
	Stats poller.Snapshot
	Link  string
	QR    string
	Logs  string
}

// NewServer builds the panel's HTTP handler: it parses every template up
// front (a bad template is a boot-time failure, not a request-time one) and
// wires the full route table behind the appropriate auth middleware.
func NewServer(d ServerDeps) (http.Handler, error) {
	s := &server{ServerDeps: d, tmpl: map[string]*template.Template{}}

	// proxy.html (the single-proxy detail page) is a later task's
	// deliverable and does not exist yet, so it is deliberately not parsed
	// here.
	for _, name := range []string{"login.html", "change_password.html", "proxies.html"} {
		t, err := template.ParseFS(webassets.FS,
			"templates/layout.html",
			"templates/_rows.html",
			"templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", name, err)
		}
		s.tmpl[name] = t
	}

	staticFS, err := fs.Sub(webassets.FS, "static")
	if err != nil {
		return nil, fmt.Errorf("web: static assets: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /login", s.getLogin)
	mux.HandleFunc("POST /login", s.postLogin)
	mux.HandleFunc("POST /logout", s.postLogout)

	mux.Handle("GET /password", s.authed(s.getPassword))
	mux.Handle("POST /password", s.authed(s.postPassword))

	mux.Handle("GET /{$}", s.authed(s.requirePassword(s.getIndex)))
	mux.Handle("POST /proxies", s.authed(s.requirePassword(s.postCreate)))
	mux.Handle("GET /proxies/{id}", s.authed(s.requirePassword(s.getProxy)))
	mux.Handle("POST /proxies/{id}/limits", s.authed(s.requirePassword(s.postLimits)))
	mux.Handle("POST /proxies/{id}/recreate", s.authed(s.requirePassword(s.postRecreate)))
	mux.Handle("POST /proxies/{id}/delete", s.authed(s.requirePassword(s.postDelete)))
	mux.Handle("GET /proxies/{id}/logs", s.authed(s.requirePassword(s.getLogs)))
	mux.Handle("GET /events", s.authed(s.requirePassword(s.getEvents)))

	return mux, nil
}

type handlerWithAdmin func(http.ResponseWriter, *http.Request, store.Admin)

// authed rejects unauthenticated requests, redirecting browsers to /login.
func (s *server) authed(next handlerWithAdmin) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			redirectLogin(w, r)
			return
		}
		adm, err := s.Auth.Session(r.Context(), c.Value)
		if err != nil {
			redirectLogin(w, r)
			return
		}
		next(w, r, adm)
	})
}

// requirePassword funnels an admin who has never set a password to /password.
func (s *server) requirePassword(next handlerWithAdmin) handlerWithAdmin {
	return func(w http.ResponseWriter, r *http.Request, adm store.Admin) {
		if adm.MustChangePassword {
			http.Redirect(w, r, "/password", http.StatusSeeOther)
			return
		}
		next(w, r, adm)
	}
}

func redirectLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) render(w http.ResponseWriter, status int, name string, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl[name].ExecuteTemplate(w, "layout", p); err != nil {
		// Headers are already written; all we can do is stop.
		return
	}
}

// clientIP is the peer address; the panel sits behind Caddy on a private
// network, so X-Forwarded-For is deliberately not trusted for rate limiting
// — trusting a client-supplied header here would let an attacker defeat
// per-IP login rate limiting simply by rotating it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
