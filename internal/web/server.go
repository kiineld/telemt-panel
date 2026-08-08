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
	"github.com/kiineld/telemt-panel/internal/docker"
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
	// LinkOK is false when Link has nothing usable to show: no telemt
	// self-reported link yet, and PublicHost unset (so the local fallback
	// would just be the literal placeholder host "SERVER-IP"). Templates
	// must not render Link or QR as a real, copyable link when this is
	// false — see linkFor in handlers_proxy.go.
	LinkOK bool
	// QR is a data: URI. html/template's contextual autoescaper treats data:
	// URIs in a src attribute as unsafe by default and silently replaces
	// them with "#ZgotmplZ" — safe framework behavior in general (a data:
	// URI is a classic way to smuggle a script), but wrong here because QR
	// is never attacker-influenced: it is qrDataURI's own base64 encoding of
	// a PNG this process just rendered from the proxy link. template.URL
	// marks it as pre-vetted so the autoescaper passes it through unchanged.
	QR   template.URL
	Logs string

	// The detail page's limits form needs its numeric/date fields
	// pre-rendered as strings — a *uint64/*int/*string on store.Proxy has no
	// natural string form an html/template value="" attribute can use
	// directly, and an empty limit must render as an empty field, not "0" or
	// "<nil>".
	QuotaGB     string
	ExpiresDate string
	MaxConns    string
	MaxIPs      string

	// DockerOK and Orphans are populated only by getIndex, since only the
	// proxy list needs them; every other page leaves DockerOK at its zero
	// value (false), which is why the "Docker unreachable" banner lives in
	// proxies.html rather than layout.html — putting it in the shared layout
	// would make it render spuriously on every page that never sets DockerOK.
	DockerOK bool
	Orphans  []docker.ContainerInfo
}

// NewServer builds the panel's HTTP handler: it parses every template up
// front (a bad template is a boot-time failure, not a request-time one) and
// wires the full route table behind the appropriate auth middleware.
func NewServer(d ServerDeps) (http.Handler, error) {
	s := &server{ServerDeps: d, tmpl: map[string]*template.Template{}}

	for _, name := range []string{"login.html", "change_password.html", "proxies.html", "proxy.html"} {
		t, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(webassets.FS,
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
	mux.Handle("POST /orphans/{id}/delete", s.authed(s.requirePassword(s.postRemoveOrphan)))

	return mux, nil
}

// templateFuncs are helpers available to every parsed template.
var templateFuncs = template.FuncMap{
	// shortID truncates a container id for display. Real Docker ids are 64
	// hex characters, but the test fake's ids (e.g. "ctr1") are much shorter;
	// a bare slice(.ID, 0, 12) in the template would panic on those, so this
	// clamps the length instead of assuming it.
	"shortID": func(id string) string {
		if len(id) > 12 {
			return id[:12]
		}
		return id
	},
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
