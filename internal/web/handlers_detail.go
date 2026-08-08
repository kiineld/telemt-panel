// handlers_detail.go implements the single-proxy detail page: the
// connection link and QR code, live connected-IP stats, editable limits,
// port/fake-domain recreate, and container logs.
package web

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
)

// qrDataURI renders link as a PNG QR code and returns it as a data URI, so
// the page needs neither a separate image request nor a CDN — it stays
// self-contained, per the panel's no-runtime-CDN constraint.
func qrDataURI(link string) (string, error) {
	png, err := qrcode.Encode(link, qrcode.Medium, 440)
	if err != nil {
		return "", fmt.Errorf("web: encode qr: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func (s *server) getProxy(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	p, err := s.Proxy.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	snap, _ := s.Poller.Get(p.ID)
	link, linkOK := s.linkFor(p, snap)

	var qr string
	if linkOK {
		q, err := qrDataURI(link)
		if err == nil {
			// A broken QR image isn't worth failing the whole page over —
			// the link text and copy button above it still work without it.
			qr = q
		}
	}
	logs, _ := s.Proxy.Logs(r.Context(), p.ID)

	s.render(w, http.StatusOK, "proxy.html", detailPage(adm, p, snap, link, linkOK, qr, logs, s.host()))
}

// detailPage fills the page fields the detail view needs beyond what every
// page carries (Title/Admin/Host).
func detailPage(adm store.Admin, p store.Proxy, snap poller.Snapshot, link string, linkOK bool, qr, logs, host string) page {
	pg := page{
		Title: p.Name, Admin: &adm, Host: host,
		Proxy: &p, Stats: snap, Link: link, LinkOK: linkOK, QR: template.URL(qr), Logs: logs,
	}
	if p.DataQuotaBytes != nil {
		pg.QuotaGB = strconv.FormatUint(*p.DataQuotaBytes/(1024*1024*1024), 10)
	}
	if p.ExpirationRFC3339 != nil {
		if t, err := time.Parse(time.RFC3339, *p.ExpirationRFC3339); err == nil {
			pg.ExpiresDate = t.Format("2006-01-02")
		}
	}
	if p.MaxTCPConns != nil {
		pg.MaxConns = strconv.Itoa(*p.MaxTCPConns)
	}
	if p.MaxUniqueIPs != nil {
		pg.MaxIPs = strconv.Itoa(*p.MaxUniqueIPs)
	}
	return pg
}

// postLimits applies the hot-reloadable limits (ad tag, data quota,
// expiration, connection/IP caps). The limits form always submits every
// field, pre-filled with the proxy's current values, so every field here is
// always "set" in LimitsPatch's tri-state sense: a field left as-is round
// trips its current value, and a field the operator clears goes through
// optionalGB/optionalInt/optionalDate as an explicit nil — which
// LimitsPatch's double-pointer and, downstream, client.Opt's tri-state
// (client.From) turn into an explicit JSON null on the wire, clearing the
// limit rather than omitting the key and leaving it untouched.
func (s *server) postLimits(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}

	patch := proxy.LimitsPatch{}

	tag := strings.TrimSpace(r.PostFormValue("ad_tag"))
	patch.AdTag = &tag

	quota, err := optionalGB(r.PostFormValue("quota_gb"))
	if err != nil {
		http.Error(w, "data quota must be a whole number of GB", http.StatusBadRequest)
		return
	}
	patch.DataQuotaBytes = &quota

	exp, err := optionalDate(r.PostFormValue("expires"))
	if err != nil {
		http.Error(w, "expiry date is not valid", http.StatusBadRequest)
		return
	}
	patch.ExpirationRFC3339 = &exp

	conns, err := optionalInt(r.PostFormValue("max_conns"))
	if err != nil {
		http.Error(w, "max connections must be a number", http.StatusBadRequest)
		return
	}
	patch.MaxTCPConns = &conns

	ips, err := optionalInt(r.PostFormValue("max_ips"))
	if err != nil {
		http.Error(w, "max unique IPs must be a number", http.StatusBadRequest)
		return
	}
	patch.MaxUniqueIPs = &ips

	if _, err := s.Proxy.UpdateLimits(r.Context(), id, patch); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/proxies/"+id, http.StatusSeeOther)
}

func (s *server) postRecreate(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "malformed form", http.StatusBadRequest)
		return
	}
	port, err := strconv.Atoi(r.PostFormValue("port"))
	if err != nil {
		http.Error(w, "port must be a number", http.StatusBadRequest)
		return
	}

	_, err = s.Proxy.Recreate(r.Context(), id, port, strings.TrimSpace(r.PostFormValue("tls_domain")))
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, proxy.ErrPortReserved):
		http.Error(w, fmt.Sprintf("port %d is reserved by the panel", port), http.StatusBadRequest)
		return
	case errors.Is(err, store.ErrPortTaken):
		http.Error(w, fmt.Sprintf("port %d is already used by another proxy", port), http.StatusBadRequest)
		return
	case errors.Is(err, proxy.ErrPortConflict):
		http.Error(w, fmt.Sprintf("port %d is already in use by something else on this host (outside the panel's tracking) — Docker rejected it", port), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/proxies/"+id, http.StatusSeeOther)
}

// getLogs serves the container's raw log tail as plain text.
//
// It is deliberately not wired to any client-side DOM swap on the detail
// page. telemt's logs are attacker-influenceable — a client can put
// arbitrary bytes into a handshake that telemt goes on to log — so they must
// be treated as hostile. The page's embedded "Container logs" card is safe
// because html/template renders {{$.Logs}} in a plain text node, which
// html/template HTML-escapes unconditionally. Wiring this endpoint to an
// htmx/JS "refresh" button that swaps the response into innerHTML would
// reopen exactly that hole: raw log bytes containing e.g. "<script>" would
// be parsed as markup instead of displayed as text. text/plain (plus
// X-Content-Type-Options: nosniff, so a browser can't be tricked into
// sniffing it as HTML) is the one response shape here a browser will never
// interpret as anything but inert text, so the link in the template opens it
// directly rather than fetching and injecting it.
func (s *server) getLogs(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	id := r.PathValue("id")
	if _, err := s.Proxy.Get(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logs, err := s.Proxy.Logs(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(logs))
}

// optionalGB returns nil for an empty field, so submitting a blank box
// clears the limit rather than leaving it untouched.
func optionalGB(v string) (*uint64, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	gb, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return nil, err
	}
	b := gb * 1024 * 1024 * 1024
	return &b, nil
}

func optionalInt(v string) (*int, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func optionalDate(v string) (*string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, err
	}
	s := t.UTC().Format(time.RFC3339)
	return &s, nil
}
