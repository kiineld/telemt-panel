package web

import (
	"errors"
	"fmt"
	"net/http"
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
		out = append(out, row{
			Proxy: p, Stats: snap,
			Link:    s.Proxy.Link(p),
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
