package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
)

type row struct {
	Proxy   store.Proxy
	Stats   poller.Snapshot
	Link    string
	LinkOK  bool
	// Host is the informational "host:port" address shown next to the link
	// (not itself copyable or in the QR). It mirrors whichever host the link
	// actually uses — see displayHost — so this line never shows a different
	// address than the link below/beside it.
	Host    string
	Traffic string
}

func (s *server) buildRows(r *http.Request) ([]row, error) {
	proxies, err := s.Proxy.List(r.Context())
	if err != nil {
		return nil, err
	}
	stats := s.Poller.All()

	out := make([]row, 0, len(proxies))
	for _, p := range proxies {
		snap := stats[p.ID]
		link, ok := s.linkFor(p, snap)
		out = append(out, row{
			Proxy: p, Stats: snap,
			Link: link, LinkOK: ok, Host: s.displayHost(link, ok),
			Traffic: formatTraffic(snap.TotalOctets, p.DataQuotaBytes),
		})
	}
	return out, nil
}

func (s *server) host() string {
	if s.Cfg.PublicHost != "" {
		return s.Cfg.PublicHost
	}
	return "SERVER-IP"
}

// displayHost is the host shown on the informational "host:port" line next
// to a proxy's link. When a real link is showing, it reads the host straight
// out of that link — via the same "server" query parameter link.FakeTLS
// wrote — instead of separately recomputing it, so this line can never
// disagree with the link/QR next to it. That divergence was possible before:
// with PublicHost unset, the zero-config path can show telemt's own
// self-reported host in the link (see linkFor/proxy.ReconcileLink) while
// this line fell back to the placeholder "SERVER-IP", showing the operator
// two different hosts on one row. Falls back to the placeholder only when
// there is no usable link at all to read a host from.
func (s *server) displayHost(link string, linkOK bool) string {
	if linkOK {
		if u, err := url.Parse(link); err == nil {
			if h := u.Query().Get("server"); h != "" {
				return h
			}
		}
	}
	return s.host()
}

// linkFor decides which tg:// link, if any, to show for a proxy, via
// proxy.ReconcileLink — see its doc comment for the "prefer telemt's
// self-reported link" reasoning.
//
// When no telemt link is available yet AND PublicHost is unset, there is
// nothing usable to show: the locally computed link would embed the literal
// placeholder host "SERVER-IP", which is not a real address and would
// silently reach nobody if copied or scanned. ok is false in that case so
// the caller shows a warning instead of a broken link and its QR code.
func (s *server) linkFor(p store.Proxy, snap poller.Snapshot) (link string, ok bool) {
	publicHostSet := s.Cfg.PublicHost != ""
	local := ""
	if publicHostSet {
		local = s.Proxy.Link(p)
	}
	l, fromTelemt := proxy.ReconcileLink(local, publicHostSet, snap.Links)
	return l, fromTelemt || l != ""
}

func (s *server) getIndex(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	rows, err := s.buildRows(r)
	if err != nil {
		s.render(w, http.StatusInternalServerError, "proxies.html",
			page{Title: "Proxies", Admin: &adm, Error: err.Error(), Host: s.host()})
		return
	}

	pg := page{Title: "Proxies", Admin: &adm, Rows: rows, Host: s.host()}
	pg.DockerOK = s.Proxy.DockerOK(r.Context())
	if pg.DockerOK {
		// Orphans() itself calls Runtime.List, which would just fail the same
		// way Ping did; skip the extra round trip to a daemon already known
		// to be unreachable.
		pg.Orphans, _ = s.Proxy.Orphans(r.Context())
	}
	s.render(w, http.StatusOK, "proxies.html", pg)
}

func (s *server) postCreate(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	if err := r.ParseForm(); err != nil {
		s.createError(w, r, adm, "Malformed form.")
		return
	}

	port, err := strconv.Atoi(r.PostFormValue("port"))
	if err != nil {
		s.createError(w, r, adm, "Port must be a number.")
		return
	}

	req := proxy.CreateRequest{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		Port:      port,
		TLSDomain: strings.TrimSpace(r.PostFormValue("tls_domain")),
		AdTag:     strings.TrimSpace(r.PostFormValue("ad_tag")),
	}
	if v := r.PostFormValue("quota_gb"); v != "" {
		gb, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			s.createError(w, r, adm, "Data quota must be a whole number of GB.")
			return
		}
		bytes := gb * 1024 * 1024 * 1024
		req.DataQuotaBytes = &bytes
	}
	if v := r.PostFormValue("expires"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			s.createError(w, r, adm, "Expiry date is not valid.")
			return
		}
		exp := t.UTC().Format(time.RFC3339)
		req.ExpirationRFC3339 = &exp
	}
	if v := r.PostFormValue("max_conns"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.createError(w, r, adm, "Max connections must be a number.")
			return
		}
		req.MaxTCPConns = &n
	}
	if v := r.PostFormValue("max_ips"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.createError(w, r, adm, "Max unique IPs must be a number.")
			return
		}
		req.MaxUniqueIPs = &n
	}

	if _, err := s.Proxy.Create(r.Context(), req); err != nil {
		msg := err.Error()
		switch {
		case errors.Is(err, proxy.ErrPortReserved):
			msg = fmt.Sprintf("Port %d is reserved by the panel's web server. Pick another.", port)
		case errors.Is(err, store.ErrPortTaken):
			msg = fmt.Sprintf("Port %d is already used by another proxy.", port)
		case errors.Is(err, proxy.ErrPortConflict):
			msg = fmt.Sprintf("Port %d is already in use by something else on this host (outside the panel's tracking) — Docker rejected it. Pick another port or free that one.", port)
		}
		s.createError(w, r, adm, msg)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) createError(w http.ResponseWriter, r *http.Request, adm store.Admin, msg string) {
	rows, _ := s.buildRows(r)
	s.render(w, http.StatusBadRequest, "proxies.html",
		page{Title: "Proxies", Admin: &adm, Error: msg, Rows: rows, Host: s.host()})
}

func (s *server) postDelete(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	if err := s.Proxy.Delete(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// getProxy, postLimits, postRecreate and getLogs — the single-proxy detail
// page — live in handlers_detail.go.

func formatTraffic(used uint64, quota *uint64) string {
	if quota != nil && *quota > 0 {
		return fmt.Sprintf("%s / %s", humanBytes(used), humanBytes(*quota))
	}
	return humanBytes(used)
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
