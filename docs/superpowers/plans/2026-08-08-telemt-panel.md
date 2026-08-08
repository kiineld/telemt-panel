# telemt-panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A self-hosted web panel that creates and monitors [telemt](https://github.com/telemt/telemt) MTProto proxies — one telemt container per proxy — installable with `git clone && docker compose up -d`.

**Architecture:** A Go binary holds `/var/run/docker.sock` and creates one telemt container per proxy, each with its own `config.toml` (own port, own fake-TLS domain, own ad-tag, one user). Each telemt's Control API listens on an internal Docker network only, guarded by a per-proxy bearer token. The panel polls those APIs for live unique-IP counts and pushes them to the browser over SSE. Caddy terminates HTTPS on `:8443` so `:443` stays available for a proxy.

**Tech Stack:** Go 1.23, `modernc.org/sqlite` (pure Go, no cgo), Docker Engine SDK (`github.com/docker/docker/client`), `golang.org/x/crypto/argon2`, `github.com/skip2/go-qrcode`, `html/template` + HTMX 2.x + Alpine 3.x (both vendored into `web/static`, never CDN), Caddy 2.

**Spec:** `docs/superpowers/specs/2026-08-08-telemt-panel-design.md`

## Global Constraints

Every task's requirements implicitly include this section.

- Go module path: `github.com/kiineld/telemt-panel`. Go version floor: `1.23`.
- **No cgo.** All builds use `CGO_ENABLED=0`. This is why SQLite is `modernc.org/sqlite`, not `mattn/go-sqlite3`.
- **No CDN assets.** HTMX and Alpine are committed under `web/static/vendor/`. The panel must work on a host with no outbound internet except to Docker Hub/ghcr.
- telemt image: `ghcr.io/telemt/telemt:latest`, overridable via env `TELEMT_IMAGE`.
- Docker network name `mtpanel_net`, subnet `172.28.0.0/16`. This subnet is the value written into each proxy's `[server.api] whitelist`.
- Reserved host ports the panel must refuse for proxies: **80** and **8443** (Caddy owns both).
- Every telemt container is labelled `mtpanel.managed=true` and `mtpanel.proxy_id=<uuid>`. Container name is `telemt-<uuid>`.
- Per-proxy config lives at `<PANEL_DATA_DIR>/proxies/<uuid>/config.toml`, mounted at `/etc/telemt` as a **directory** (never a single-file bind — telemt rewrites it via temp+rename).
- telemt user name inside every proxy is the literal string `user`. One user per proxy, always.
- Secrets are 32 lowercase hex chars. Ad-tags, when present, are exactly 32 hex chars.
- All time stamps stored and compared in UTC. Expirations are RFC3339.
- `go test ./...` must pass on a machine with no Docker daemon. Docker-dependent tests sit behind the build tag `docker`.
- Commit after every task. Conventional commit prefixes (`feat:`, `test:`, `fix:`, `chore:`).

## File Structure

| Path | Responsibility |
| --- | --- |
| `cmd/panel/main.go` | Wiring only: load config, open store, build runtime, start poller, serve HTTP |
| `internal/config/config.go` | Environment → `Config` struct, with defaults |
| `internal/telemt/link/link.go` | Build `tg://` and `https://t.me` fake-TLS links |
| `internal/telemt/tconfig/render.go` | Render a proxy's `config.toml` from a `Spec` |
| `internal/telemt/tconfig/config.toml.tmpl` | The template itself |
| `internal/telemt/client/client.go` | Typed Control API client + error envelope |
| `internal/store/store.go` | SQLite open + migrate |
| `internal/store/schema.sql` | DDL |
| `internal/store/proxies.go` | Proxy CRUD |
| `internal/store/admins.go` | Admin + session CRUD |
| `internal/docker/runtime.go` | `Runtime` interface + shared types |
| `internal/docker/client.go` | Real Docker SDK implementation |
| `internal/docker/fake.go` | In-memory fake for tests |
| `internal/proxy/ports.go` | Port reservation + liveness check |
| `internal/proxy/service.go` | Create/Delete/Update/Recreate sagas, Reconcile |
| `internal/poller/poller.go` | Stats poll loop + snapshot cache + subscriber fan-out |
| `internal/web/server.go` | Router, middleware, template loading |
| `internal/web/auth.go` | argon2id, sessions, login rate limit |
| `internal/web/handlers_proxy.go` | Proxy list/create/detail/delete handlers |
| `internal/web/sse.go` | SSE endpoint |
| `web/templates/*.html` | Server-rendered pages |
| `web/static/` | CSS + vendored HTMX/Alpine |
| `Dockerfile`, `docker-compose.yml`, `Caddyfile`, `.env.example` | One-click install |

---

### Task 1: Scaffold and one-click install skeleton

Deliverable: `docker compose up -d` on a clean host serves `https://<ip>:8443/healthz` returning `ok`.

**Files:**
- Create: `go.mod`, `cmd/panel/main.go`, `internal/config/config.go`, `internal/config/config_test.go`, `Dockerfile`, `docker-compose.yml`, `Caddyfile`, `.env.example`, `README.md`

**Interfaces:**
- Consumes: nothing.
- Produces: `config.Config` struct with fields `ListenAddr string`, `DataDir string`, `Network string`, `NetworkSubnet string`, `TelemtImage string`, `PublicHost string`, `PollInterval time.Duration`, `ReservedPorts []int`; and `config.Load() (Config, error)`.

- [ ] **Step 1: Initialise the module**

```bash
cd /Users/kiineld/WebstormProjects/telemt-panel
go mod init github.com/kiineld/telemt-panel
go mod edit -go=1.23
```

- [ ] **Step 2: Write the failing config test**

Create `internal/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("PANEL_DATA_DIR", "/data")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.ListenAddr != ":8080" {
		t.Errorf("ListenAddr = %q, want %q", c.ListenAddr, ":8080")
	}
	if c.Network != "mtpanel_net" {
		t.Errorf("Network = %q, want %q", c.Network, "mtpanel_net")
	}
	if c.NetworkSubnet != "172.28.0.0/16" {
		t.Errorf("NetworkSubnet = %q, want %q", c.NetworkSubnet, "172.28.0.0/16")
	}
	if c.TelemtImage != "ghcr.io/telemt/telemt:latest" {
		t.Errorf("TelemtImage = %q", c.TelemtImage)
	}
	if c.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", c.PollInterval)
	}
	if got, want := len(c.ReservedPorts), 2; got != want {
		t.Fatalf("len(ReservedPorts) = %d, want %d", got, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("PANEL_DATA_DIR", "/tmp/x")
	t.Setenv("PANEL_LISTEN", ":9000")
	t.Setenv("PANEL_POLL_INTERVAL", "12s")
	t.Setenv("TELEMT_IMAGE", "ghcr.io/telemt/telemt:v1")
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.ListenAddr != ":9000" {
		t.Errorf("ListenAddr = %q", c.ListenAddr)
	}
	if c.PollInterval != 12*time.Second {
		t.Errorf("PollInterval = %v", c.PollInterval)
	}
	if c.TelemtImage != "ghcr.io/telemt/telemt:v1" {
		t.Errorf("TelemtImage = %q", c.TelemtImage)
	}
}

func TestLoadRejectsBadInterval(t *testing.T) {
	t.Setenv("PANEL_DATA_DIR", "/data")
	t.Setenv("PANEL_POLL_INTERVAL", "banana")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for unparseable interval")
	}
}
```

- [ ] **Step 3: Run it and confirm it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — package `config` has no `Load`, build error.

- [ ] **Step 4: Implement the config loader**

Create `internal/config/config.go`:

```go
// Package config loads panel settings from the environment.
package config

import (
	"fmt"
	"os"
	"time"
)

// Config holds every runtime setting the panel needs. All fields have
// working defaults so the panel boots with an empty environment.
type Config struct {
	ListenAddr    string
	DataDir       string
	Network       string
	NetworkSubnet string
	TelemtImage   string
	// PublicHost overrides the host used in generated tg:// links. Empty
	// means telemt's own external-IP detection supplies it.
	PublicHost    string
	PollInterval  time.Duration
	// ReservedPorts may not be assigned to a proxy; Caddy owns them.
	ReservedPorts []int
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:    env("PANEL_LISTEN", ":8080"),
		DataDir:       env("PANEL_DATA_DIR", "/data"),
		Network:       env("MTPANEL_NETWORK", "mtpanel_net"),
		NetworkSubnet: env("MTPANEL_SUBNET", "172.28.0.0/16"),
		TelemtImage:   env("TELEMT_IMAGE", "ghcr.io/telemt/telemt:latest"),
		PublicHost:    env("PANEL_PUBLIC_HOST", ""),
		ReservedPorts: []int{80, 8443},
	}

	d, err := time.ParseDuration(env("PANEL_POLL_INTERVAL", "5s"))
	if err != nil {
		return Config{}, fmt.Errorf("PANEL_POLL_INTERVAL: %w", err)
	}
	if d < time.Second {
		return Config{}, fmt.Errorf("PANEL_POLL_INTERVAL: %v is below the 1s minimum", d)
	}
	c.PollInterval = d

	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
```

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS — all three tests.

- [ ] **Step 6: Write the minimal main**

Create `cmd/panel/main.go`:

```go
package main

import (
	"log"
	"net/http"

	"github.com/kiineld/telemt-panel/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	log.Printf("panel listening on %s", cfg.ListenAddr)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
```

- [ ] **Step 7: Write the Dockerfile**

Create `Dockerfile`:

```dockerfile
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/panel ./cmd/panel

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/panel /usr/local/bin/panel
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/panel"]
```

- [ ] **Step 8: Write the compose file and Caddyfile**

Create `docker-compose.yml`:

```yaml
services:
  caddy:
    image: caddy:2-alpine
    container_name: mtpanel-caddy
    restart: unless-stopped
    ports:
      - "80:80"
      - "8443:8443"
    environment:
      - PANEL_DOMAIN=${PANEL_DOMAIN:-}
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - ./data/caddy/data:/data
      - ./data/caddy/config:/config
    networks:
      - mtpanel_net
    depends_on:
      - panel

  panel:
    build: .
    image: telemt-panel:local
    container_name: mtpanel
    restart: unless-stopped
    environment:
      - PANEL_LISTEN=:8080
      - PANEL_DATA_DIR=/data
      - MTPANEL_NETWORK=mtpanel_net
      - MTPANEL_SUBNET=172.28.0.0/16
      - TELEMT_IMAGE=${TELEMT_IMAGE:-ghcr.io/telemt/telemt:latest}
      - PANEL_PUBLIC_HOST=${PANEL_PUBLIC_HOST:-}
      - PANEL_HOST_DATA_DIR=${PWD}/data
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
    networks:
      - mtpanel_net

networks:
  mtpanel_net:
    name: mtpanel_net
    ipam:
      config:
        - subnet: 172.28.0.0/16
```

Create `Caddyfile`:

```caddyfile
{
	# ACME HTTP-01 runs on :80. The panel itself is served on :8443 so that
	# host port 443 stays free for an MTProto proxy.
	http_port 80
	https_port 8443
}

{$PANEL_DOMAIN::8443} {
	# With PANEL_DOMAIN unset this matches any host on :8443 and Caddy issues
	# an internal self-signed certificate. Setting PANEL_DOMAIN switches it to
	# a real Let's Encrypt certificate with no other change.
	@nodomain expression {$PANEL_DOMAIN:""} == ""
	tls internal {
		on_demand
	}
	reverse_proxy panel:8080
}
```

Create `.env.example`:

```bash
# Optional. Set to a domain pointing at this server to get a real
# Let's Encrypt certificate. Leave unset for a self-signed cert on
# https://<server-ip>:8443 — the panel works either way.
PANEL_DOMAIN=

# Optional. Host or IP embedded in generated tg:// links. Leave unset to
# let telemt detect the server's external address itself.
PANEL_PUBLIC_HOST=

# Optional. Pin a telemt version instead of tracking latest.
TELEMT_IMAGE=ghcr.io/telemt/telemt:latest
```

- [ ] **Step 9: Verify the Caddyfile parses and the stack builds**

Run:

```bash
docker run --rm -v "$PWD/Caddyfile:/etc/caddy/Caddyfile:ro" caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile
```

Expected: `Valid configuration`. If Caddy rejects the `@nodomain`/`tls internal` combination, replace the site block body with a plain `tls internal` and drop the matcher — the `on_demand` form is the only part that may need adjusting for the installed Caddy version, and the requirement is only that an unset `PANEL_DOMAIN` yields a working self-signed cert.

Then:

```bash
docker compose build panel
docker compose up -d
sleep 5
curl -sk https://localhost:8443/healthz
```

Expected: `ok`.

- [ ] **Step 10: Write the README**

Create `README.md`:

```markdown
# telemt-panel

Web panel for [telemt](https://github.com/telemt/telemt) MTProto proxies.
Each proxy is its own telemt container with its own port, fake-TLS domain and
ad-tag, and reports its live unique-IP count back to the panel.

## Install

```bash
git clone https://github.com/kiineld/telemt-panel
cd telemt-panel
docker compose up -d
```

Open `https://<server-ip>:8443`. The first-boot admin password is printed by:

```bash
docker compose logs panel | grep "admin password"
```

For a real certificate instead of the self-signed one, put a domain in `.env`
as `PANEL_DOMAIN=panel.example.com` (A record pointing here) and re-run
`docker compose up -d`.

## Why port 8443?

Caddy serves the panel on `:8443` and uses `:80` only for the ACME challenge.
That leaves host port `443` free to assign to a proxy, where fake-TLS traffic
blends in with ordinary HTTPS.

## Security

The panel mounts the Docker socket, so **it is root-equivalent on this host**.
Anything able to create a container can mount the host filesystem. The panel
publishes no host port of its own (Caddy is the only ingress), hashes passwords
with argon2id, and rate-limits logins — but do not expose it to the internet
without a firewall you trust.
```

- [ ] **Step 11: Commit**

```bash
git add go.mod cmd internal Dockerfile docker-compose.yml Caddyfile .env.example README.md
git commit -m "feat: scaffold panel with one-click compose install"
```

---

### Task 2: Fake-TLS link builder

Deliverable: given host, port, secret and domain, produce the exact `tg://` link Telegram accepts. Built test-first because a malformed link looks fine in the UI and fails only on the user's phone.

**Files:**
- Create: `internal/telemt/link/link.go`, `internal/telemt/link/link_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `link.FakeTLS(host string, port int, secretHex, domain string) (string, error)` returning a `tg://proxy?...` URL, and `link.FakeTLSHTTPS(...)` returning the `https://t.me/proxy?...` equivalent. Both validate that `secretHex` is exactly 32 hex chars and `domain` is non-empty.

- [ ] **Step 1: Write the failing tests**

Create `internal/telemt/link/link_test.go`:

```go
package link

import "testing"

// hex("petrovich.ru") == 706574726f766963682e7275
const petrovichHex = "706574726f766963682e7275"

func TestFakeTLS(t *testing.T) {
	secret := "00112233445566778899aabbccddeeff"
	got, err := FakeTLS("1.2.3.4", 443, secret, "petrovich.ru")
	if err != nil {
		t.Fatalf("FakeTLS() error = %v", err)
	}
	want := "tg://proxy?server=1.2.3.4&port=443&secret=ee" + secret + petrovichHex
	if got != want {
		t.Errorf("FakeTLS() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestFakeTLSHTTPS(t *testing.T) {
	secret := "00112233445566778899aabbccddeeff"
	got, err := FakeTLSHTTPS("1.2.3.4", 443, secret, "petrovich.ru")
	if err != nil {
		t.Fatalf("FakeTLSHTTPS() error = %v", err)
	}
	want := "https://t.me/proxy?server=1.2.3.4&port=443&secret=ee" + secret + petrovichHex
	if got != want {
		t.Errorf("FakeTLSHTTPS() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestFakeTLSLowercasesSecret(t *testing.T) {
	got, err := FakeTLS("h", 443, "00112233445566778899AABBCCDDEEFF", "a.com")
	if err != nil {
		t.Fatalf("FakeTLS() error = %v", err)
	}
	want := "tg://proxy?server=h&port=443&secret=ee00112233445566778899aabbccddeeff612e636f6d"
	if got != want {
		t.Errorf("FakeTLS() = %q, want %q", got, want)
	}
}

func TestFakeTLSRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		port   int
		secret string
		domain string
	}{
		{"short secret", "h", 443, "00112233", "a.com"},
		{"non-hex secret", "h", 443, "zz112233445566778899aabbccddeeff", "a.com"},
		{"empty domain", "h", 443, "00112233445566778899aabbccddeeff", ""},
		{"empty host", "", 443, "00112233445566778899aabbccddeeff", "a.com"},
		{"port zero", "h", 0, "00112233445566778899aabbccddeeff", "a.com"},
		{"port too high", "h", 70000, "00112233445566778899aabbccddeeff", "a.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := FakeTLS(tc.host, tc.port, tc.secret, tc.domain); err == nil {
				t.Fatal("FakeTLS() error = nil, want error")
			}
		})
	}
}

func TestFakeTLSEscapesHost(t *testing.T) {
	// IPv6 literals contain colons, which must survive as query-safe text.
	got, err := FakeTLS("2001:db8::1", 443, "00112233445566778899aabbccddeeff", "a.com")
	if err != nil {
		t.Fatalf("FakeTLS() error = %v", err)
	}
	want := "tg://proxy?server=2001%3Adb8%3A%3A1&port=443&secret=ee00112233445566778899aabbccddeeff612e636f6d"
	if got != want {
		t.Errorf("FakeTLS() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/telemt/link/ -v`
Expected: FAIL — undefined `FakeTLS`, `FakeTLSHTTPS`.

- [ ] **Step 3: Implement the builder**

Create `internal/telemt/link/link.go`:

```go
// Package link builds Telegram proxy links for telemt's fake-TLS ("ee") mode.
//
// A fake-TLS secret is the literal prefix "ee", followed by the 32-hex user
// secret, followed by the hex encoding of the SNI domain's raw bytes.
package link

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// FakeTLS returns a tg://proxy link for the given proxy parameters.
func FakeTLS(host string, port int, secretHex, domain string) (string, error) {
	return build("tg://proxy", host, port, secretHex, domain)
}

// FakeTLSHTTPS returns the https://t.me/proxy form of the same link, which is
// what you paste into a chat so it renders as a tappable button.
func FakeTLSHTTPS(host string, port int, secretHex, domain string) (string, error) {
	return build("https://t.me/proxy", host, port, secretHex, domain)
}

func build(base, host string, port int, secretHex, domain string) (string, error) {
	secret, err := normalizeSecret(secretHex)
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("link: host is empty")
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("link: port %d out of range 1-65535", port)
	}
	if domain == "" {
		return "", fmt.Errorf("link: domain is empty")
	}

	q := url.Values{}
	q.Set("server", host)
	q.Set("port", strconv.Itoa(port))
	q.Set("secret", "ee"+secret+hex.EncodeToString([]byte(domain)))

	// url.Values.Encode sorts keys alphabetically; we want server, port,
	// secret in that order because that is the order every other MTProto
	// tool emits and it makes links diffable against telemt's own output.
	return base + "?server=" + url.QueryEscape(host) +
		"&port=" + strconv.Itoa(port) +
		"&secret=" + q.Get("secret"), nil
}

// normalizeSecret validates a 32-character hex secret and lowercases it.
func normalizeSecret(s string) (string, error) {
	if len(s) != 32 {
		return "", fmt.Errorf("link: secret must be 32 hex chars, got %d", len(s))
	}
	s = strings.ToLower(s)
	if _, err := hex.DecodeString(s); err != nil {
		return "", fmt.Errorf("link: secret is not hex: %w", err)
	}
	return s, nil
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/telemt/link/ -v`
Expected: PASS — all five test functions.

- [ ] **Step 5: Commit**

```bash
git add internal/telemt/link
git commit -m "feat: add fake-TLS proxy link builder"
```

---

### Task 3: telemt config.toml renderer

Deliverable: a `Spec` renders to a valid `config.toml` that telemt accepts, with the proxy's single user, its ad-tag and its limits already present at boot.

**Files:**
- Create: `internal/telemt/tconfig/render.go`, `internal/telemt/tconfig/config.toml.tmpl`, `internal/telemt/tconfig/render_test.go`, `internal/telemt/tconfig/testdata/full.golden.toml`, `internal/telemt/tconfig/testdata/minimal.golden.toml`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type Spec struct {
	Username          string   // always "user"
	Secret            string   // 32 hex
	Port              int
	TLSDomain         string
	AdTag             string   // "" means no ad-tag
	APIToken          string   // bearer value for [server.api] auth_header
	APIWhitelist      []string // CIDRs, e.g. ["172.28.0.0/16"]
	PublicHost        string   // "" means let telemt autodetect
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
}

func Render(s Spec) (string, error)
```

`Render` returns an error for an invalid spec (bad secret length, port out of range, empty domain, ad-tag not 32 hex).

- [ ] **Step 1: Write the failing tests**

Create `internal/telemt/tconfig/render_test.go`:

```go
package tconfig

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func minimalSpec() Spec {
	return Spec{
		Username:     "user",
		Secret:       "00112233445566778899aabbccddeeff",
		Port:         443,
		TLSDomain:    "petrovich.ru",
		APIToken:     "tok_abc",
		APIWhitelist: []string{"172.28.0.0/16"},
	}
}

func fullSpec() Spec {
	s := minimalSpec()
	s.AdTag = "ffeeddccbbaa99887766554433221100"
	s.PublicHost = "proxy.example.com"
	quota := uint64(107374182400)
	exp := "2027-01-01T00:00:00Z"
	conns := 200
	ips := 8
	s.DataQuotaBytes = &quota
	s.ExpirationRFC3339 = &exp
	s.MaxTCPConns = &conns
	s.MaxUniqueIPs = &ips
	return s
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update to create)", path, err)
	}
	if got != string(want) {
		t.Errorf("rendered config does not match %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRenderMinimal(t *testing.T) {
	got, err := Render(minimalSpec())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertGolden(t, "minimal.golden.toml", got)
}

func TestRenderFull(t *testing.T) {
	got, err := Render(fullSpec())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	assertGolden(t, "full.golden.toml", got)
}

func TestRenderOmitsEmptyOptionals(t *testing.T) {
	got, err := Render(minimalSpec())
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, absent := range []string{
		"user_ad_tags", "user_data_quota", "user_expirations",
		"user_max_tcp_conns", "user_max_unique_ips", "public_host",
	} {
		if contains(got, absent) {
			t.Errorf("minimal config unexpectedly contains %q:\n%s", absent, got)
		}
	}
}

func TestRenderValidation(t *testing.T) {
	cases := map[string]func(*Spec){
		"short secret":  func(s *Spec) { s.Secret = "abc" },
		"non-hex secret": func(s *Spec) { s.Secret = "zz112233445566778899aabbccddeeff" },
		"port zero":     func(s *Spec) { s.Port = 0 },
		"port high":     func(s *Spec) { s.Port = 70000 },
		"empty domain":  func(s *Spec) { s.TLSDomain = "" },
		"empty user":    func(s *Spec) { s.Username = "" },
		"bad ad tag":    func(s *Spec) { s.AdTag = "nope" },
		"empty token":   func(s *Spec) { s.APIToken = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := minimalSpec()
			mutate(&s)
			if _, err := Render(s); err == nil {
				t.Fatal("Render() error = nil, want error")
			}
		})
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/telemt/tconfig/ -v`
Expected: FAIL — undefined `Spec`, `Render`.

- [ ] **Step 3: Write the template**

Create `internal/telemt/tconfig/config.toml.tmpl`:

```gotemplate
# Generated by telemt-panel. Edits here are overwritten when the proxy is
# recreated; change settings through the panel instead.

[general]
use_middle_proxy = true
log_level = "normal"

[general.modes]
classic = false
secure = false
tls = true

[general.links]
show = "*"
{{- if .PublicHost }}
public_host = "{{ .PublicHost }}"
{{- end }}
public_port = {{ .Port }}

[server]
port = {{ .Port }}

[server.api]
enabled = true
listen = "0.0.0.0:9091"
whitelist = [{{ range $i, $c := .APIWhitelist }}{{ if $i }}, {{ end }}"{{ $c }}"{{ end }}]
auth_header = "{{ .APIToken }}"
minimal_runtime_enabled = true
minimal_runtime_cache_ttl_ms = 1000

[[server.listeners]]
ip = "0.0.0.0"
port = {{ .Port }}

[censorship]
tls_domain = "{{ .TLSDomain }}"
mask = true
tls_emulation = true
tls_front_dir = "tlsfront"
unknown_sni_action = "reject_handshake"

[access.users]
{{ .Username }} = "{{ .Secret }}"
{{- if .AdTag }}

[access.user_ad_tags]
{{ .Username }} = "{{ .AdTag }}"
{{- end }}
{{- if .MaxTCPConns }}

[access.user_max_tcp_conns]
{{ .Username }} = {{ .MaxTCPConns }}
{{- end }}
{{- if .ExpirationRFC3339 }}

[access.user_expirations]
{{ .Username }} = "{{ .ExpirationRFC3339 }}"
{{- end }}
{{- if .DataQuotaBytes }}

[access.user_data_quota]
{{ .Username }} = {{ .DataQuotaBytes }}
{{- end }}
{{- if .MaxUniqueIPs }}

[access.user_max_unique_ips]
{{ .Username }} = {{ .MaxUniqueIPs }}
{{- end }}
```

Note: `{{ .MaxTCPConns }}` on a `*int` renders the pointed-to value in Go templates, so no dereference helper is needed.

- [ ] **Step 4: Implement Render**

Create `internal/telemt/tconfig/render.go`:

```go
// Package tconfig renders a telemt config.toml for a single panel-managed proxy.
//
// Every proxy container runs exactly one telemt user, named by Spec.Username,
// so all the per-user maps in [access] have exactly one entry.
package tconfig

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
	"text/template"
)

//go:embed config.toml.tmpl
var tmplSrc string

var tmpl = template.Must(template.New("config.toml").Parse(tmplSrc))

// Spec is everything that varies between one proxy's config and another's.
type Spec struct {
	Username          string
	Secret            string
	Port              int
	TLSDomain         string
	AdTag             string
	APIToken          string
	APIWhitelist      []string
	PublicHost        string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
}

// Render produces the complete config.toml text for one proxy.
func Render(s Spec) (string, error) {
	if err := s.validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, s); err != nil {
		return "", fmt.Errorf("tconfig: render: %w", err)
	}
	out := b.String()
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out, nil
}

func (s Spec) validate() error {
	if s.Username == "" {
		return fmt.Errorf("tconfig: username is empty")
	}
	if err := hex32("secret", s.Secret); err != nil {
		return err
	}
	if s.AdTag != "" {
		if err := hex32("ad_tag", s.AdTag); err != nil {
			return err
		}
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("tconfig: port %d out of range 1-65535", s.Port)
	}
	if s.TLSDomain == "" {
		return fmt.Errorf("tconfig: tls_domain is empty")
	}
	if s.APIToken == "" {
		return fmt.Errorf("tconfig: api token is empty")
	}
	if len(s.APIWhitelist) == 0 {
		return fmt.Errorf("tconfig: api whitelist is empty, which would expose the control API")
	}
	// Guard against a quoted string breaking out of the TOML value.
	for _, v := range []string{s.TLSDomain, s.APIToken, s.PublicHost, s.Username} {
		if strings.ContainsAny(v, "\"\n\\") {
			return fmt.Errorf("tconfig: value %q contains a quote, backslash or newline", v)
		}
	}
	return nil
}

func hex32(field, v string) error {
	if len(v) != 32 {
		return fmt.Errorf("tconfig: %s must be 32 hex chars, got %d", field, len(v))
	}
	if _, err := hex.DecodeString(v); err != nil {
		return fmt.Errorf("tconfig: %s is not hex: %w", field, err)
	}
	return nil
}
```

- [ ] **Step 5: Generate the golden files and inspect them**

Run:

```bash
mkdir -p internal/telemt/tconfig/testdata
go test ./internal/telemt/tconfig/ -update
cat internal/telemt/tconfig/testdata/full.golden.toml
```

Expected: a complete TOML file. **Read it** and confirm `[access.users]` has `user = "0011..."`, `[access.user_ad_tags]` is present, `[server.api] whitelist = ["172.28.0.0/16"]`, and `auth_header = "tok_abc"`. If anything is off, fix the template and regenerate — the golden file is only meaningful once you have eyeballed it.

- [ ] **Step 6: Verify telemt itself accepts the rendered config**

Run:

```bash
docker run --rm -v "$PWD/internal/telemt/tconfig/testdata:/etc/telemt:ro" \
  --entrypoint /app/telemt ghcr.io/telemt/telemt:latest \
  healthcheck /etc/telemt/full.golden.toml --mode liveness || true
```

Expected: telemt parses the file without a config error. A liveness failure is fine (nothing is running); a *parse* or *validation* error is not — if you see one, fix the template. If the image's entrypoint path differs, run `docker run --rm --entrypoint sh ghcr.io/telemt/telemt:latest -c 'ls /app'` to locate the binary.

- [ ] **Step 7: Run the tests and confirm they pass**

Run: `go test ./internal/telemt/tconfig/ -v`
Expected: PASS — all four test functions.

- [ ] **Step 8: Commit**

```bash
git add internal/telemt/tconfig
git commit -m "feat: render per-proxy telemt config.toml"
```

---

### Task 4: telemt Control API client

Deliverable: a typed client for the three endpoints the panel actually calls, decoding telemt's success and error envelopes.

**Files:**
- Create: `internal/telemt/client/client.go`, `internal/telemt/client/client_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
func New(baseURL, token string) *Client
func (c *Client) Health(ctx context.Context) error
func (c *Client) Users(ctx context.Context) ([]UserInfo, error)
func (c *Client) PatchUser(ctx context.Context, username string, p PatchUser) (UserInfo, error)

type UserInfo struct {
	Username            string
	Enabled             bool
	UserAdTag           *string
	CurrentConnections  uint64
	ActiveUniqueIPs     int
	ActiveUniqueIPsList []string
	TotalOctets         uint64
	DataQuotaBytes      *uint64
	ExpirationRFC3339   *string
	MaxTCPConns         *int
	MaxUniqueIPs        *int
	Links               UserLinks
}

type UserLinks struct {
	Classic    []string
	Secure     []string
	TLS        []string
	TLSDomains []TLSDomainLink
}

type TLSDomainLink struct{ Domain, Link string }

// PatchUser uses JSON Merge Patch: a nil field means "leave unchanged",
// a non-nil field whose pointed-to value is nil means "remove the override".
type PatchUser struct {
	UserAdTag         *string  `json:"user_ad_tag,omitempty"`
	DataQuotaBytes    *uint64  `json:"data_quota_bytes,omitempty"`
	ExpirationRFC3339 *string  `json:"expiration_rfc3339,omitempty"`
	MaxTCPConns       *int     `json:"max_tcp_conns,omitempty"`
	MaxUniqueIPs      *int     `json:"max_unique_ips,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"`
}

type APIError struct {
	Status  int
	Code    string
	Message string
}
func (e *APIError) Error() string
```

- [ ] **Step 1: Write the failing tests**

Create `internal/telemt/client/client_test.go`:

```go
package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func TestHealthOK(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("path = %q, want /v1/health", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "tok" {
			t.Errorf("Authorization = %q, want %q", got, "tok")
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"status":"ok","read_only":false}}`))
	})
	if err := New(s.URL, "tok").Health(context.Background()); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

func TestHealthUnauthorized(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error":{"code":"unauthorized","message":"bad token"}}`))
	})
	err := New(s.URL, "wrong").Health(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("Health() error = %v, want *APIError", err)
	}
	if apiErr.Code != "unauthorized" {
		t.Errorf("Code = %q, want %q", apiErr.Code, "unauthorized")
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", apiErr.Status)
	}
}

func TestUsers(t *testing.T) {
	body := `{"ok":true,"revision":"abc","data":[{
		"username":"user","enabled":true,"in_runtime":true,
		"user_ad_tag":"ffeeddccbbaa99887766554433221100",
		"current_connections":7,"active_unique_ips":3,
		"active_unique_ips_list":["1.2.3.4","5.6.7.8","::1"],
		"total_octets":123456,"data_quota_bytes":1000000,
		"max_tcp_conns":200,
		"links":{"classic":[],"secure":[],
			"tls":["tg://proxy?server=1.2.3.4&port=443&secret=eeaa"],
			"tls_domains":[{"domain":"petrovich.ru","link":"tg://proxy?server=1.2.3.4&port=443&secret=eebb"}]}
	}]}`
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users" {
			t.Errorf("path = %q, want /v1/users", r.URL.Path)
		}
		_, _ = w.Write([]byte(body))
	})

	users, err := New(s.URL, "tok").Users(context.Background())
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("len(users) = %d, want 1", len(users))
	}
	u := users[0]
	if u.Username != "user" {
		t.Errorf("Username = %q", u.Username)
	}
	if u.ActiveUniqueIPs != 3 {
		t.Errorf("ActiveUniqueIPs = %d, want 3", u.ActiveUniqueIPs)
	}
	if len(u.ActiveUniqueIPsList) != 3 {
		t.Errorf("len(ActiveUniqueIPsList) = %d, want 3", len(u.ActiveUniqueIPsList))
	}
	if u.CurrentConnections != 7 {
		t.Errorf("CurrentConnections = %d, want 7", u.CurrentConnections)
	}
	if u.TotalOctets != 123456 {
		t.Errorf("TotalOctets = %d", u.TotalOctets)
	}
	if u.DataQuotaBytes == nil || *u.DataQuotaBytes != 1000000 {
		t.Errorf("DataQuotaBytes = %v, want 1000000", u.DataQuotaBytes)
	}
	if u.MaxUniqueIPs != nil {
		t.Errorf("MaxUniqueIPs = %v, want nil (absent in payload)", u.MaxUniqueIPs)
	}
	if len(u.Links.TLS) != 1 {
		t.Fatalf("len(Links.TLS) = %d, want 1", len(u.Links.TLS))
	}
	if len(u.Links.TLSDomains) != 1 || u.Links.TLSDomains[0].Domain != "petrovich.ru" {
		t.Errorf("Links.TLSDomains = %+v", u.Links.TLSDomains)
	}
}

func TestPatchUserSendsMergePatch(t *testing.T) {
	var got map[string]any
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", r.Method)
		}
		if r.URL.Path != "/v1/users/user" {
			t.Errorf("path = %q, want /v1/users/user", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"username":"user","enabled":true}}`))
	})

	quota := uint64(500)
	u, err := New(s.URL, "tok").PatchUser(context.Background(), "user", PatchUser{DataQuotaBytes: &quota})
	if err != nil {
		t.Fatalf("PatchUser() error = %v", err)
	}
	if u.Username != "user" {
		t.Errorf("Username = %q", u.Username)
	}
	if v, ok := got["data_quota_bytes"]; !ok || v.(float64) != 500 {
		t.Errorf("body[data_quota_bytes] = %v, want 500", v)
	}
	if _, ok := got["max_tcp_conns"]; ok {
		t.Error("body contains max_tcp_conns; omitted fields must not be sent")
	}
}

func TestPatchUserEscapesUsername(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users/a b" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/v1/users/a b")
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"username":"a b"}}`))
	})
	if _, err := New(s.URL, "tok").PatchUser(context.Background(), "a b", PatchUser{}); err != nil {
		t.Fatalf("PatchUser() error = %v", err)
	}
}

func TestNonJSONBodyIsAnError(t *testing.T) {
	s := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>nginx</html>"))
	})
	err := New(s.URL, "tok").Health(context.Background())
	if err == nil {
		t.Fatal("Health() error = nil, want error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("error = %v, want *APIError with status 502", err)
	}
}
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `go test ./internal/telemt/client/ -v`
Expected: FAIL — undefined `New`, `APIError`, `PatchUser`.

- [ ] **Step 3: Implement the client**

Create `internal/telemt/client/client.go`:

```go
// Package client is a typed client for telemt's Control API (/v1).
//
// It covers only the endpoints the panel calls: health, list users, and patch
// one user. Users are created by writing config.toml before the container
// starts, so there is deliberately no POST /v1/users here.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// New returns a client for a telemt Control API rooted at baseURL, e.g.
// "http://telemt-abc123:9091". token is sent verbatim as Authorization.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
}

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("telemt api: http %d", e.Status)
	}
	return fmt.Sprintf("telemt api: %s (http %d): %s", e.Code, e.Status, e.Message)
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type TLSDomainLink struct {
	Domain string `json:"domain"`
	Link   string `json:"link"`
}

type UserLinks struct {
	Classic    []string        `json:"classic"`
	Secure     []string        `json:"secure"`
	TLS        []string        `json:"tls"`
	TLSDomains []TLSDomainLink `json:"tls_domains"`
}

type UserInfo struct {
	Username            string    `json:"username"`
	Enabled             bool      `json:"enabled"`
	InRuntime           bool      `json:"in_runtime"`
	UserAdTag           *string   `json:"user_ad_tag"`
	CurrentConnections  uint64    `json:"current_connections"`
	ActiveUniqueIPs     int       `json:"active_unique_ips"`
	ActiveUniqueIPsList []string  `json:"active_unique_ips_list"`
	TotalOctets         uint64    `json:"total_octets"`
	DataQuotaBytes      *uint64   `json:"data_quota_bytes"`
	ExpirationRFC3339   *string   `json:"expiration_rfc3339"`
	MaxTCPConns         *int      `json:"max_tcp_conns"`
	MaxUniqueIPs        *int      `json:"max_unique_ips"`
	Links               UserLinks `json:"links"`
}

// PatchUser follows JSON Merge Patch semantics: omitted fields are unchanged.
type PatchUser struct {
	UserAdTag         *string `json:"user_ad_tag,omitempty"`
	DataQuotaBytes    *uint64 `json:"data_quota_bytes,omitempty"`
	ExpirationRFC3339 *string `json:"expiration_rfc3339,omitempty"`
	MaxTCPConns       *int    `json:"max_tcp_conns,omitempty"`
	MaxUniqueIPs      *int    `json:"max_unique_ips,omitempty"`
	Enabled           *bool   `json:"enabled,omitempty"`
}

func (c *Client) Health(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/v1/health", nil)
	return err
}

func (c *Client) Users(ctx context.Context) ([]UserInfo, error) {
	raw, err := c.do(ctx, http.MethodGet, "/v1/users", nil)
	if err != nil {
		return nil, err
	}
	var users []UserInfo
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, fmt.Errorf("telemt api: decode users: %w", err)
	}
	return users, nil
}

func (c *Client) PatchUser(ctx context.Context, username string, p PatchUser) (UserInfo, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return UserInfo{}, fmt.Errorf("telemt api: encode patch: %w", err)
	}
	raw, err := c.do(ctx, http.MethodPatch, "/v1/users/"+url.PathEscape(username), body)
	if err != nil {
		return UserInfo{}, err
	}
	var u UserInfo
	if err := json.Unmarshal(raw, &u); err != nil {
		return UserInfo{}, fmt.Errorf("telemt api: decode user: %w", err)
	}
	return u, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("telemt api: build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telemt api: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("telemt api: read body: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// Not a telemt envelope at all — a proxy in front, or a crash page.
		return nil, &APIError{Status: resp.StatusCode, Code: "invalid_response",
			Message: "response was not a telemt JSON envelope"}
	}
	if !env.OK || resp.StatusCode >= 400 {
		e := &APIError{Status: resp.StatusCode}
		if env.Error != nil {
			e.Code, e.Message = env.Error.Code, env.Error.Message
		}
		return nil, e
	}
	return env.Data, nil
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/telemt/client/ -v`
Expected: PASS — all six test functions.

- [ ] **Step 5: Commit**

```bash
git add internal/telemt/client
git commit -m "feat: add telemt control API client"
```

---

### Task 5: SQLite store

Deliverable: proxies, admins and sessions persist across restarts. Port uniqueness is enforced by the database, not by application logic that can race.

**Files:**
- Create: `internal/store/schema.sql`, `internal/store/store.go`, `internal/store/proxies.go`, `internal/store/admins.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type State string
const (
	StateCreating   State = "creating"
	StateRunning    State = "running"
	StateStopped    State = "stopped"
	StateError      State = "error"
	StateRecreating State = "recreating"
	StateDeleting   State = "deleting"
)

type Proxy struct {
	ID, Name          string
	Port              int
	TLSDomain, AdTag  string
	Secret, APIToken  string
	ContainerID       string
	State             State
	StateMessage      string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
	CreatedAt, UpdatedAt time.Time
}

func Open(path string) (*Store, error)
func (s *Store) Close() error
func (s *Store) CreateProxy(ctx context.Context, p Proxy) error   // ErrPortTaken on port conflict
func (s *Store) GetProxy(ctx context.Context, id string) (Proxy, error) // ErrNotFound
func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error)
func (s *Store) UpdateProxy(ctx context.Context, p Proxy) error
func (s *Store) DeleteProxy(ctx context.Context, id string) error

type Admin struct { ID int64; Username, PasswordHash string; MustChangePassword bool }
func (s *Store) AdminCount(ctx context.Context) (int, error)
func (s *Store) CreateAdmin(ctx context.Context, username, hash string) (Admin, error)
func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error)
func (s *Store) SetAdminPassword(ctx context.Context, id int64, hash string) error
func (s *Store) CreateSession(ctx context.Context, tokenHash string, adminID int64, expires time.Time) error
func (s *Store) SessionAdmin(ctx context.Context, tokenHash string) (Admin, error)
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error

var ErrNotFound, ErrPortTaken error
```

- [ ] **Step 1: Add the SQLite dependency**

```bash
go get modernc.org/sqlite@latest
go get github.com/google/uuid@latest
```

- [ ] **Step 2: Write the failing tests**

Create `internal/store/store_test.go`:

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleProxy(id string, port int) Proxy {
	quota := uint64(1000)
	return Proxy{
		ID: id, Name: "p" + id, Port: port,
		TLSDomain: "petrovich.ru", AdTag: "",
		Secret:   "00112233445566778899aabbccddeeff",
		APIToken: "tok", State: StateCreating,
		DataQuotaBytes: &quota,
	}
}

func TestCreateAndGetProxy(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	want := sampleProxy("a", 443)
	if err := s.CreateProxy(ctx, want); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	got, err := s.GetProxy(ctx, "a")
	if err != nil {
		t.Fatalf("GetProxy() error = %v", err)
	}
	if got.Port != 443 || got.TLSDomain != "petrovich.ru" || got.State != StateCreating {
		t.Errorf("GetProxy() = %+v", got)
	}
	if got.DataQuotaBytes == nil || *got.DataQuotaBytes != 1000 {
		t.Errorf("DataQuotaBytes = %v, want 1000", got.DataQuotaBytes)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero; Open should stamp it")
	}
}

func TestGetProxyNotFound(t *testing.T) {
	s := newStore(t)
	_, err := s.GetProxy(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProxy() error = %v, want ErrNotFound", err)
	}
}

func TestPortUniqueness(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.CreateProxy(ctx, sampleProxy("a", 443)); err != nil {
		t.Fatalf("first CreateProxy() error = %v", err)
	}
	err := s.CreateProxy(ctx, sampleProxy("b", 443))
	if !errors.Is(err, ErrPortTaken) {
		t.Fatalf("second CreateProxy() error = %v, want ErrPortTaken", err)
	}
}

func TestPortFreedAfterDelete(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	if err := s.CreateProxy(ctx, sampleProxy("a", 443)); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	if err := s.DeleteProxy(ctx, "a"); err != nil {
		t.Fatalf("DeleteProxy() error = %v", err)
	}
	if err := s.CreateProxy(ctx, sampleProxy("b", 443)); err != nil {
		t.Fatalf("re-CreateProxy() on freed port error = %v", err)
	}
}

func TestUpdateProxy(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	p := sampleProxy("a", 443)
	if err := s.CreateProxy(ctx, p); err != nil {
		t.Fatalf("CreateProxy() error = %v", err)
	}
	p.State = StateRunning
	p.ContainerID = "deadbeef"
	p.StateMessage = ""
	p.DataQuotaBytes = nil
	if err := s.UpdateProxy(ctx, p); err != nil {
		t.Fatalf("UpdateProxy() error = %v", err)
	}
	got, err := s.GetProxy(ctx, "a")
	if err != nil {
		t.Fatalf("GetProxy() error = %v", err)
	}
	if got.State != StateRunning || got.ContainerID != "deadbeef" {
		t.Errorf("after update = %+v", got)
	}
	if got.DataQuotaBytes != nil {
		t.Errorf("DataQuotaBytes = %v, want nil after clearing", got.DataQuotaBytes)
	}
	if !got.UpdatedAt.After(got.CreatedAt) && !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Error("UpdatedAt should be stamped on update")
	}
}

func TestListProxiesSortedByPort(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	for _, tc := range []struct {
		id   string
		port int
	}{{"c", 8443 + 1}, {"a", 443}, {"b", 1080}} {
		if err := s.CreateProxy(ctx, sampleProxy(tc.id, tc.port)); err != nil {
			t.Fatalf("CreateProxy(%s) error = %v", tc.id, err)
		}
	}
	got, err := s.ListProxies(ctx)
	if err != nil {
		t.Fatalf("ListProxies() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Port != 443 || got[1].Port != 1080 || got[2].Port != 8444 {
		t.Errorf("ports = %d, %d, %d", got[0].Port, got[1].Port, got[2].Port)
	}
}

func TestAdminLifecycle(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	n, err := s.AdminCount(ctx)
	if err != nil {
		t.Fatalf("AdminCount() error = %v", err)
	}
	if n != 0 {
		t.Fatalf("AdminCount() = %d, want 0", n)
	}

	a, err := s.CreateAdmin(ctx, "admin", "hash1")
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	if !a.MustChangePassword {
		t.Error("new admin should have MustChangePassword = true")
	}

	got, err := s.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}
	if got.PasswordHash != "hash1" {
		t.Errorf("PasswordHash = %q", got.PasswordHash)
	}

	if err := s.SetAdminPassword(ctx, a.ID, "hash2"); err != nil {
		t.Fatalf("SetAdminPassword() error = %v", err)
	}
	got, _ = s.AdminByUsername(ctx, "admin")
	if got.PasswordHash != "hash2" {
		t.Errorf("PasswordHash = %q, want hash2", got.PasswordHash)
	}
	if got.MustChangePassword {
		t.Error("MustChangePassword should clear once the password is set")
	}
}

func TestSessions(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a, err := s.CreateAdmin(ctx, "admin", "h")
	if err != nil {
		t.Fatalf("CreateAdmin() error = %v", err)
	}
	if err := s.CreateSession(ctx, "th", a.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	got, err := s.SessionAdmin(ctx, "th")
	if err != nil {
		t.Fatalf("SessionAdmin() error = %v", err)
	}
	if got.Username != "admin" {
		t.Errorf("Username = %q", got.Username)
	}

	if err := s.DeleteSession(ctx, "th"); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if _, err := s.SessionAdmin(ctx, "th"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SessionAdmin() after delete error = %v, want ErrNotFound", err)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	s, ctx := newStore(t), context.Background()
	a, _ := s.CreateAdmin(ctx, "admin", "h")
	if err := s.CreateSession(ctx, "old", a.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	if _, err := s.SessionAdmin(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SessionAdmin() on expired session error = %v, want ErrNotFound", err)
	}
}
```

- [ ] **Step 3: Run the tests and confirm they fail**

Run: `go test ./internal/store/ -v`
Expected: FAIL — undefined `Open`, `Proxy`, etc.

- [ ] **Step 4: Write the schema**

Create `internal/store/schema.sql`:

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS proxies (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL,
    port                INTEGER NOT NULL UNIQUE,
    tls_domain          TEXT NOT NULL,
    ad_tag              TEXT NOT NULL DEFAULT '',
    secret              TEXT NOT NULL,
    api_token           TEXT NOT NULL,
    container_id        TEXT NOT NULL DEFAULT '',
    state               TEXT NOT NULL,
    state_message       TEXT NOT NULL DEFAULT '',
    data_quota_bytes    INTEGER,
    expiration_rfc3339  TEXT,
    max_tcp_conns       INTEGER,
    max_unique_ips      INTEGER,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS admins (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    username             TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    must_change_password INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    admin_id   INTEGER NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
```

- [ ] **Step 5: Implement Open and the proxy CRUD**

Create `internal/store/store.go`:

```go
// Package store is the panel's SQLite persistence layer.
//
// It holds intent only — proxy definitions, admins, sessions. Live counters
// (connections, unique IPs, traffic) are never stored here; telemt is the
// single source of truth for those.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound  = errors.New("store: not found")
	ErrPortTaken = errors.New("store: port already assigned to another proxy")
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	// SQLite tolerates exactly one writer; serialising here avoids
	// SQLITE_BUSY under concurrent HTTP handlers.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func isUniqueViolation(err error, column string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, column)
}
```

Create `internal/store/proxies.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type State string

const (
	StateCreating   State = "creating"
	StateRunning    State = "running"
	StateStopped    State = "stopped"
	StateError      State = "error"
	StateRecreating State = "recreating"
	StateDeleting   State = "deleting"
)

type Proxy struct {
	ID                string
	Name              string
	Port              int
	TLSDomain         string
	AdTag             string
	Secret            string
	APIToken          string
	ContainerID       string
	State             State
	StateMessage      string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

const proxyColumns = `id, name, port, tls_domain, ad_tag, secret, api_token,
	container_id, state, state_message, data_quota_bytes, expiration_rfc3339,
	max_tcp_conns, max_unique_ips, created_at, updated_at`

func (s *Store) CreateProxy(ctx context.Context, p Proxy) error {
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now

	_, err := s.db.ExecContext(ctx, `INSERT INTO proxies (`+proxyColumns+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Port, p.TLSDomain, p.AdTag, p.Secret, p.APIToken,
		p.ContainerID, string(p.State), p.StateMessage,
		nullU64(p.DataQuotaBytes), nullStr(p.ExpirationRFC3339),
		nullInt(p.MaxTCPConns), nullInt(p.MaxUniqueIPs),
		p.CreatedAt.Format(time.RFC3339Nano), p.UpdatedAt.Format(time.RFC3339Nano))

	if isUniqueViolation(err, "proxies.port") {
		return fmt.Errorf("%w: port %d", ErrPortTaken, p.Port)
	}
	if err != nil {
		return fmt.Errorf("store: create proxy: %w", err)
	}
	return nil
}

func (s *Store) GetProxy(ctx context.Context, id string) (Proxy, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+proxyColumns+` FROM proxies WHERE id = ?`, id)
	p, err := scanProxy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Proxy{}, fmt.Errorf("%w: proxy %s", ErrNotFound, id)
	}
	return p, err
}

func (s *Store) ListProxies(ctx context.Context) ([]Proxy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+proxyColumns+` FROM proxies ORDER BY port`)
	if err != nil {
		return nil, fmt.Errorf("store: list proxies: %w", err)
	}
	defer rows.Close()

	var out []Proxy
	for rows.Next() {
		p, err := scanProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateProxy(ctx context.Context, p Proxy) error {
	p.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE proxies SET
		name=?, port=?, tls_domain=?, ad_tag=?, secret=?, api_token=?,
		container_id=?, state=?, state_message=?, data_quota_bytes=?,
		expiration_rfc3339=?, max_tcp_conns=?, max_unique_ips=?, updated_at=?
		WHERE id=?`,
		p.Name, p.Port, p.TLSDomain, p.AdTag, p.Secret, p.APIToken,
		p.ContainerID, string(p.State), p.StateMessage,
		nullU64(p.DataQuotaBytes), nullStr(p.ExpirationRFC3339),
		nullInt(p.MaxTCPConns), nullInt(p.MaxUniqueIPs),
		p.UpdatedAt.Format(time.RFC3339Nano), p.ID)

	if isUniqueViolation(err, "proxies.port") {
		return fmt.Errorf("%w: port %d", ErrPortTaken, p.Port)
	}
	if err != nil {
		return fmt.Errorf("store: update proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: proxy %s", ErrNotFound, p.ID)
	}
	return nil
}

func (s *Store) DeleteProxy(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM proxies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete proxy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: proxy %s", ErrNotFound, id)
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scanProxy(sc scanner) (Proxy, error) {
	var (
		p                    Proxy
		state                string
		quota, conns, ips    sql.NullInt64
		exp                  sql.NullString
		createdAt, updatedAt string
	)
	err := sc.Scan(&p.ID, &p.Name, &p.Port, &p.TLSDomain, &p.AdTag, &p.Secret,
		&p.APIToken, &p.ContainerID, &state, &p.StateMessage,
		&quota, &exp, &conns, &ips, &createdAt, &updatedAt)
	if err != nil {
		return Proxy{}, err
	}

	p.State = State(state)
	if quota.Valid {
		v := uint64(quota.Int64)
		p.DataQuotaBytes = &v
	}
	if exp.Valid {
		v := exp.String
		p.ExpirationRFC3339 = &v
	}
	if conns.Valid {
		v := int(conns.Int64)
		p.MaxTCPConns = &v
	}
	if ips.Valid {
		v := int(ips.Int64)
		p.MaxUniqueIPs = &v
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return p, nil
}

func nullU64(v *uint64) any {
	if v == nil {
		return nil
	}
	return int64(*v)
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return int64(*v)
}

func nullStr(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}
```

- [ ] **Step 6: Implement admins and sessions**

Create `internal/store/admins.go`:

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type Admin struct {
	ID                 int64
	Username           string
	PasswordHash       string
	MustChangePassword bool
}

func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count admins: %w", err)
	}
	return n, nil
}

func (s *Store) CreateAdmin(ctx context.Context, username, hash string) (Admin, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admins (username, password_hash, must_change_password) VALUES (?,?,1)`,
		username, hash)
	if err != nil {
		return Admin{}, fmt.Errorf("store: create admin: %w", err)
	}
	id, _ := res.LastInsertId()
	return Admin{ID: id, Username: username, PasswordHash: hash, MustChangePassword: true}, nil
}

func (s *Store) AdminByUsername(ctx context.Context, username string) (Admin, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, must_change_password FROM admins WHERE username = ?`,
		username)
	a, err := scanAdmin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, fmt.Errorf("%w: admin %s", ErrNotFound, username)
	}
	return a, err
}

func (s *Store) SetAdminPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admins SET password_hash = ?, must_change_password = 0 WHERE id = ?`, hash, id)
	if err != nil {
		return fmt.Errorf("store: set admin password: %w", err)
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, tokenHash string, adminID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO sessions (token_hash, admin_id, expires_at) VALUES (?,?,?)`,
		tokenHash, adminID, expires.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// SessionAdmin resolves a session token hash to its admin, treating an expired
// session as absent.
func (s *Store) SessionAdmin(ctx context.Context, tokenHash string) (Admin, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT a.id, a.username, a.password_hash, a.must_change_password
		FROM sessions s JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = ? AND s.expires_at > ?`,
		tokenHash, time.Now().UTC().Format(time.RFC3339Nano))
	a, err := scanAdmin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, fmt.Errorf("%w: session", ErrNotFound)
	}
	return a, err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

func scanAdmin(sc scanner) (Admin, error) {
	var (
		a    Admin
		must int
	)
	if err := sc.Scan(&a.ID, &a.Username, &a.PasswordHash, &must); err != nil {
		return Admin{}, err
	}
	a.MustChangePassword = must != 0
	return a, nil
}
```

- [ ] **Step 7: Run the tests and confirm they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS — all nine test functions. If `TestPortUniqueness` fails with a generic error instead of `ErrPortTaken`, print the raw driver error and adjust the string match in `isUniqueViolation` — modernc's message is `constraint failed: UNIQUE constraint failed: proxies.port (2067)`.

- [ ] **Step 8: Commit**

```bash
git add internal/store go.mod go.sum
git commit -m "feat: add SQLite store for proxies, admins and sessions"
```

---

### Task 6: Docker runtime abstraction

Deliverable: a `Runtime` interface with a real Docker implementation and an in-memory fake. The fake is what makes Task 7's saga testable in milliseconds without a daemon.

**Files:**
- Create: `internal/docker/runtime.go`, `internal/docker/client.go`, `internal/docker/fake.go`, `internal/docker/fake_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:

```go
type ContainerSpec struct {
	Name          string
	Image         string
	Labels        map[string]string
	ConfigHostDir string // bind-mounted read-write at /etc/telemt
	Port          int    // published as 0.0.0.0:Port -> Port/tcp
	Network       string
}

type ContainerInfo struct {
	ID, Name  string
	Running   bool
	Labels    map[string]string
	IPAddress string // address on the panel's network
}

type Runtime interface {
	EnsureNetwork(ctx context.Context, name, subnet string) error
	Pull(ctx context.Context, image string) error
	Create(ctx context.Context, spec ContainerSpec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (ContainerInfo, error)
	List(ctx context.Context, labels map[string]string) ([]ContainerInfo, error)
	Logs(ctx context.Context, id string, tail int) (string, error)
}

func NewDockerRuntime() (Runtime, error)
func NewFake() *Fake   // *Fake implements Runtime; exported fields let tests inject failures
var ErrNoSuchContainer error
```

- [ ] **Step 1: Add the Docker SDK**

```bash
go get github.com/docker/docker@v27.3.1+incompatible
go get github.com/docker/go-connections@latest
```

- [ ] **Step 2: Define the interface and shared types**

Create `internal/docker/runtime.go`:

```go
// Package docker wraps container lifecycle behind a Runtime interface so the
// proxy service can be unit-tested without a Docker daemon.
package docker

import (
	"context"
	"errors"
)

var ErrNoSuchContainer = errors.New("docker: no such container")

// ContainerSpec is the subset of container configuration the panel needs.
// telemt's Control API port (9091) is deliberately not published to the host:
// it is reachable only over the panel's private network.
type ContainerSpec struct {
	Name          string
	Image         string
	Labels        map[string]string
	ConfigHostDir string
	Port          int
	Network       string
}

type ContainerInfo struct {
	ID        string
	Name      string
	Running   bool
	Labels    map[string]string
	IPAddress string
}

type Runtime interface {
	EnsureNetwork(ctx context.Context, name, subnet string) error
	Pull(ctx context.Context, image string) error
	Create(ctx context.Context, spec ContainerSpec) (string, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Remove(ctx context.Context, id string) error
	Inspect(ctx context.Context, id string) (ContainerInfo, error)
	List(ctx context.Context, labels map[string]string) ([]ContainerInfo, error)
	Logs(ctx context.Context, id string, tail int) (string, error)
}
```

- [ ] **Step 3: Write the fake and its tests**

Create `internal/docker/fake.go`:

```go
package docker

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory Runtime for tests. Set the Fail* fields to make a
// specific call return an error and exercise rollback paths.
type Fake struct {
	mu         sync.Mutex
	seq        int
	containers map[string]*fakeContainer
	Networks   map[string]string

	FailPull   error
	FailCreate error
	FailStart  error
	FailRemove error

	// Created records every spec passed to Create, in order.
	Created []ContainerSpec
}

type fakeContainer struct {
	info ContainerInfo
	logs string
}

func NewFake() *Fake {
	return &Fake{containers: map[string]*fakeContainer{}, Networks: map[string]string{}}
}

func (f *Fake) EnsureNetwork(_ context.Context, name, subnet string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Networks[name] = subnet
	return nil
}

func (f *Fake) Pull(context.Context, string) error { return f.FailPull }

func (f *Fake) Create(_ context.Context, spec ContainerSpec) (string, error) {
	if f.FailCreate != nil {
		return "", f.FailCreate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	id := fmt.Sprintf("ctr%d", f.seq)
	f.containers[id] = &fakeContainer{info: ContainerInfo{
		ID: id, Name: spec.Name, Labels: spec.Labels,
		IPAddress: fmt.Sprintf("172.28.0.%d", f.seq+1),
	}}
	f.Created = append(f.Created, spec)
	return id, nil
}

func (f *Fake) Start(_ context.Context, id string) error {
	if f.FailStart != nil {
		return f.FailStart
	}
	return f.mutate(id, func(c *fakeContainer) { c.info.Running = true })
}

func (f *Fake) Stop(_ context.Context, id string) error {
	return f.mutate(id, func(c *fakeContainer) { c.info.Running = false })
}

func (f *Fake) Remove(_ context.Context, id string) error {
	if f.FailRemove != nil {
		return f.FailRemove
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	delete(f.containers, id)
	return nil
}

func (f *Fake) Inspect(_ context.Context, id string) (ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return ContainerInfo{}, fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	return c.info, nil
}

func (f *Fake) List(_ context.Context, labels map[string]string) ([]ContainerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ContainerInfo
	for _, c := range f.containers {
		match := true
		for k, v := range labels {
			if c.info.Labels[k] != v {
				match = false
				break
			}
		}
		if match {
			out = append(out, c.info)
		}
	}
	return out, nil
}

func (f *Fake) Logs(_ context.Context, id string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	return c.logs, nil
}

// Count reports how many containers currently exist. Tests use it to assert
// that rollback left nothing behind.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.containers)
}

func (f *Fake) mutate(id string, fn func(*fakeContainer)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	fn(c)
	return nil
}
```

Create `internal/docker/fake_test.go`:

```go
package docker

import (
	"context"
	"errors"
	"testing"
)

func TestFakeImplementsRuntime(t *testing.T) {
	var _ Runtime = NewFake()
}

func TestFakeLifecycle(t *testing.T) {
	f, ctx := NewFake(), context.Background()
	id, err := f.Create(ctx, ContainerSpec{Name: "a", Labels: map[string]string{"mtpanel.managed": "true"}})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := f.Start(ctx, id); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	info, err := f.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !info.Running {
		t.Error("container should be running after Start")
	}
	if info.IPAddress == "" {
		t.Error("fake should assign an IP address")
	}

	got, err := f.List(ctx, map[string]string{"mtpanel.managed": "true"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List() len = %d, want 1", len(got))
	}

	if err := f.Remove(ctx, id); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if f.Count() != 0 {
		t.Errorf("Count() = %d, want 0", f.Count())
	}
	if _, err := f.Inspect(ctx, id); !errors.Is(err, ErrNoSuchContainer) {
		t.Fatalf("Inspect() after remove error = %v, want ErrNoSuchContainer", err)
	}
}

func TestFakeLabelFilterExcludes(t *testing.T) {
	f, ctx := NewFake(), context.Background()
	if _, err := f.Create(ctx, ContainerSpec{Name: "a", Labels: map[string]string{"other": "1"}}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got, err := f.List(ctx, map[string]string{"mtpanel.managed": "true"})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() len = %d, want 0", len(got))
	}
}

func TestFakeInjectedFailures(t *testing.T) {
	f, ctx := NewFake(), context.Background()
	f.FailCreate = errors.New("boom")
	if _, err := f.Create(ctx, ContainerSpec{}); err == nil {
		t.Fatal("Create() error = nil, want injected failure")
	}
}
```

- [ ] **Step 4: Run the fake's tests and confirm they pass**

Run: `go test ./internal/docker/ -v`
Expected: PASS — four test functions.

- [ ] **Step 5: Implement the real Docker runtime**

Create `internal/docker/client.go`:

```go
package docker

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type dockerRuntime struct{ cli *client.Client }

func NewDockerRuntime() (Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker: connect: %w", err)
	}
	return &dockerRuntime{cli: cli}, nil
}

func (d *dockerRuntime) EnsureNetwork(ctx context.Context, name, subnet string) error {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return fmt.Errorf("docker: list networks: %w", err)
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	_, err = d.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		IPAM:   &network.IPAM{Config: []network.IPAMConfig{{Subnet: subnet}}},
	})
	if err != nil {
		return fmt.Errorf("docker: create network %s: %w", name, err)
	}
	return nil
}

func (d *dockerRuntime) Pull(ctx context.Context, ref string) error {
	rc, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	defer rc.Close()
	// Draining the stream is what makes the pull synchronous.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	return nil
}

func (d *dockerRuntime) Create(ctx context.Context, spec ContainerSpec) (string, error) {
	port := nat.Port(strconv.Itoa(spec.Port) + "/tcp")

	resp, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:      spec.Image,
			Cmd:        []string{"/etc/telemt/config.toml"},
			WorkingDir: "/run/telemt",
			Labels:     spec.Labels,
			Env:        []string{"RUST_LOG=info"},
			ExposedPorts: nat.PortSet{port: struct{}{}},
		},
		&container.HostConfig{
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
			PortBindings:  nat.PortMap{port: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: strconv.Itoa(spec.Port)}}},
			Mounts: []mount.Mount{{
				Type: mount.TypeBind, Source: spec.ConfigHostDir, Target: "/etc/telemt",
			}},
			// telemt caches proxy-secret at runtime and otherwise wants a
			// read-only rootfs; this mirrors upstream's compose file.
			Tmpfs:      map[string]string{"/run/telemt": "rw,mode=1777,size=4m"},
			ReadonlyRootfs: true,
			CapDrop:    []string{"ALL"},
			CapAdd:     []string{"NET_BIND_SERVICE"},
			SecurityOpt: []string{"no-new-privileges:true"},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{spec.Network: {}},
		},
		nil, spec.Name)
	if err != nil {
		return "", fmt.Errorf("docker: create %s: %w", spec.Name, err)
	}
	return resp.ID, nil
}

func (d *dockerRuntime) Start(ctx context.Context, id string) error {
	if err := d.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("docker: start %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Stop(ctx context.Context, id string) error {
	if err := d.cli.ContainerStop(ctx, id, container.StopOptions{}); err != nil {
		return fmt.Errorf("docker: stop %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Remove(ctx context.Context, id string) error {
	err := d.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
	if client.IsErrNotFound(err) {
		return fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	if err != nil {
		return fmt.Errorf("docker: remove %s: %w", id, err)
	}
	return nil
}

func (d *dockerRuntime) Inspect(ctx context.Context, id string) (ContainerInfo, error) {
	c, err := d.cli.ContainerInspect(ctx, id)
	if client.IsErrNotFound(err) {
		return ContainerInfo{}, fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("docker: inspect %s: %w", id, err)
	}

	info := ContainerInfo{
		ID:      c.ID,
		Name:    strings.TrimPrefix(c.Name, "/"),
		Running: c.State != nil && c.State.Running,
		Labels:  c.Config.Labels,
	}
	if c.NetworkSettings != nil {
		for _, ep := range c.NetworkSettings.Networks {
			if ep.IPAddress != "" {
				info.IPAddress = ep.IPAddress
				break
			}
		}
	}
	return info, nil
}

func (d *dockerRuntime) List(ctx context.Context, labels map[string]string) ([]ContainerInfo, error) {
	args := filters.NewArgs()
	for k, v := range labels {
		args.Add("label", k+"="+v)
	}
	list, err := d.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}

	out := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		// ContainerList's summary omits the network IP reliably, so inspect.
		info, err := d.Inspect(ctx, c.ID)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

func (d *dockerRuntime) Logs(ctx context.Context, id string, tail int) (string, error) {
	rc, err := d.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail),
	})
	if client.IsErrNotFound(err) {
		return "", fmt.Errorf("%w: %s", ErrNoSuchContainer, id)
	}
	if err != nil {
		return "", fmt.Errorf("docker: logs %s: %w", id, err)
	}
	defer rc.Close()

	var b strings.Builder
	if _, err := io.Copy(&b, rc); err != nil {
		return "", fmt.Errorf("docker: read logs %s: %w", id, err)
	}
	return b.String(), nil
}
```

- [ ] **Step 6: Verify the real runtime compiles and satisfies the interface**

Run:

```bash
go build ./... && go vet ./internal/docker/
```

Expected: no output. If the Docker SDK's `container.RestartPolicyUnlessStopped` constant is absent in the pinned version, substitute the string literal `"unless-stopped"`; the SDK moved this constant between releases.

- [ ] **Step 7: Run the full suite**

Run: `go test ./...`
Expected: PASS across config, link, tconfig, client, store, docker.

- [ ] **Step 8: Commit**

```bash
git add internal/docker go.mod go.sum
git commit -m "feat: add docker runtime abstraction with in-memory fake"
```

---

### Task 7: Proxy service — create saga

Deliverable: `Create` builds a working proxy or leaves the system exactly as it found it. This is the task where the design's correctness actually lives.

**Files:**
- Create: `internal/proxy/ports.go`, `internal/proxy/service.go`, `internal/proxy/ports_test.go`, `internal/proxy/create_test.go`

**Interfaces:**
- Consumes: `store.Store`, `docker.Runtime`, `tconfig.Render`, `client.Client`, `link.FakeTLS`, `config.Config`.
- Produces:

```go
type Deps struct {
	Store       *store.Store
	Runtime     docker.Runtime
	Cfg         config.Config
	// HostDataDir is the data directory as the *host* sees it, which is what
	// bind mounts must use. Inside the panel container Cfg.DataDir is /data,
	// but Docker resolves mount sources on the host.
	HostDataDir string
	// NewClient builds a control-API client for a proxy. Overridden in tests.
	NewClient func(p store.Proxy, ip string) TelemtClient
	// Now is injected for deterministic tests.
	Now func() time.Time
}

type TelemtClient interface {
	Health(ctx context.Context) error
	Users(ctx context.Context) ([]client.UserInfo, error)
	PatchUser(ctx context.Context, username string, p client.PatchUser) (client.UserInfo, error)
}

type Service struct { /* built from Deps */ }
func New(d Deps) *Service

type CreateRequest struct {
	Name              string
	Port              int
	TLSDomain         string
	AdTag             string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (store.Proxy, error)
func (s *Service) Link(p store.Proxy) string

const Username = "user"
const HealthBudget = 30 * time.Second

var ErrPortReserved error
```

- [ ] **Step 1: Write the failing port-allocator tests**

Create `internal/proxy/ports_test.go`:

```go
package proxy

import (
	"errors"
	"net"
	"testing"
)

func TestCheckPortReserved(t *testing.T) {
	for _, p := range []int{80, 8443} {
		if err := CheckPort(p, []int{80, 8443}); !errors.Is(err, ErrPortReserved) {
			t.Errorf("CheckPort(%d) error = %v, want ErrPortReserved", p, err)
		}
	}
}

func TestCheckPortOutOfRange(t *testing.T) {
	for _, p := range []int{0, -1, 65536} {
		if err := CheckPort(p, nil); err == nil {
			t.Errorf("CheckPort(%d) error = nil, want error", p)
		}
	}
}

func TestCheckPortFree(t *testing.T) {
	// Grab an ephemeral port, note it, release it — it is then almost
	// certainly free.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	if err := CheckPort(port, nil); err != nil {
		t.Errorf("CheckPort(%d) on a free port error = %v", port, err)
	}
}

func TestCheckPortInUse(t *testing.T) {
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port

	if err := CheckPort(port, nil); err == nil {
		t.Fatalf("CheckPort(%d) on a bound port error = nil, want error", port)
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/proxy/ -v`
Expected: FAIL — undefined `CheckPort`, `ErrPortReserved`.

- [ ] **Step 3: Implement the port check**

Create `internal/proxy/ports.go`:

```go
package proxy

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

var ErrPortReserved = errors.New("proxy: port is reserved by the panel")

// CheckPort rejects reserved and out-of-range ports, then confirms nothing on
// the host is already bound to it. The bind test is advisory: a proxy created
// a microsecond later could still lose a race, which is why the database also
// enforces port uniqueness.
func CheckPort(port int, reserved []int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("proxy: port %d out of range 1-65535", port)
	}
	for _, r := range reserved {
		if port == r {
			return fmt.Errorf("%w: %d is used by the panel's web server", ErrPortReserved, port)
		}
	}

	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("proxy: port %d is already in use on this host: %w", port, err)
	}
	return l.Close()
}
```

- [ ] **Step 4: Run the port tests and confirm they pass**

Run: `go test ./internal/proxy/ -run TestCheckPort -v`
Expected: PASS — four tests.

- [ ] **Step 5: Write the failing create-saga tests**

Create `internal/proxy/create_test.go`:

```go
package proxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

// stubClient is a TelemtClient whose Health result the test controls.
type stubClient struct {
	healthErr error
	users     []client.UserInfo
}

func (s *stubClient) Health(context.Context) error { return s.healthErr }
func (s *stubClient) Users(context.Context) ([]client.UserInfo, error) {
	return s.users, nil
}
func (s *stubClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}

func newService(t *testing.T, fake *docker.Fake, stub *stubClient) (*Service, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := New(Deps{
		Store:       st,
		Runtime:     fake,
		Cfg:         config.Config{DataDir: dir, Network: "mtpanel_net", NetworkSubnet: "172.28.0.0/16", TelemtImage: "img", PublicHost: "1.2.3.4", ReservedPorts: []int{80, 8443}},
		HostDataDir: dir,
		NewClient:   func(store.Proxy, string) TelemtClient { return stub },
		Now:         time.Now,
	})
	return svc, dir
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func TestCreateHappyPath(t *testing.T) {
	fake := docker.NewFake()
	svc, dir := newService(t, fake, &stubClient{})
	port := freePort(t)

	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "main", Port: port, TLSDomain: "petrovich.ru",
		AdTag: "ffeeddccbbaa99887766554433221100",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if p.State != store.StateRunning {
		t.Errorf("State = %q, want running", p.State)
	}
	if len(p.Secret) != 32 {
		t.Errorf("Secret = %q, want 32 hex chars", p.Secret)
	}
	if p.APIToken == "" {
		t.Error("APIToken should be generated")
	}
	if p.ContainerID == "" {
		t.Error("ContainerID should be recorded")
	}

	// Config file written where the container will bind-mount it.
	cfgPath := filepath.Join(dir, "proxies", p.ID, "config.toml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read rendered config: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, p.Secret) {
		t.Error("rendered config is missing the generated secret")
	}
	if !strings.Contains(body, "ffeeddccbbaa99887766554433221100") {
		t.Error("rendered config is missing the ad tag")
	}
	if !strings.Contains(body, `whitelist = ["172.28.0.0/16"]`) {
		t.Errorf("rendered config has the wrong API whitelist:\n%s", body)
	}

	// Container spec is correct.
	if len(fake.Created) != 1 {
		t.Fatalf("len(fake.Created) = %d, want 1", len(fake.Created))
	}
	spec := fake.Created[0]
	if spec.Port != port {
		t.Errorf("spec.Port = %d, want %d", spec.Port, port)
	}
	if spec.Labels["mtpanel.proxy_id"] != p.ID {
		t.Errorf("spec.Labels[mtpanel.proxy_id] = %q, want %q", spec.Labels["mtpanel.proxy_id"], p.ID)
	}
	if spec.Labels["mtpanel.managed"] != "true" {
		t.Error("container is missing the mtpanel.managed label")
	}
	if spec.ConfigHostDir != filepath.Join(dir, "proxies", p.ID) {
		t.Errorf("spec.ConfigHostDir = %q", spec.ConfigHostDir)
	}
	if spec.Name != "telemt-"+p.ID {
		t.Errorf("spec.Name = %q, want telemt-%s", spec.Name, p.ID)
	}

	if got := fake.Networks["mtpanel_net"]; got != "172.28.0.0/16" {
		t.Errorf("network subnet = %q", got)
	}
}

func TestCreateRejectsReservedPort(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})

	_, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: 8443, TLSDomain: "a.com",
	})
	if !errors.Is(err, ErrPortReserved) {
		t.Fatalf("Create() error = %v, want ErrPortReserved", err)
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0 — nothing should be created", fake.Count())
	}
}

func TestCreateRejectsDuplicatePort(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	port := freePort(t)
	ctx := context.Background()

	if _, err := svc.Create(ctx, CreateRequest{Name: "a", Port: port, TLSDomain: "a.com"}); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	// The first proxy's container is a fake and binds nothing, so the host
	// bind check passes; the database must be what rejects this.
	_, err := svc.Create(ctx, CreateRequest{Name: "b", Port: port, TLSDomain: "b.com"})
	if !errors.Is(err, store.ErrPortTaken) {
		t.Fatalf("second Create() error = %v, want ErrPortTaken", err)
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1", fake.Count())
	}
}

func TestCreateRollsBackOnContainerCreateFailure(t *testing.T) {
	fake := docker.NewFake()
	fake.FailCreate = errors.New("no such image")
	svc, dir := newService(t, fake, &stubClient{})

	_, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: freePort(t), TLSDomain: "a.com",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want failure")
	}

	proxies, _ := svc.deps.Store.ListProxies(context.Background())
	if len(proxies) != 0 {
		t.Errorf("store has %d proxies, want 0 after rollback", len(proxies))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "proxies"))
	if len(entries) != 0 {
		t.Errorf("config dir has %d entries, want 0 after rollback", len(entries))
	}
}

func TestCreateRollsBackOnStartFailure(t *testing.T) {
	fake := docker.NewFake()
	fake.FailStart = errors.New("port already allocated")
	svc, dir := newService(t, fake, &stubClient{})

	_, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: freePort(t), TLSDomain: "a.com",
	})
	if err == nil {
		t.Fatal("Create() error = nil, want failure")
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0 — the container should be removed", fake.Count())
	}
	proxies, _ := svc.deps.Store.ListProxies(context.Background())
	if len(proxies) != 0 {
		t.Errorf("store has %d proxies, want 0", len(proxies))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "proxies"))
	if len(entries) != 0 {
		t.Errorf("config dir has %d entries, want 0", len(entries))
	}
}

// A container that starts but never becomes healthy is kept, not rolled back,
// so the operator can read its logs.
func TestCreateKeepsContainerOnHealthTimeout(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{healthErr: errors.New("connection refused")})

	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "x", Port: freePort(t), TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create() should not return an error for an unhealthy container, got %v", err)
	}
	if p.State != store.StateError {
		t.Errorf("State = %q, want error", p.State)
	}
	if p.StateMessage == "" {
		t.Error("StateMessage should explain the health failure")
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1 — the container must be kept for log inspection", fake.Count())
	}
}

func TestLink(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := store.Proxy{
		Port: 443, TLSDomain: "petrovich.ru",
		Secret: "00112233445566778899aabbccddeeff",
	}
	want := "tg://proxy?server=1.2.3.4&port=443&secret=ee00112233445566778899aabbccddeeff706574726f766963682e7275"
	if got := svc.Link(p); got != want {
		t.Errorf("Link() =\n  %q\nwant\n  %q", got, want)
	}
}
```

Add `"net"` to that file's imports.

- [ ] **Step 6: Run and confirm failure**

Run: `go test ./internal/proxy/ -v`
Expected: FAIL — undefined `New`, `Deps`, `Service`, `CreateRequest`.

- [ ] **Step 7: Implement the service and the create saga**

Create `internal/proxy/service.go`:

```go
// Package proxy is the panel's service layer. It is the only place that
// coordinates the store, the container runtime and telemt's control API.
package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
	"github.com/kiineld/telemt-panel/internal/telemt/link"
	"github.com/kiineld/telemt-panel/internal/telemt/tconfig"
)

// Username is the telemt user name inside every proxy container. One proxy
// holds exactly one user, so this never varies.
const Username = "user"

// HealthBudget is how long Create waits for a new container's control API to
// answer before giving up and marking the proxy as errored.
const HealthBudget = 30 * time.Second

const (
	LabelManaged = "mtpanel.managed"
	LabelProxyID = "mtpanel.proxy_id"
)

// TelemtClient is the subset of the control API the service uses.
type TelemtClient interface {
	Health(ctx context.Context) error
	Users(ctx context.Context) ([]client.UserInfo, error)
	PatchUser(ctx context.Context, username string, p client.PatchUser) (client.UserInfo, error)
}

type Deps struct {
	Store       *store.Store
	Runtime     docker.Runtime
	Cfg         config.Config
	HostDataDir string
	NewClient   func(p store.Proxy, ip string) TelemtClient
	Now         func() time.Time
}

type Service struct{ deps Deps }

func New(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.NewClient == nil {
		d.NewClient = func(p store.Proxy, ip string) TelemtClient {
			return client.New("http://"+ip+":9091", p.APIToken)
		}
	}
	if d.HostDataDir == "" {
		d.HostDataDir = d.Cfg.DataDir
	}
	return &Service{deps: d}
}

type CreateRequest struct {
	Name              string
	Port              int
	TLSDomain         string
	AdTag             string
	DataQuotaBytes    *uint64
	ExpirationRFC3339 *string
	MaxTCPConns       *int
	MaxUniqueIPs      *int
}

// Create builds a proxy through a saga: every completed step registers a
// compensating action, and any failure before the container starts unwinds
// them all. Once the container is running it is kept even if it never becomes
// healthy, so its logs remain readable.
func (s *Service) Create(ctx context.Context, req CreateRequest) (store.Proxy, error) {
	if err := CheckPort(req.Port, s.deps.Cfg.ReservedPorts); err != nil {
		return store.Proxy{}, err
	}
	if req.TLSDomain == "" {
		return store.Proxy{}, errors.New("proxy: fake domain is required")
	}
	if req.Name == "" {
		req.Name = fmt.Sprintf("proxy-%d", req.Port)
	}

	secret, err := randomHex(16)
	if err != nil {
		return store.Proxy{}, err
	}
	token, err := randomHex(32)
	if err != nil {
		return store.Proxy{}, err
	}

	p := store.Proxy{
		ID: uuid.NewString(), Name: req.Name, Port: req.Port,
		TLSDomain: req.TLSDomain, AdTag: req.AdTag,
		Secret: secret, APIToken: token, State: store.StateCreating,
		DataQuotaBytes: req.DataQuotaBytes, ExpirationRFC3339: req.ExpirationRFC3339,
		MaxTCPConns: req.MaxTCPConns, MaxUniqueIPs: req.MaxUniqueIPs,
	}

	var undo []func()
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}

	// 1. Claim the port in the database. This is the authoritative check.
	if err := s.deps.Store.CreateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}
	undo = append(undo, func() {
		_ = s.deps.Store.DeleteProxy(context.WithoutCancel(ctx), p.ID)
	})

	// 2. Write config.toml into the directory the container will mount.
	if err := s.writeConfig(p); err != nil {
		rollback()
		return store.Proxy{}, err
	}
	undo = append(undo, func() { _ = os.RemoveAll(s.configDir(p.ID)) })

	// 3. Make sure the private network and the image exist.
	if err := s.deps.Runtime.EnsureNetwork(ctx, s.deps.Cfg.Network, s.deps.Cfg.NetworkSubnet); err != nil {
		rollback()
		return store.Proxy{}, err
	}
	if err := s.deps.Runtime.Pull(ctx, s.deps.Cfg.TelemtImage); err != nil {
		rollback()
		return store.Proxy{}, err
	}

	// 4. Create the container.
	id, err := s.deps.Runtime.Create(ctx, docker.ContainerSpec{
		Name:  "telemt-" + p.ID,
		Image: s.deps.Cfg.TelemtImage,
		Labels: map[string]string{
			LabelManaged: "true",
			LabelProxyID: p.ID,
		},
		ConfigHostDir: s.hostConfigDir(p.ID),
		Port:          p.Port,
		Network:       s.deps.Cfg.Network,
	})
	if err != nil {
		rollback()
		return store.Proxy{}, err
	}
	undo = append(undo, func() {
		_ = s.deps.Runtime.Remove(context.WithoutCancel(ctx), id)
	})
	p.ContainerID = id

	// 5. Start it.
	if err := s.deps.Runtime.Start(ctx, id); err != nil {
		rollback()
		return store.Proxy{}, err
	}

	// Past this point the container stays even on failure.
	if err := s.waitHealthy(ctx, p); err != nil {
		p.State = store.StateError
		p.StateMessage = err.Error()
	} else {
		p.State = store.StateRunning
		p.StateMessage = ""
	}
	if err := s.deps.Store.UpdateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}
	return p, nil
}

// Link returns the tg:// fake-TLS link for a proxy, computed locally so it can
// be shown before the container is healthy.
func (s *Service) Link(p store.Proxy) string {
	host := s.deps.Cfg.PublicHost
	if host == "" {
		host = "SERVER-IP"
	}
	l, err := link.FakeTLS(host, p.Port, p.Secret, p.TLSDomain)
	if err != nil {
		return ""
	}
	return l
}

func (s *Service) waitHealthy(ctx context.Context, p store.Proxy) error {
	deadline := s.deps.Now().Add(HealthBudget)
	var lastErr error

	for s.deps.Now().Before(deadline) {
		info, err := s.deps.Runtime.Inspect(ctx, p.ContainerID)
		if err == nil && info.IPAddress != "" {
			if lastErr = s.deps.NewClient(p, info.IPAddress).Health(ctx); lastErr == nil {
				return nil
			}
		} else if err != nil {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("proxy: control API did not become healthy within %s: %v", HealthBudget, lastErr)
}

func (s *Service) writeConfig(p store.Proxy) error {
	dir := s.configDir(p.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("proxy: create config dir: %w", err)
	}

	body, err := tconfig.Render(tconfig.Spec{
		Username: Username, Secret: p.Secret, Port: p.Port,
		TLSDomain: p.TLSDomain, AdTag: p.AdTag,
		APIToken: p.APIToken, APIWhitelist: []string{s.deps.Cfg.NetworkSubnet},
		PublicHost:     s.deps.Cfg.PublicHost,
		DataQuotaBytes: p.DataQuotaBytes, ExpirationRFC3339: p.ExpirationRFC3339,
		MaxTCPConns:    p.MaxTCPConns, MaxUniqueIPs: p.MaxUniqueIPs,
	})
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "config.toml"), body)
}

// writeAtomic writes via a temp file and rename in the same directory, so a
// reader (telemt) never observes a partial config.
func writeAtomic(path, body string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return fmt.Errorf("proxy: temp config: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return fmt.Errorf("proxy: write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("proxy: close config: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("proxy: chmod config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("proxy: rename config: %w", err)
	}
	return nil
}

// configDir is the path as this process sees it.
func (s *Service) configDir(id string) string {
	return filepath.Join(s.deps.Cfg.DataDir, "proxies", id)
}

// hostConfigDir is the same directory as the Docker daemon sees it, which is
// what a bind mount source must be.
func (s *Service) hostConfigDir(id string) string {
	return filepath.Join(s.deps.HostDataDir, "proxies", id)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("proxy: generate random: %w", err)
	}
	return hex.EncodeToString(b), nil
}
```

- [ ] **Step 8: Run the tests and confirm they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS — all eleven tests. `TestCreateKeepsContainerOnHealthTimeout` takes ~30s of wall clock because `waitHealthy` polls until its budget expires; if that is too slow to iterate on, temporarily lower `HealthBudget`. Do **not** leave it lowered.

- [ ] **Step 9: Make the health-timeout test fast**

The 30s test is a drag on every future run. Make the budget injectable: add `HealthBudget time.Duration` to `Deps`, default it to the `HealthBudget` constant in `New` when zero, and use `s.deps.HealthBudget` in `waitHealthy`. Then set `HealthBudget: 50 * time.Millisecond` in `newService`.

Run: `go test ./internal/proxy/ -v`
Expected: PASS in under a second.

- [ ] **Step 10: Commit**

```bash
git add internal/proxy go.mod go.sum
git commit -m "feat: add proxy create saga with rollback"
```

---

### Task 8: Proxy service — delete, edit, recreate, reconcile

Deliverable: the remaining lifecycle operations, with hot edits and recreate edits kept clearly separate.

**Files:**
- Modify: `internal/proxy/service.go`
- Create: `internal/proxy/lifecycle.go`, `internal/proxy/lifecycle_test.go`

**Interfaces:**
- Consumes: everything from Task 7.
- Produces:

```go
type LimitsPatch struct {
	AdTag             *string  // pointer-to-empty-string clears the tag
	DataQuotaBytes    **uint64 // nil = unchanged; non-nil pointing at nil = clear
	ExpirationRFC3339 **string
	MaxTCPConns       **int
	MaxUniqueIPs      **int
}

func (s *Service) Get(ctx context.Context, id string) (store.Proxy, error)
func (s *Service) List(ctx context.Context) ([]store.Proxy, error)
func (s *Service) Delete(ctx context.Context, id string) error
func (s *Service) UpdateLimits(ctx context.Context, id string, patch LimitsPatch) (store.Proxy, error)
func (s *Service) Recreate(ctx context.Context, id string, port int, tlsDomain string) (store.Proxy, error)
func (s *Service) Logs(ctx context.Context, id string) (string, error)
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error)
func (s *Service) ClientFor(ctx context.Context, p store.Proxy) (TelemtClient, error)

type ReconcileReport struct {
	Orphans   []string // container IDs with no matching proxy row
	Restarted []string // proxy IDs whose container was missing and was rebuilt
	CleanedUp []string // proxy IDs abandoned mid-create and removed
}
```

- [ ] **Step 1: Write the failing tests**

Create `internal/proxy/lifecycle_test.go`:

```go
package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

func mustCreate(t *testing.T, svc *Service, port int) store.Proxy {
	t.Helper()
	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "p", Port: port, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return p
}

func TestDeleteRemovesEverything(t *testing.T) {
	fake := docker.NewFake()
	svc, dir := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	if err := svc.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0", fake.Count())
	}
	if _, err := svc.Get(context.Background(), p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxies", p.ID)); !os.IsNotExist(err) {
		t.Error("config dir should be removed")
	}
}

func TestDeleteSucceedsWhenContainerAlreadyGone(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	// Simulate someone running `docker rm` by hand.
	if err := fake.Remove(context.Background(), p.ContainerID); err != nil {
		t.Fatalf("pre-remove: %v", err)
	}
	if err := svc.Delete(context.Background(), p.ID); err != nil {
		t.Fatalf("Delete() error = %v, want nil when the container is already gone", err)
	}
}

// patchRecorder captures what UpdateLimits sends to the control API.
type patchRecorder struct {
	stubClient
	got client.PatchUser
}

func (p *patchRecorder) PatchUser(_ context.Context, _ string, in client.PatchUser) (client.UserInfo, error) {
	p.got = in
	return client.UserInfo{Username: Username}, nil
}

func TestUpdateLimitsIsHot(t *testing.T) {
	fake := docker.NewFake()
	rec := &patchRecorder{}
	svc, _ := newService(t, fake, &stubClient{})
	svc.deps.NewClient = func(store.Proxy, string) TelemtClient { return rec }

	p := mustCreate(t, svc, freePort(t))
	before := fake.Count()

	quota := uint64(5000)
	quotaPtr := &quota
	updated, err := svc.UpdateLimits(context.Background(), p.ID, LimitsPatch{
		DataQuotaBytes: &quotaPtr,
	})
	if err != nil {
		t.Fatalf("UpdateLimits() error = %v", err)
	}
	if updated.DataQuotaBytes == nil || *updated.DataQuotaBytes != 5000 {
		t.Errorf("DataQuotaBytes = %v, want 5000", updated.DataQuotaBytes)
	}
	if rec.got.DataQuotaBytes == nil || *rec.got.DataQuotaBytes != 5000 {
		t.Errorf("patch sent to telemt = %+v", rec.got)
	}
	if fake.Count() != before {
		t.Error("UpdateLimits must not touch containers")
	}
}

func TestUpdateLimitsClearsWithNilPointer(t *testing.T) {
	fake := docker.NewFake()
	rec := &patchRecorder{}
	svc, _ := newService(t, fake, &stubClient{})
	svc.deps.NewClient = func(store.Proxy, string) TelemtClient { return rec }

	quota := uint64(100)
	p, err := svc.Create(context.Background(), CreateRequest{
		Name: "p", Port: freePort(t), TLSDomain: "a.com", DataQuotaBytes: &quota,
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	var clear *uint64 // nil
	updated, err := svc.UpdateLimits(context.Background(), p.ID, LimitsPatch{DataQuotaBytes: &clear})
	if err != nil {
		t.Fatalf("UpdateLimits() error = %v", err)
	}
	if updated.DataQuotaBytes != nil {
		t.Errorf("DataQuotaBytes = %v, want nil after clearing", updated.DataQuotaBytes)
	}
}

func TestRecreateChangesPortAndDomain(t *testing.T) {
	fake := docker.NewFake()
	svc, dir := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))
	oldContainer := p.ContainerID
	newPort := freePort(t)

	got, err := svc.Recreate(context.Background(), p.ID, newPort, "bsi.bund.de")
	if err != nil {
		t.Fatalf("Recreate() error = %v", err)
	}
	if got.Port != newPort {
		t.Errorf("Port = %d, want %d", got.Port, newPort)
	}
	if got.TLSDomain != "bsi.bund.de" {
		t.Errorf("TLSDomain = %q", got.TLSDomain)
	}
	if got.ContainerID == oldContainer {
		t.Error("ContainerID should change after recreate")
	}
	if got.Secret != p.Secret {
		t.Error("secret must survive a recreate, or every user's link breaks unnecessarily")
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1 — the old container must be removed", fake.Count())
	}

	raw, err := os.ReadFile(filepath.Join(dir, "proxies", p.ID, "config.toml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !contains(string(raw), "bsi.bund.de") {
		t.Error("rewritten config should carry the new domain")
	}
}

func TestRecreateRejectsReservedPort(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	if _, err := svc.Recreate(context.Background(), p.ID, 80, "a.com"); !errors.Is(err, ErrPortReserved) {
		t.Fatalf("Recreate() error = %v, want ErrPortReserved", err)
	}
}

func TestReconcileRebuildsMissingContainer(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})
	p := mustCreate(t, svc, freePort(t))

	if err := fake.Remove(context.Background(), p.ContainerID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	rep, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(rep.Restarted) != 1 || rep.Restarted[0] != p.ID {
		t.Errorf("Restarted = %v, want [%s]", rep.Restarted, p.ID)
	}
	if fake.Count() != 1 {
		t.Errorf("fake.Count() = %d, want 1", fake.Count())
	}
}

func TestReconcileFlagsOrphans(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})

	id, err := fake.Create(context.Background(), docker.ContainerSpec{
		Name:   "telemt-ghost",
		Labels: map[string]string{LabelManaged: "true", LabelProxyID: "ghost"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rep, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0] != id {
		t.Errorf("Orphans = %v, want [%s]", rep.Orphans, id)
	}
}

func TestReconcileCleansUpAbandonedCreate(t *testing.T) {
	fake := docker.NewFake()
	svc, _ := newService(t, fake, &stubClient{})

	// A row left behind by a panel that died mid-create: state creating,
	// no container.
	p := store.Proxy{
		ID: "half", Name: "half", Port: freePort(t), TLSDomain: "a.com",
		Secret: "00112233445566778899aabbccddeeff", APIToken: "t",
		State: store.StateCreating,
	}
	if err := svc.deps.Store.CreateProxy(context.Background(), p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rep, err := svc.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(rep.CleanedUp) != 1 || rep.CleanedUp[0] != "half" {
		t.Errorf("CleanedUp = %v, want [half]", rep.CleanedUp)
	}
	if _, err := svc.Get(context.Background(), "half"); !errors.Is(err, store.ErrNotFound) {
		t.Error("abandoned proxy row should be deleted")
	}
}

var _ = net.Listen // keep the net import when freePort lives in another file
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/proxy/ -v`
Expected: FAIL — undefined `Delete`, `UpdateLimits`, `Recreate`, `Reconcile`, `LimitsPatch`.

- [ ] **Step 3: Implement the lifecycle operations**

Create `internal/proxy/lifecycle.go`:

```go
package proxy

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

func (s *Service) Get(ctx context.Context, id string) (store.Proxy, error) {
	return s.deps.Store.GetProxy(ctx, id)
}

func (s *Service) List(ctx context.Context) ([]store.Proxy, error) {
	return s.deps.Store.ListProxies(ctx)
}

// ClientFor builds a control-API client for a running proxy, resolving its
// address on the panel's private network.
func (s *Service) ClientFor(ctx context.Context, p store.Proxy) (TelemtClient, error) {
	if p.ContainerID == "" {
		return nil, fmt.Errorf("proxy %s has no container", p.ID)
	}
	info, err := s.deps.Runtime.Inspect(ctx, p.ContainerID)
	if err != nil {
		return nil, err
	}
	if info.IPAddress == "" {
		return nil, fmt.Errorf("proxy %s has no address on %s", p.ID, s.deps.Cfg.Network)
	}
	return s.deps.NewClient(p, info.IPAddress), nil
}

// Delete removes the container, the config directory and the database row.
// A container that has already vanished is not an error.
func (s *Service) Delete(ctx context.Context, id string) error {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return err
	}

	if p.ContainerID != "" {
		err := s.deps.Runtime.Remove(ctx, p.ContainerID)
		if err != nil && !errors.Is(err, docker.ErrNoSuchContainer) {
			return err
		}
	}
	if err := os.RemoveAll(s.configDir(id)); err != nil {
		return fmt.Errorf("proxy: remove config dir: %w", err)
	}
	return s.deps.Store.DeleteProxy(ctx, id)
}

// LimitsPatch expresses three states per field. A nil outer pointer leaves the
// value alone; a non-nil outer pointer to a nil inner pointer clears it; a
// non-nil outer pointer to a value sets it.
type LimitsPatch struct {
	AdTag             *string
	DataQuotaBytes    **uint64
	ExpirationRFC3339 **string
	MaxTCPConns       **int
	MaxUniqueIPs      **int
}

// UpdateLimits applies changes that telemt can hot-reload, with no downtime.
func (s *Service) UpdateLimits(ctx context.Context, id string, patch LimitsPatch) (store.Proxy, error) {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return store.Proxy{}, err
	}

	if patch.AdTag != nil {
		p.AdTag = *patch.AdTag
	}
	if patch.DataQuotaBytes != nil {
		p.DataQuotaBytes = *patch.DataQuotaBytes
	}
	if patch.ExpirationRFC3339 != nil {
		p.ExpirationRFC3339 = *patch.ExpirationRFC3339
	}
	if patch.MaxTCPConns != nil {
		p.MaxTCPConns = *patch.MaxTCPConns
	}
	if patch.MaxUniqueIPs != nil {
		p.MaxUniqueIPs = *patch.MaxUniqueIPs
	}

	// Persist to config.toml first so the change survives a container restart,
	// then hot-apply it to the running process.
	if err := s.writeConfig(p); err != nil {
		return store.Proxy{}, err
	}
	if c, cerr := s.ClientFor(ctx, p); cerr == nil {
		if _, perr := c.PatchUser(ctx, Username, apiPatch(patch)); perr != nil {
			// The file is already correct, so the change lands on next restart.
			p.StateMessage = "limits saved; live apply failed: " + perr.Error()
		}
	}

	if err := s.deps.Store.UpdateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}
	return p, nil
}

func apiPatch(patch LimitsPatch) client.PatchUser {
	var api client.PatchUser
	if patch.AdTag != nil {
		tag := *patch.AdTag
		api.UserAdTag = &tag
	}
	if patch.DataQuotaBytes != nil {
		api.DataQuotaBytes = *patch.DataQuotaBytes
	}
	if patch.ExpirationRFC3339 != nil {
		api.ExpirationRFC3339 = *patch.ExpirationRFC3339
	}
	if patch.MaxTCPConns != nil {
		api.MaxTCPConns = *patch.MaxTCPConns
	}
	if patch.MaxUniqueIPs != nil {
		api.MaxUniqueIPs = *patch.MaxUniqueIPs
	}
	return api
}

// Recreate applies a port or fake-domain change, which telemt cannot hot-reload.
// The secret is preserved so links only break for the reason the operator chose
// (a new domain), never gratuitously.
func (s *Service) Recreate(ctx context.Context, id string, port int, tlsDomain string) (store.Proxy, error) {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return store.Proxy{}, err
	}
	if port != p.Port {
		if err := CheckPort(port, s.deps.Cfg.ReservedPorts); err != nil {
			return store.Proxy{}, err
		}
	}
	if tlsDomain == "" {
		return store.Proxy{}, errors.New("proxy: fake domain is required")
	}

	old := p.ContainerID
	p.State = store.StateRecreating
	if err := s.deps.Store.UpdateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}

	if old != "" {
		if err := s.deps.Runtime.Remove(ctx, old); err != nil && !errors.Is(err, docker.ErrNoSuchContainer) {
			return store.Proxy{}, err
		}
	}

	p.Port, p.TLSDomain, p.ContainerID = port, tlsDomain, ""
	if err := s.deps.Store.UpdateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}
	if err := s.writeConfig(p); err != nil {
		return store.Proxy{}, err
	}
	return s.startContainer(ctx, p)
}

// startContainer creates and starts a container for an existing proxy row and
// records the outcome. Shared by Recreate and Reconcile.
func (s *Service) startContainer(ctx context.Context, p store.Proxy) (store.Proxy, error) {
	id, err := s.deps.Runtime.Create(ctx, docker.ContainerSpec{
		Name:  "telemt-" + p.ID,
		Image: s.deps.Cfg.TelemtImage,
		Labels: map[string]string{
			LabelManaged: "true",
			LabelProxyID: p.ID,
		},
		ConfigHostDir: s.hostConfigDir(p.ID),
		Port:          p.Port,
		Network:       s.deps.Cfg.Network,
	})
	if err != nil {
		p.State, p.StateMessage = store.StateError, err.Error()
		_ = s.deps.Store.UpdateProxy(ctx, p)
		return store.Proxy{}, err
	}
	p.ContainerID = id

	if err := s.deps.Runtime.Start(ctx, id); err != nil {
		p.State, p.StateMessage = store.StateError, err.Error()
		_ = s.deps.Store.UpdateProxy(ctx, p)
		return store.Proxy{}, err
	}

	if err := s.waitHealthy(ctx, p); err != nil {
		p.State, p.StateMessage = store.StateError, err.Error()
	} else {
		p.State, p.StateMessage = store.StateRunning, ""
	}
	if err := s.deps.Store.UpdateProxy(ctx, p); err != nil {
		return store.Proxy{}, err
	}
	return p, nil
}

func (s *Service) Logs(ctx context.Context, id string) (string, error) {
	p, err := s.deps.Store.GetProxy(ctx, id)
	if err != nil {
		return "", err
	}
	if p.ContainerID == "" {
		return "", fmt.Errorf("proxy %s has no container", id)
	}
	return s.deps.Runtime.Logs(ctx, p.ContainerID, 200)
}

type ReconcileReport struct {
	Orphans   []string
	Restarted []string
	CleanedUp []string
}

// Reconcile makes the world match the database after a panel or host restart.
func (s *Service) Reconcile(ctx context.Context) (ReconcileReport, error) {
	var rep ReconcileReport

	proxies, err := s.deps.Store.ListProxies(ctx)
	if err != nil {
		return rep, err
	}
	containers, err := s.deps.Runtime.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		return rep, err
	}

	byProxyID := make(map[string]docker.ContainerInfo, len(containers))
	for _, c := range containers {
		byProxyID[c.Labels[LabelProxyID]] = c
	}
	known := make(map[string]bool, len(proxies))
	for _, p := range proxies {
		known[p.ID] = true
	}

	for _, c := range containers {
		if !known[c.Labels[LabelProxyID]] {
			rep.Orphans = append(rep.Orphans, c.ID)
		}
	}

	for _, p := range proxies {
		c, ok := byProxyID[p.ID]
		if ok {
			// Keep the recorded container id honest after a daemon restart.
			if p.ContainerID != c.ID {
				p.ContainerID = c.ID
				_ = s.deps.Store.UpdateProxy(ctx, p)
			}
			continue
		}

		if p.State == store.StateCreating {
			// The panel died mid-create; nothing was ever running.
			_ = os.RemoveAll(s.configDir(p.ID))
			if err := s.deps.Store.DeleteProxy(ctx, p.ID); err == nil {
				rep.CleanedUp = append(rep.CleanedUp, p.ID)
			}
			continue
		}

		if _, err := s.startContainer(ctx, p); err == nil {
			rep.Restarted = append(rep.Restarted, p.ID)
		}
	}

	return rep, nil
}
```

- [ ] **Step 4: Verify the package builds cleanly**

`UpdateLimits` assigns only to `p`, writes the config, then hot-applies via
`apiPatch(patch)` — that helper is the single source of the API payload, so
there is no second copy of the field mapping to drift.

Run: `go vet ./internal/proxy/`
Expected: no output, no unused-variable complaints.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/proxy/ -v`
Expected: PASS — all tests from Tasks 7 and 8.

- [ ] **Step 6: Commit**

```bash
git add internal/proxy
git commit -m "feat: add proxy delete, hot limit updates, recreate and reconcile"
```

---

### Task 9: Stats poller

Deliverable: one background loop polls every proxy's control API and caches the result. Browsers read the cache, so ten open tabs cost telemt nothing extra.

**Files:**
- Create: `internal/poller/poller.go`, `internal/poller/poller_test.go`

**Interfaces:**
- Consumes: `proxy.Service` via the `Source` interface below.
- Produces:

```go
type Snapshot struct {
	ProxyID     string
	OK          bool
	Err         string
	UniqueIPs   int
	IPs         []string
	Connections uint64
	TotalOctets uint64
	Links       []string
	At          time.Time
}

// Source is the slice of proxy.Service the poller needs.
type Source interface {
	List(ctx context.Context) ([]store.Proxy, error)
	ClientFor(ctx context.Context, p store.Proxy) (proxy.TelemtClient, error)
}

func New(src Source, interval time.Duration) *Poller
func (p *Poller) Run(ctx context.Context)          // blocks until ctx is done
func (p *Poller) PollOnce(ctx context.Context)     // one sweep; used by Run and tests
func (p *Poller) Get(proxyID string) (Snapshot, bool)
func (p *Poller) All() map[string]Snapshot
func (p *Poller) Subscribe() (<-chan struct{}, func())
```

Backoff rule: after 3 consecutive failures a proxy is polled once every 6th sweep instead of every sweep, until it succeeds again.

- [ ] **Step 1: Write the failing tests**

Create `internal/poller/poller_test.go`:

```go
package poller

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

type fakeClient struct {
	mu    sync.Mutex
	users []client.UserInfo
	err   error
	calls int
}

func (f *fakeClient) Health(context.Context) error { return nil }
func (f *fakeClient) Users(context.Context) ([]client.UserInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.users, f.err
}
func (f *fakeClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}
func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeSource struct {
	proxies []store.Proxy
	clients map[string]*fakeClient
	clientErr error
}

func (s *fakeSource) List(context.Context) ([]store.Proxy, error) { return s.proxies, nil }
func (s *fakeSource) ClientFor(_ context.Context, p store.Proxy) (proxy.TelemtClient, error) {
	if s.clientErr != nil {
		return nil, s.clientErr
	}
	return s.clients[p.ID], nil
}

func TestPollOnceCachesStats(t *testing.T) {
	fc := &fakeClient{users: []client.UserInfo{{
		Username: "user", ActiveUniqueIPs: 4,
		ActiveUniqueIPsList: []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"},
		CurrentConnections:  11, TotalOctets: 999,
		Links: client.UserLinks{TLS: []string{"tg://proxy?x=1"}},
	}}}
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": fc},
	}

	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, ok := p.Get("a")
	if !ok {
		t.Fatal("Get(a) not found")
	}
	if !got.OK {
		t.Errorf("OK = false, Err = %q", got.Err)
	}
	if got.UniqueIPs != 4 {
		t.Errorf("UniqueIPs = %d, want 4", got.UniqueIPs)
	}
	if len(got.IPs) != 4 {
		t.Errorf("len(IPs) = %d, want 4", len(got.IPs))
	}
	if got.Connections != 11 {
		t.Errorf("Connections = %d, want 11", got.Connections)
	}
	if got.TotalOctets != 999 {
		t.Errorf("TotalOctets = %d, want 999", got.TotalOctets)
	}
	if len(got.Links) != 1 || got.Links[0] != "tg://proxy?x=1" {
		t.Errorf("Links = %v", got.Links)
	}
	if got.At.IsZero() {
		t.Error("At should be stamped")
	}
}

func TestPollOnceRecordsFailure(t *testing.T) {
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": {err: errors.New("connection refused")}},
	}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, ok := p.Get("a")
	if !ok {
		t.Fatal("Get(a) not found")
	}
	if got.OK {
		t.Error("OK = true, want false")
	}
	if got.Err == "" {
		t.Error("Err should describe the failure")
	}
}

func TestPollOnceHandlesUnreachableClient(t *testing.T) {
	src := &fakeSource{
		proxies:   []store.Proxy{{ID: "a"}},
		clientErr: errors.New("no such container"),
	}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	got, _ := p.Get("a")
	if got.OK {
		t.Error("OK = true, want false when no client can be built")
	}
}

func TestBackoffAfterRepeatedFailures(t *testing.T) {
	fc := &fakeClient{err: errors.New("down")}
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": fc},
	}
	p := New(src, time.Second)

	// Sweeps 1-3 all attempt; from sweep 4 on, only every 6th sweep attempts.
	for i := 0; i < 3; i++ {
		p.PollOnce(context.Background())
	}
	if fc.callCount() != 3 {
		t.Fatalf("calls after 3 sweeps = %d, want 3", fc.callCount())
	}

	for i := 0; i < 5; i++ {
		p.PollOnce(context.Background())
	}
	if got := fc.callCount(); got != 3 {
		t.Errorf("calls after 5 more sweeps = %d, want 3 — backoff should skip them", got)
	}

	p.PollOnce(context.Background())
	if got := fc.callCount(); got != 4 {
		t.Errorf("calls on the 6th backoff sweep = %d, want 4", got)
	}
}

func TestBackoffResetsOnSuccess(t *testing.T) {
	fc := &fakeClient{err: errors.New("down")}
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": fc},
	}
	p := New(src, time.Second)
	for i := 0; i < 3; i++ {
		p.PollOnce(context.Background())
	}

	fc.mu.Lock()
	fc.err = nil
	fc.mu.Unlock()

	// Advance to the sweep where backoff lets it retry.
	for i := 0; i < 6; i++ {
		p.PollOnce(context.Background())
	}
	if got, _ := p.Get("a"); !got.OK {
		t.Fatal("proxy should recover after a successful poll")
	}

	before := fc.callCount()
	p.PollOnce(context.Background())
	if fc.callCount() != before+1 {
		t.Error("polling should return to every sweep after a success")
	}
}

func TestSubscribeNotifiesOnSweep(t *testing.T) {
	src := &fakeSource{
		proxies: []store.Proxy{{ID: "a"}},
		clients: map[string]*fakeClient{"a": {}},
	}
	p := New(src, time.Second)

	ch, cancel := p.Subscribe()
	defer cancel()

	p.PollOnce(context.Background())
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber was not notified within 1s")
	}
}

func TestSubscribeCancelStopsNotifications(t *testing.T) {
	src := &fakeSource{proxies: []store.Proxy{{ID: "a"}}, clients: map[string]*fakeClient{"a": {}}}
	p := New(src, time.Second)

	ch, cancel := p.Subscribe()
	cancel()
	p.PollOnce(context.Background())

	select {
	case _, open := <-ch:
		if open {
			t.Error("cancelled subscriber received a notification")
		}
	default:
	}
}

func TestAllReturnsACopy(t *testing.T) {
	src := &fakeSource{proxies: []store.Proxy{{ID: "a"}}, clients: map[string]*fakeClient{"a": {}}}
	p := New(src, time.Second)
	p.PollOnce(context.Background())

	m := p.All()
	delete(m, "a")
	if _, ok := p.Get("a"); !ok {
		t.Error("mutating the map from All() affected the poller's state")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	src := &fakeSource{proxies: []store.Proxy{{ID: "a"}}, clients: map[string]*fakeClient{"a": {}}}
	p := New(src, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/poller/ -v`
Expected: FAIL — undefined `New`, `Snapshot`, `Poller`.

- [ ] **Step 3: Implement the poller**

Create `internal/poller/poller.go`:

```go
// Package poller keeps a cache of live per-proxy statistics.
//
// A single loop polls every proxy, so browser count does not affect load on
// telemt. Failing proxies back off so a dead container does not dominate the
// sweep.
package poller

import (
	"context"
	"sync"
	"time"

	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
)

// failuresBeforeBackoff is how many consecutive errors a proxy may have before
// the poller starts skipping sweeps for it.
const failuresBeforeBackoff = 3

// backoffEvery means a backed-off proxy is retried on one sweep in six.
const backoffEvery = 6

type Snapshot struct {
	ProxyID     string
	OK          bool
	Err         string
	UniqueIPs   int
	IPs         []string
	Connections uint64
	TotalOctets uint64
	Links       []string
	At          time.Time
}

type Source interface {
	List(ctx context.Context) ([]store.Proxy, error)
	ClientFor(ctx context.Context, p store.Proxy) (proxy.TelemtClient, error)
}

type Poller struct {
	src      Source
	interval time.Duration

	mu        sync.RWMutex
	snapshots map[string]Snapshot
	failures  map[string]int
	sweep     uint64

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

func New(src Source, interval time.Duration) *Poller {
	return &Poller{
		src:       src,
		interval:  interval,
		snapshots: map[string]Snapshot{},
		failures:  map[string]int{},
		subs:      map[chan struct{}]struct{}{},
	}
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.interval)
	defer t.Stop()

	p.PollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.PollOnce(ctx)
		}
	}
}

// PollOnce performs one sweep across all proxies.
func (p *Poller) PollOnce(ctx context.Context) {
	proxies, err := p.src.List(ctx)
	if err != nil {
		return
	}

	p.mu.Lock()
	p.sweep++
	sweep := p.sweep
	skip := make(map[string]bool, len(proxies))
	for _, pr := range proxies {
		if f := p.failures[pr.ID]; f >= failuresBeforeBackoff && sweep%backoffEvery != 0 {
			skip[pr.ID] = true
		}
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	results := make(chan Snapshot, len(proxies))

	for _, pr := range proxies {
		if skip[pr.ID] {
			continue
		}
		wg.Add(1)
		go func(pr store.Proxy) {
			defer wg.Done()
			results <- p.pollProxy(ctx, pr)
		}(pr)
	}
	wg.Wait()
	close(results)

	p.mu.Lock()
	for snap := range results {
		p.snapshots[snap.ProxyID] = snap
		if snap.OK {
			delete(p.failures, snap.ProxyID)
		} else {
			p.failures[snap.ProxyID]++
		}
	}
	// Drop cache entries for proxies that no longer exist.
	live := make(map[string]bool, len(proxies))
	for _, pr := range proxies {
		live[pr.ID] = true
	}
	for id := range p.snapshots {
		if !live[id] {
			delete(p.snapshots, id)
			delete(p.failures, id)
		}
	}
	p.mu.Unlock()

	p.notify()
}

func (p *Poller) pollProxy(ctx context.Context, pr store.Proxy) Snapshot {
	snap := Snapshot{ProxyID: pr.ID, At: time.Now()}

	c, err := p.src.ClientFor(ctx, pr)
	if err != nil {
		snap.Err = err.Error()
		return snap
	}

	users, err := c.Users(ctx)
	if err != nil {
		snap.Err = err.Error()
		return snap
	}
	if len(users) == 0 {
		snap.Err = "telemt reported no users"
		return snap
	}

	u := users[0]
	snap.OK = true
	snap.UniqueIPs = u.ActiveUniqueIPs
	snap.IPs = u.ActiveUniqueIPsList
	snap.Connections = u.CurrentConnections
	snap.TotalOctets = u.TotalOctets
	snap.Links = u.Links.TLS
	return snap
}

func (p *Poller) Get(proxyID string) (Snapshot, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s, ok := p.snapshots[proxyID]
	return s, ok
}

// All returns a copy, so callers can range over it without holding the lock.
func (p *Poller) All() map[string]Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]Snapshot, len(p.snapshots))
	for k, v := range p.snapshots {
		out[k] = v
	}
	return out
}

// Subscribe returns a channel that receives a token after every sweep, plus a
// function to unsubscribe. The channel is buffered and sends are dropped when
// full, so a slow reader never blocks the poll loop.
func (p *Poller) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)

	p.subMu.Lock()
	p.subs[ch] = struct{}{}
	p.subMu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			p.subMu.Lock()
			delete(p.subs, ch)
			p.subMu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

func (p *Poller) notify() {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	for ch := range p.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `go test ./internal/poller/ -race -v`
Expected: PASS — all nine tests, no race warnings. The `-race` flag matters here: this is the only package with real concurrency.

- [ ] **Step 5: Commit**

```bash
git add internal/poller
git commit -m "feat: add stats poller with backoff and subscriber fan-out"
```

---

### Task 10: Authentication

Deliverable: first boot generates an admin password and logs it; login issues a session cookie; brute force is rate-limited; the first login forces a password change.

**Files:**
- Create: `internal/web/auth.go`, `internal/web/auth_test.go`

**Interfaces:**
- Consumes: `store.Store`.
- Produces:

```go
func HashPassword(plain string) (string, error)     // argon2id, encoded PHC string
func VerifyPassword(encoded, plain string) bool

type Auth struct{ /* store + limiter */ }
func NewAuth(st *store.Store) *Auth

// Bootstrap creates the "admin" account on first boot and returns the
// generated password. Returns ("", nil) when an admin already exists.
func (a *Auth) Bootstrap(ctx context.Context) (string, error)

func (a *Auth) Login(ctx context.Context, ip, username, plain string) (token string, adm store.Admin, err error)
func (a *Auth) Logout(ctx context.Context, token string) error
func (a *Auth) Session(ctx context.Context, token string) (store.Admin, error)
func (a *Auth) ChangePassword(ctx context.Context, id int64, plain string) error

var ErrBadCredentials, ErrRateLimited error
const SessionTTL = 7 * 24 * time.Hour
const MinPasswordLen = 10
```

- [ ] **Step 1: Add the crypto dependency**

```bash
go get golang.org/x/crypto@latest
```

- [ ] **Step 2: Write the failing tests**

Create `internal/web/auth_test.go`:

```go
package web

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiineld/telemt-panel/internal/store"
)

func newAuth(t *testing.T) (*Auth, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewAuth(st), st
}

func TestHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Errorf("hash = %q, want an argon2id PHC string", h)
	}
	if !VerifyPassword(h, "correct horse battery") {
		t.Error("VerifyPassword() = false for the correct password")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("VerifyPassword() = true for a wrong password")
	}
}

func TestHashIsSalted(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	for _, h := range []string{"", "notahash", "$argon2id$v=19$m=1", "$argon2id$v=19$m=65536,t=1,p=1$!!!$!!!"} {
		if VerifyPassword(h, "x") {
			t.Errorf("VerifyPassword(%q) = true, want false", h)
		}
	}
}

func TestBootstrapCreatesAdminOnce(t *testing.T) {
	a, st := newAuth(t)
	ctx := context.Background()

	pw, err := a.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if len(pw) < MinPasswordLen {
		t.Errorf("generated password %q is shorter than %d chars", pw, MinPasswordLen)
	}

	adm, err := st.AdminByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("AdminByUsername() error = %v", err)
	}
	if !VerifyPassword(adm.PasswordHash, pw) {
		t.Error("the generated password does not verify against the stored hash")
	}
	if !adm.MustChangePassword {
		t.Error("bootstrapped admin should be required to change the password")
	}

	pw2, err := a.Bootstrap(ctx)
	if err != nil {
		t.Fatalf("second Bootstrap() error = %v", err)
	}
	if pw2 != "" {
		t.Errorf("second Bootstrap() = %q, want empty — it must not reset the password", pw2)
	}
}

func TestLoginSuccess(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)

	token, adm, err := a.Login(ctx, "1.2.3.4", "admin", pw)
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if token == "" {
		t.Fatal("Login() returned an empty token")
	}
	if adm.Username != "admin" {
		t.Errorf("Username = %q", adm.Username)
	}

	got, err := a.Session(ctx, token)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if got.ID != adm.ID {
		t.Errorf("Session() returned admin %d, want %d", got.ID, adm.ID)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	_, _, err := a.Login(ctx, "1.2.3.4", "admin", "nope")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login() error = %v, want ErrBadCredentials", err)
	}
}

func TestLoginUnknownUserIsIndistinguishable(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	_, _, err := a.Login(ctx, "1.2.3.4", "ghost", "whatever")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("Login() error = %v, want ErrBadCredentials", err)
	}
}

func TestLoginRateLimited(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	_, _ = a.Bootstrap(ctx)

	for i := 0; i < 5; i++ {
		if _, _, err := a.Login(ctx, "9.9.9.9", "admin", "bad"); !errors.Is(err, ErrBadCredentials) {
			t.Fatalf("attempt %d error = %v, want ErrBadCredentials", i, err)
		}
	}
	if _, _, err := a.Login(ctx, "9.9.9.9", "admin", "bad"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("6th attempt error = %v, want ErrRateLimited", err)
	}
}

func TestRateLimitIsPerIP(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)

	for i := 0; i < 6; i++ {
		_, _, _ = a.Login(ctx, "9.9.9.9", "admin", "bad")
	}
	if _, _, err := a.Login(ctx, "8.8.8.8", "admin", pw); err != nil {
		t.Fatalf("a different IP should not be limited, got %v", err)
	}
}

func TestSuccessfulLoginClearsRateLimit(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)

	for i := 0; i < 3; i++ {
		_, _, _ = a.Login(ctx, "7.7.7.7", "admin", "bad")
	}
	if _, _, err := a.Login(ctx, "7.7.7.7", "admin", pw); err != nil {
		t.Fatalf("Login() with the right password error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, _, err := a.Login(ctx, "7.7.7.7", "admin", "bad"); errors.Is(err, ErrRateLimited) {
			t.Fatalf("attempt %d was rate limited; the counter should have reset", i)
		}
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)
	token, _, _ := a.Login(ctx, "1.2.3.4", "admin", pw)

	if err := a.Logout(ctx, token); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := a.Session(ctx, token); err == nil {
		t.Fatal("Session() = nil error after logout, want failure")
	}
}

func TestChangePassword(t *testing.T) {
	a, _ := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)
	_, adm, _ := a.Login(ctx, "1.2.3.4", "admin", pw)

	if err := a.ChangePassword(ctx, adm.ID, "short"); err == nil {
		t.Error("ChangePassword() accepted a password below the minimum length")
	}
	if err := a.ChangePassword(ctx, adm.ID, "a-long-enough-password"); err != nil {
		t.Fatalf("ChangePassword() error = %v", err)
	}
	if _, _, err := a.Login(ctx, "1.2.3.4", "admin", "a-long-enough-password"); err != nil {
		t.Fatalf("Login() with the new password error = %v", err)
	}
	if _, _, err := a.Login(ctx, "1.2.3.4", "admin", pw); !errors.Is(err, ErrBadCredentials) {
		t.Error("the old password still works after a change")
	}
}

func TestSessionTokenIsNotStoredInPlaintext(t *testing.T) {
	a, st := newAuth(t)
	ctx := context.Background()
	pw, _ := a.Bootstrap(ctx)
	token, _, _ := a.Login(ctx, "1.2.3.4", "admin", pw)

	// The raw token must not resolve directly as a stored hash.
	if _, err := st.SessionAdmin(ctx, token); err == nil {
		t.Error("the raw session token is stored verbatim; it must be hashed")
	}
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `go test ./internal/web/ -v`
Expected: FAIL — undefined `Auth`, `HashPassword`, etc.

- [ ] **Step 4: Implement authentication**

Create `internal/web/auth.go`:

```go
package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/kiineld/telemt-panel/internal/store"
)

var (
	ErrBadCredentials = errors.New("auth: invalid username or password")
	ErrRateLimited    = errors.New("auth: too many failed attempts, try again later")
)

const (
	SessionTTL     = 7 * 24 * time.Hour
	MinPasswordLen = 10

	maxAttempts   = 5
	attemptWindow = 15 * time.Minute
)

// argon2id parameters. Memory dominates cost; 64 MiB keeps login well under
// 100ms on a small VPS while staying expensive to brute force.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}

	var memory uint32
	var timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}

	got := argon2.IDKey([]byte(plain), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

type Auth struct {
	store *store.Store

	mu       sync.Mutex
	attempts map[string]*attemptRecord
}

type attemptRecord struct {
	count int
	first time.Time
}

func NewAuth(st *store.Store) *Auth {
	return &Auth{store: st, attempts: map[string]*attemptRecord{}}
}

// Bootstrap creates the admin account on first boot. It returns the generated
// password exactly once; on later calls it returns an empty string.
func (a *Auth) Bootstrap(ctx context.Context) (string, error) {
	n, err := a.store.AdminCount(ctx)
	if err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}

	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate password: %w", err)
	}
	pw := base64.RawURLEncoding.EncodeToString(raw)

	hash, err := HashPassword(pw)
	if err != nil {
		return "", err
	}
	if _, err := a.store.CreateAdmin(ctx, "admin", hash); err != nil {
		return "", err
	}
	return pw, nil
}

func (a *Auth) Login(ctx context.Context, ip, username, plain string) (string, store.Admin, error) {
	if a.limited(ip) {
		return "", store.Admin{}, ErrRateLimited
	}

	adm, err := a.store.AdminByUsername(ctx, username)
	if err != nil {
		// Spend the same work as a real verify so timing does not reveal
		// whether the account exists.
		_ = VerifyPassword("$argon2id$v=19$m=65536,t=1,p=4$"+
			base64.RawStdEncoding.EncodeToString(make([]byte, argonSaltLen))+"$"+
			base64.RawStdEncoding.EncodeToString(make([]byte, argonKeyLen)), plain)
		a.recordFailure(ip)
		return "", store.Admin{}, ErrBadCredentials
	}

	if !VerifyPassword(adm.PasswordHash, plain) {
		a.recordFailure(ip)
		return "", store.Admin{}, ErrBadCredentials
	}

	token, err := newToken()
	if err != nil {
		return "", store.Admin{}, err
	}
	if err := a.store.CreateSession(ctx, hashToken(token), adm.ID, time.Now().Add(SessionTTL)); err != nil {
		return "", store.Admin{}, err
	}

	a.clearFailures(ip)
	return token, adm, nil
}

func (a *Auth) Logout(ctx context.Context, token string) error {
	return a.store.DeleteSession(ctx, hashToken(token))
}

func (a *Auth) Session(ctx context.Context, token string) (store.Admin, error) {
	return a.store.SessionAdmin(ctx, hashToken(token))
}

func (a *Auth) ChangePassword(ctx context.Context, id int64, plain string) error {
	if len(plain) < MinPasswordLen {
		return fmt.Errorf("auth: password must be at least %d characters", MinPasswordLen)
	}
	hash, err := HashPassword(plain)
	if err != nil {
		return err
	}
	return a.store.SetAdminPassword(ctx, id, hash)
}

func (a *Auth) limited(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.attempts[ip]
	if !ok {
		return false
	}
	if time.Since(r.first) > attemptWindow {
		delete(a.attempts, ip)
		return false
	}
	return r.count >= maxAttempts
}

func (a *Auth) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	r, ok := a.attempts[ip]
	if !ok || time.Since(r.first) > attemptWindow {
		a.attempts[ip] = &attemptRecord{count: 1, first: time.Now()}
		return
	}
	r.count++
}

func (a *Auth) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// hashToken is what gets stored, so a database leak does not hand out sessions.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS — all thirteen tests.

- [ ] **Step 6: Commit**

```bash
git add internal/web go.mod go.sum
git commit -m "feat: add argon2id auth with sessions and login rate limiting"
```

---

### Task 11: Web server, login screen and proxy list

Deliverable: log in, see the proxy list with live counters, create a proxy from a form.

**Files:**
- Create: `internal/web/server.go`, `internal/web/handlers_auth.go`, `internal/web/handlers_proxy.go`, `internal/web/sse.go`, `internal/web/server_test.go`
- Create: `web/templates/layout.html`, `web/templates/login.html`, `web/templates/change_password.html`, `web/templates/proxies.html`, `web/templates/_rows.html`
- Create: `web/static/app.css`, `web/static/vendor/htmx.min.js`, `web/static/vendor/alpine.min.js`

**Interfaces:**
- Consumes: `proxy.Service`, `poller.Poller`, `Auth`.
- Produces:

```go
type ServerDeps struct {
	Auth   *Auth
	Proxy  *proxy.Service
	Poller *poller.Poller
	Cfg    config.Config
}
func NewServer(d ServerDeps) (http.Handler, error)
```

Routes: `GET /healthz`, `GET|POST /login`, `POST /logout`, `GET|POST /password`, `GET /`, `POST /proxies`, `GET /proxies/{id}`, `POST /proxies/{id}/limits`, `POST /proxies/{id}/recreate`, `POST /proxies/{id}/delete`, `GET /proxies/{id}/logs`, `GET /events`, `GET /static/...`.

- [ ] **Step 1: Vendor the front-end libraries**

```bash
mkdir -p web/static/vendor
curl -fsSL https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js -o web/static/vendor/htmx.min.js
curl -fsSL https://unpkg.com/htmx-ext-sse@2.2.2/sse.js -o web/static/vendor/htmx-sse.js
curl -fsSL https://cdn.jsdelivr.net/npm/alpinejs@3.14.8/dist/cdn.min.js -o web/static/vendor/alpine.min.js
ls -la web/static/vendor
```

Expected: three non-empty files. These are committed to the repo — the panel must never fetch from a CDN at runtime.

- [ ] **Step 2: Write the failing server tests**

Create `internal/web/server_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/telemt/client"
)

type okClient struct{}

func (okClient) Health(context.Context) error { return nil }
func (okClient) Users(context.Context) ([]client.UserInfo, error) {
	return []client.UserInfo{{Username: "user", ActiveUniqueIPs: 2, CurrentConnections: 3}}, nil
}
func (okClient) PatchUser(context.Context, string, client.PatchUser) (client.UserInfo, error) {
	return client.UserInfo{}, nil
}

func newTestServer(t *testing.T) (http.Handler, *Auth, *proxy.Service) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := config.Config{
		DataDir: dir, Network: "n", NetworkSubnet: "172.28.0.0/16",
		TelemtImage: "img", PublicHost: "1.2.3.4",
		ReservedPorts: []int{80, 8443}, PollInterval: time.Hour,
	}
	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: docker.NewFake(), Cfg: cfg, HostDataDir: dir,
		NewClient:    func(store.Proxy, string) proxy.TelemtClient { return okClient{} },
		HealthBudget: 50 * time.Millisecond,
	})
	auth := NewAuth(st)

	h, err := NewServer(ServerDeps{
		Auth: auth, Proxy: svc, Poller: poller.New(svc, time.Hour), Cfg: cfg,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return h, auth, svc
}

func loginCookie(t *testing.T, h http.Handler, auth *Auth) *http.Cookie {
	t.Helper()
	pw, err := auth.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	form := url.Values{"username": {"admin"}, "password": {pw}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatalf("no session cookie in response (status %d)", rec.Code)
	return nil
}

func TestHealthzIsPublic(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestIndexRedirectsWhenLoggedOut(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/login" {
		t.Errorf("Location = %q, want /login", got)
	}
}

func TestLoginSetsHardenedCookie(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := loginCookie(t, h, auth)
	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if c.SameSite != http.SameSiteLaxMode && c.SameSite != http.SameSiteStrictMode {
		t.Error("session cookie must set SameSite")
	}
	if c.Path != "/" {
		t.Errorf("cookie Path = %q, want /", c.Path)
	}
}

func TestFirstLoginForcesPasswordChange(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := loginCookie(t, h, auth)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/password" {
		t.Fatalf("status = %d, Location = %q; want 303 to /password", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCreateProxyThroughForm(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := loginCookie(t, h, auth)

	// Clear the forced password change so the app routes normally.
	adm, _ := auth.Session(context.Background(), c.Value)
	if err := auth.ChangePassword(context.Background(), adm.ID, "a-long-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	form := url.Values{
		"name": {"main"}, "port": {"14431"},
		"tls_domain": {"petrovich.ru"},
		"ad_tag":     {"ffeeddccbbaa99887766554433221100"},
	}
	req := httptest.NewRequest(http.MethodPost, "/proxies", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}

	proxies, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(proxies) != 1 {
		t.Fatalf("len(proxies) = %d, want 1", len(proxies))
	}
	if proxies[0].Port != 14431 || proxies[0].TLSDomain != "petrovich.ru" {
		t.Errorf("created proxy = %+v", proxies[0])
	}
}

func TestCreateProxyRejectsReservedPort(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := loginCookie(t, h, auth)
	adm, _ := auth.Session(context.Background(), c.Value)
	_ = auth.ChangePassword(context.Background(), adm.ID, "a-long-password")

	form := url.Values{"name": {"x"}, "port": {"8443"}, "tls_domain": {"a.com"}}
	req := httptest.NewRequest(http.MethodPost, "/proxies", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "reserved") {
		t.Errorf("body should explain the port is reserved:\n%s", rec.Body.String())
	}
}

func TestProxyRoutesRequireAuth(t *testing.T) {
	h, _, _ := newTestServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/proxies"},
		{http.MethodGet, "/proxies/abc"},
		{http.MethodPost, "/proxies/abc/delete"},
		{http.MethodGet, "/events"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 303 or 401", tc.method, tc.path, rec.Code)
		}
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/vendor/htmx.min.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — did you vendor htmx in step 1?", rec.Code)
	}
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `go test ./internal/web/ -run 'TestHealthz|TestIndex|TestLoginSets|TestFirstLogin|TestCreateProxy|TestProxyRoutes|TestStatic' -v`
Expected: FAIL — undefined `NewServer`, `ServerDeps`, `sessionCookie`.

- [ ] **Step 4: Write the templates**

Create `web/templates/layout.html`:

```html
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · telemt-panel</title>
<link rel="stylesheet" href="/static/app.css">
<script src="/static/vendor/htmx.min.js" defer></script>
<script src="/static/vendor/htmx-sse.js" defer></script>
<script src="/static/vendor/alpine.min.js" defer></script>
</head>
<body>
<header>
  <a class="brand" href="/">telemt-panel</a>
  {{if .Admin}}<form method="post" action="/logout"><button class="link">Log out</button></form>{{end}}
</header>
<main>
{{if .Error}}<div class="error">{{.Error}}</div>{{end}}
{{template "content" .}}
</main>
</body>
</html>{{end}}
```

Create `web/templates/login.html`:

```html
{{define "content"}}
<form class="card narrow" method="post" action="/login">
  <h1>Sign in</h1>
  <label>Username <input name="username" value="admin" autocomplete="username" required></label>
  <label>Password <input name="password" type="password" autocomplete="current-password" required></label>
  <button type="submit">Sign in</button>
</form>
{{end}}
```

Create `web/templates/change_password.html`:

```html
{{define "content"}}
<form class="card narrow" method="post" action="/password">
  <h1>Choose a password</h1>
  <p class="muted">This is the generated password's first and only use.</p>
  <label>New password <input name="password" type="password" minlength="10" autocomplete="new-password" required></label>
  <button type="submit">Save</button>
</form>
{{end}}
```

Create `web/templates/proxies.html`:

```html
{{define "content"}}
<div x-data="{open:false}">
  <div class="toolbar">
    <h1>Proxies</h1>
    <button @click="open = !open" x-text="open ? 'Cancel' : 'New proxy'">New proxy</button>
  </div>

  <form class="card" method="post" action="/proxies" x-show="open" x-cloak>
    <div class="grid">
      <label>Name <input name="name" placeholder="main"></label>
      <label>Port <input name="port" type="number" min="1" max="65535" value="443" required></label>
      <label>Fake domain
        <input name="tls_domain" list="domains" value="petrovich.ru" required>
        <datalist id="domains">
          <option value="petrovich.ru"><option value="bsi.bund.de"><option value="telekom.com">
        </datalist>
      </label>
      <label>Ad tag <input name="ad_tag" pattern="[0-9a-fA-F]{32}" placeholder="32 hex from @MTProxybot"></label>
    </div>
    <details>
      <summary>Advanced limits</summary>
      <div class="grid">
        <label>Data quota (GB) <input name="quota_gb" type="number" min="0" step="1"></label>
        <label>Expires <input name="expires" type="date"></label>
        <label>Max connections <input name="max_conns" type="number" min="0"></label>
        <label>Max unique IPs <input name="max_ips" type="number" min="0"></label>
      </div>
    </details>
    <button type="submit">Create proxy</button>
  </form>

  <div hx-ext="sse" sse-connect="/events" sse-swap="rows" hx-target="#rows">
    <div id="rows">{{template "rows" .}}</div>
  </div>
</div>
{{end}}
```

Create `web/templates/_rows.html`:

```html
{{define "rows"}}
{{if not .Rows}}<p class="muted">No proxies yet. Create one above.</p>{{end}}
<div class="rows">
{{range .Rows}}
  <article class="card row state-{{.Proxy.State}}">
    <div class="row-main">
      <a class="name" href="/proxies/{{.Proxy.ID}}">{{.Proxy.Name}}</a>
      <span class="addr">{{$.Host}}:{{.Proxy.Port}}</span>
      <span class="domain">{{.Proxy.TLSDomain}}</span>
      <span class="badge">{{.Proxy.State}}</span>
    </div>
    <div class="row-stats">
      {{if .Stats.OK}}
        <span title="unique source IPs right now"><b>{{.Stats.UniqueIPs}}</b> users</span>
        <span><b>{{.Stats.Connections}}</b> conns</span>
        <span>{{.Traffic}}</span>
      {{else}}
        <span class="muted">stats unavailable</span>
      {{end}}
    </div>
    <div class="row-actions" x-data="{copied:false}">
      <button type="button" @click="navigator.clipboard.writeText('{{.Link}}'); copied=true; setTimeout(()=>copied=false,1500)"
              x-text="copied ? 'Copied' : 'Copy link'">Copy link</button>
    </div>
  </article>
{{end}}
</div>
{{end}}
```

Create `web/static/app.css`:

```css
:root {
  --bg: #14161a; --card: #1c1f26; --fg: #e7e9ee; --muted: #8b93a3;
  --accent: #4f8cff; --error: #ff6b6b; --ok: #46d18b; --border: #2a2f3a;
}
* { box-sizing: border-box; }
[x-cloak] { display: none !important; }
body { margin: 0; background: var(--bg); color: var(--fg);
  font: 15px/1.5 ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif; }
header { display: flex; justify-content: space-between; align-items: center;
  padding: 14px 22px; border-bottom: 1px solid var(--border); }
.brand { color: var(--fg); text-decoration: none; font-weight: 600; }
main { max-width: 980px; margin: 0 auto; padding: 24px 22px 60px; }
h1 { font-size: 20px; margin: 0; }
.toolbar { display: flex; justify-content: space-between; align-items: center; margin-bottom: 18px; }
.card { background: var(--card); border: 1px solid var(--border);
  border-radius: 10px; padding: 18px; margin-bottom: 14px; }
.narrow { max-width: 380px; margin: 60px auto; }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 12px; }
label { display: flex; flex-direction: column; gap: 5px; font-size: 13px; color: var(--muted); }
input { background: #11131a; border: 1px solid var(--border); color: var(--fg);
  border-radius: 7px; padding: 9px 11px; font-size: 14px; }
input:focus { outline: 2px solid var(--accent); outline-offset: -1px; }
button { background: var(--accent); color: #fff; border: 0; border-radius: 7px;
  padding: 9px 15px; font-size: 14px; cursor: pointer; }
button.link { background: none; color: var(--muted); padding: 0; }
details { margin: 14px 0; } summary { cursor: pointer; color: var(--muted); font-size: 13px; }
.row { display: flex; flex-wrap: wrap; gap: 14px; align-items: center; justify-content: space-between; }
.row-main { display: flex; gap: 12px; align-items: baseline; flex-wrap: wrap; }
.name { color: var(--fg); font-weight: 600; text-decoration: none; }
.addr { font-family: ui-monospace, monospace; font-size: 13px; }
.domain, .muted { color: var(--muted); font-size: 13px; }
.row-stats { display: flex; gap: 16px; font-size: 13px; color: var(--muted); }
.row-stats b { color: var(--fg); }
.badge { font-size: 11px; text-transform: uppercase; letter-spacing: .04em;
  padding: 2px 7px; border-radius: 20px; border: 1px solid var(--border); color: var(--muted); }
.state-running .badge { color: var(--ok); border-color: var(--ok); }
.state-error .badge { color: var(--error); border-color: var(--error); }
.error { background: #3a1d20; border: 1px solid var(--error); color: #ffd6d6;
  padding: 11px 14px; border-radius: 8px; margin-bottom: 16px; }
pre { background: #11131a; border: 1px solid var(--border); border-radius: 8px;
  padding: 14px; overflow-x: auto; font-size: 12px; }
```

- [ ] **Step 5: Implement the server**

Create `internal/web/server.go`:

```go
package web

import (
	"embed"
	"fmt"
	"html/template"
	"net"
	"net/http"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
)

//go:embed all:../../web/templates all:../../web/static
var assets embed.FS

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

func NewServer(d ServerDeps) (http.Handler, error) {
	s := &server{ServerDeps: d, tmpl: map[string]*template.Template{}}

	for _, name := range []string{"login.html", "change_password.html", "proxies.html", "proxy.html"} {
		t, err := template.ParseFS(assets,
			"../../web/templates/layout.html",
			"../../web/templates/_rows.html",
			"../../web/templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("web: parse %s: %w", name, err)
		}
		s.tmpl[name] = t
	}

	staticFS, err := fsSub(assets, "../../web/static")
	if err != nil {
		return nil, err
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
// network, so X-Forwarded-For is deliberately not trusted for rate limiting.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
```

Add `internal/web/fs.go`:

```go
package web

import (
	"io/fs"
	"path"
	"strings"
)

// fsSub is fs.Sub with the ".." segments an embed path may contain resolved,
// because embed.FS keys keep the literal directory names.
func fsSub(f fs.FS, dir string) (fs.FS, error) {
	clean := path.Clean(dir)
	clean = strings.TrimPrefix(clean, "../../")
	return fs.Sub(f, path.Join("web", path.Base(clean)))
}
```

**Note for the implementer:** Go's `//go:embed` cannot reference parent directories. If `go build` rejects the `../../web/...` patterns, move the embed into a new file `web/assets.go` at the repository root declaring `package webassets` with `//go:embed templates static` and an exported `var FS embed.FS`, then import it from `internal/web` and drop `fs.go`. Take that route as soon as the compiler complains — do not fight the embed path.

- [ ] **Step 6: Implement the auth and proxy handlers**

Create `internal/web/handlers_auth.go`:

```go
package web

import (
	"errors"
	"net/http"
)

func (s *server) getLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "login.html", page{Title: "Sign in"})
}

func (s *server) postLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.render(w, http.StatusBadRequest, "login.html", page{Title: "Sign in", Error: "Malformed form."})
		return
	}

	token, _, err := s.Auth.Login(r.Context(), clientIP(r),
		r.PostFormValue("username"), r.PostFormValue("password"))
	if err != nil {
		msg := "Invalid username or password."
		if errors.Is(err, ErrRateLimited) {
			msg = "Too many failed attempts. Wait 15 minutes and try again."
		}
		s.render(w, http.StatusUnauthorized, "login.html", page{Title: "Sign in", Error: msg})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(SessionTTL.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) postLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) getPassword(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	s.render(w, http.StatusOK, "change_password.html", page{Title: "Change password", Admin: &adm})
}

func (s *server) postPassword(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	if err := r.ParseForm(); err != nil {
		s.render(w, http.StatusBadRequest, "change_password.html",
			page{Title: "Change password", Admin: &adm, Error: "Malformed form."})
		return
	}
	if err := s.Auth.ChangePassword(r.Context(), adm.ID, r.PostFormValue("password")); err != nil {
		s.render(w, http.StatusBadRequest, "change_password.html",
			page{Title: "Change password", Admin: &adm, Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

Add `"github.com/kiineld/telemt-panel/internal/store"` to that file's imports.

Create `internal/web/handlers_proxy.go`:

```go
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
	s.render(w, http.StatusOK, "proxies.html",
		page{Title: "Proxies", Admin: &adm, Rows: rows, Host: s.host()})
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
```

Create `internal/web/sse.go`:

```go
package web

import (
	"bytes"
	"net/http"
	"time"

	"github.com/kiineld/telemt-panel/internal/store"
)

// getEvents streams re-rendered proxy rows whenever the poller completes a
// sweep. One SSE stream per browser tab; all of them read the poller's cache,
// so tab count never affects load on telemt.
func (s *server) getEvents(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	updates, cancel := s.Poller.Subscribe()
	defer cancel()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	send := func() bool {
		rows, err := s.buildRows(r)
		if err != nil {
			return true
		}
		var buf bytes.Buffer
		if err := s.tmpl["proxies.html"].ExecuteTemplate(&buf, "rows",
			page{Rows: rows, Host: s.host()}); err != nil {
			return true
		}
		if _, err := w.Write(sseFrame("rows", buf.Bytes())); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-updates:
			if !open {
				return
			}
			if !send() {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// sseFrame formats one named SSE event. Every newline in the payload has to
// start its own data: line or the frame is truncated at the first break.
func sseFrame(event string, payload []byte) []byte {
	var b bytes.Buffer
	b.WriteString("event: " + event + "\n")
	for _, line := range bytes.Split(payload, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	return b.Bytes()
}
```

- [ ] **Step 7: Add the HealthBudget field used by the test helper**

`newTestServer` passes `HealthBudget` to `proxy.Deps`. Confirm Task 7 step 9 added that field; if not, add it now:

```go
// in internal/proxy/service.go, inside Deps
HealthBudget time.Duration
```

and in `New`: `if d.HealthBudget == 0 { d.HealthBudget = HealthBudget }`, with `waitHealthy` using `s.deps.HealthBudget`.

Run: `go build ./...`
Expected: no output.

- [ ] **Step 8: Run the tests and confirm they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS. `TestCreateProxyThroughForm` binds port 14431 briefly during the port check; if your machine has it occupied, change the number in the test.

- [ ] **Step 9: Commit**

```bash
git add internal/web web/templates web/static
git commit -m "feat: add web server, login and live proxy list"
```

---

### Task 12: Proxy detail page

Deliverable: full link with QR code, the live list of connected IPs, container logs, editable limits, port/domain change with a warning, and delete.

**Files:**
- Create: `web/templates/proxy.html`, `internal/web/handlers_detail.go`, `internal/web/handlers_detail_test.go`
- Modify: `internal/web/handlers_proxy.go` (nothing to change if `getProxy`, `postLimits`, `postRecreate` live in the new file)

**Interfaces:**
- Consumes: `proxy.Service`, `poller.Poller`.
- Produces: `s.getProxy`, `s.postLimits`, `s.postRecreate` handlers, and `qrDataURI(link string) (string, error)` returning a `data:image/png;base64,...` string.

- [ ] **Step 1: Add the QR dependency**

```bash
go get github.com/skip2/go-qrcode@latest
```

- [ ] **Step 2: Write the failing tests**

Create `internal/web/handlers_detail_test.go`:

```go
package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kiineld/telemt-panel/internal/proxy"
)

func authedSession(t *testing.T, h http.Handler, auth *Auth) *http.Cookie {
	t.Helper()
	c := loginCookie(t, h, auth)
	adm, _ := auth.Session(context.Background(), c.Value)
	if err := auth.ChangePassword(context.Background(), adm.ID, "a-long-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	return c
}

func TestQRDataURI(t *testing.T) {
	uri, err := qrDataURI("tg://proxy?server=1.2.3.4&port=443&secret=eeff")
	if err != nil {
		t.Fatalf("qrDataURI() error = %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Errorf("uri = %q, want a PNG data URI", uri[:min(40, len(uri))])
	}
	if len(uri) < 200 {
		t.Errorf("data URI is suspiciously short (%d chars)", len(uri))
	}
}

func TestProxyDetailShowsLinkAndIPs(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "main", Port: 14432, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/proxies/"+p.ID, nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "secret=ee"+p.Secret) {
		t.Error("detail page does not show the fake-TLS link")
	}
	if !strings.Contains(body, "data:image/png;base64,") {
		t.Error("detail page does not show a QR code")
	}
	if !strings.Contains(body, "petrovich.ru") {
		t.Error("detail page does not show the fake domain")
	}
}

func TestProxyDetailUnknownID(t *testing.T) {
	h, auth, _ := newTestServer(t)
	c := authedSession(t, h, auth)

	req := httptest.NewRequest(http.MethodGet, "/proxies/nope", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPostLimitsUpdatesQuota(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14433, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"quota_gb": {"5"}, "max_conns": {"100"}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/limits", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}

	got, err := svc.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.DataQuotaBytes == nil || *got.DataQuotaBytes != 5*1024*1024*1024 {
		t.Errorf("DataQuotaBytes = %v, want 5 GiB", got.DataQuotaBytes)
	}
	if got.MaxTCPConns == nil || *got.MaxTCPConns != 100 {
		t.Errorf("MaxTCPConns = %v, want 100", got.MaxTCPConns)
	}
}

func TestPostLimitsClearsEmptyFields(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	quota := uint64(1 << 30)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14434, TLSDomain: "a.com", DataQuotaBytes: &quota,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"quota_gb": {""}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/limits", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	h.ServeHTTP(httptest.NewRecorder(), req)

	got, _ := svc.Get(context.Background(), p.ID)
	if got.DataQuotaBytes != nil {
		t.Errorf("DataQuotaBytes = %v, want nil when the field is submitted empty", got.DataQuotaBytes)
	}
}

func TestPostRecreateChangesDomain(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14435, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	form := url.Values{"port": {"14436"}, "tls_domain": {"bsi.bund.de"}}
	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/recreate", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body:\n%s", rec.Code, rec.Body.String())
	}
	got, _ := svc.Get(context.Background(), p.ID)
	if got.Port != 14436 || got.TLSDomain != "bsi.bund.de" {
		t.Errorf("after recreate = port %d, domain %q", got.Port, got.TLSDomain)
	}
}

func TestDeleteProxyThroughForm(t *testing.T) {
	h, auth, svc := newTestServer(t)
	c := authedSession(t, h, auth)
	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "m", Port: 14437, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/proxies/"+p.ID+"/delete", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	list, _ := svc.List(context.Background())
	if len(list) != 0 {
		t.Errorf("len(proxies) = %d, want 0", len(list))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `go test ./internal/web/ -run 'TestQR|TestProxyDetail|TestPostLimits|TestPostRecreate|TestDeleteProxy' -v`
Expected: FAIL — undefined `qrDataURI`; 404s on the detail routes.

- [ ] **Step 4: Write the detail template**

Create `web/templates/proxy.html`:

```html
{{define "content"}}
{{with .Proxy}}
<div class="toolbar">
  <h1>{{.Name}}</h1>
  <a class="muted" href="/">← All proxies</a>
</div>

<article class="card">
  <h2>Connection link</h2>
  <p class="addr">{{$.Host}}:{{.Port}} · {{.TLSDomain}} · <span class="badge">{{.State}}</span></p>
  {{if .StateMessage}}<p class="muted">{{.StateMessage}}</p>{{end}}
  <pre>{{$.Link}}</pre>
  <div x-data="{copied:false}">
    <button type="button" @click="navigator.clipboard.writeText('{{$.Link}}'); copied=true; setTimeout(()=>copied=false,1500)"
            x-text="copied ? 'Copied' : 'Copy link'">Copy link</button>
  </div>
  <img class="qr" src="{{$.QR}}" alt="QR code for the proxy link" width="220" height="220">
</article>

<article class="card">
  <h2>Connected now</h2>
  {{if $.Stats.OK}}
    <p><b>{{$.Stats.UniqueIPs}}</b> unique IPs · <b>{{$.Stats.Connections}}</b> connections</p>
    {{if $.Stats.IPs}}<pre>{{range $.Stats.IPs}}{{.}}
{{end}}</pre>{{end}}
  {{else}}
    <p class="muted">Statistics unavailable{{if $.Stats.Err}}: {{$.Stats.Err}}{{end}}</p>
  {{end}}
</article>

<article class="card">
  <h2>Limits</h2>
  <p class="muted">Applied immediately, without dropping connections.</p>
  <form method="post" action="/proxies/{{.ID}}/limits">
    <div class="grid">
      <label>Ad tag <input name="ad_tag" value="{{.AdTag}}" pattern="[0-9a-fA-F]{32}"></label>
      <label>Data quota (GB) <input name="quota_gb" type="number" min="0" value="{{$.QuotaGB}}"></label>
      <label>Expires <input name="expires" type="date" value="{{$.ExpiresDate}}"></label>
      <label>Max connections <input name="max_conns" type="number" min="0" value="{{$.MaxConns}}"></label>
      <label>Max unique IPs <input name="max_ips" type="number" min="0" value="{{$.MaxIPs}}"></label>
    </div>
    <button type="submit">Save limits</button>
  </form>
</article>

<article class="card">
  <h2>Port and fake domain</h2>
  <p class="muted">Restarts the proxy (about 2 seconds). Changing the fake domain invalidates the link above — everyone using it will need the new one.</p>
  <form method="post" action="/proxies/{{.ID}}/recreate"
        onsubmit="return confirm('Restart this proxy? If you changed the fake domain, the existing link stops working.')">
    <div class="grid">
      <label>Port <input name="port" type="number" min="1" max="65535" value="{{.Port}}" required></label>
      <label>Fake domain <input name="tls_domain" value="{{.TLSDomain}}" required></label>
    </div>
    <button type="submit">Apply and restart</button>
  </form>
</article>

<article class="card">
  <h2>Container logs</h2>
  <pre>{{if $.Logs}}{{$.Logs}}{{else}}No logs available.{{end}}</pre>
</article>

<article class="card">
  <h2>Delete</h2>
  <form method="post" action="/proxies/{{.ID}}/delete"
        onsubmit="return confirm('Delete this proxy for good? The link stops working immediately.')">
    <button type="submit" style="background:var(--error)">Delete proxy</button>
  </form>
</article>
{{end}}
{{end}}
```

Append to `web/static/app.css`:

```css
h2 { font-size: 15px; margin: 0 0 10px; }
.qr { display: block; margin-top: 14px; border-radius: 8px; background: #fff; padding: 8px; }
```

- [ ] **Step 5: Implement the detail handlers**

Create `internal/web/handlers_detail.go`:

```go
package web

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
)

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
		http.Error(w, "proxy not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	link := s.Proxy.Link(p)
	qr, err := qrDataURI(link)
	if err != nil {
		qr = ""
	}
	snap, _ := s.Poller.Get(p.ID)
	logs, _ := s.Proxy.Logs(r.Context(), p.ID)

	s.render(w, http.StatusOK, "proxy.html", detailPage(adm, p, snap, link, qr, logs, s.host()))
}

// detailPage fills the template fields the proxy view needs beyond page's base.
func detailPage(adm store.Admin, p store.Proxy, snap poller.Snapshot,
	link, qr, logs, host string) page {

	pg := page{
		Title: p.Name, Admin: &adm, Host: host,
		Proxy: &p, Stats: snap, Link: link, QR: qr, Logs: logs,
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
	case errors.Is(err, proxy.ErrPortReserved):
		http.Error(w, fmt.Sprintf("port %d is reserved by the panel", port), http.StatusBadRequest)
		return
	case errors.Is(err, store.ErrPortTaken):
		http.Error(w, fmt.Sprintf("port %d is already used by another proxy", port), http.StatusBadRequest)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/proxies/"+id, http.StatusSeeOther)
}

// optionalGB returns nil for an empty field, so submitting a blank box clears
// the limit rather than leaving it untouched.
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
```

- [ ] **Step 6: Extend the page struct and fix imports**

`internal/web/handlers_detail.go` references `poller.Snapshot` in
`detailPage`, so add `"github.com/kiineld/telemt-panel/internal/poller"` to its
imports. Then, in `internal/web/server.go`, add these fields to `page` (its
`Stats poller.Snapshot` field already requires the same import):

```go
	QuotaGB     string
	ExpiresDate string
	MaxConns    string
	MaxIPs      string
```

Run: `go build ./...`
Expected: no output.

- [ ] **Step 7: Run the tests and confirm they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS — all tests from Tasks 10, 11 and 12.

- [ ] **Step 8: Wire everything into main**

Replace `cmd/panel/main.go`:

```go
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/poller"
	"github.com/kiineld/telemt-panel/internal/proxy"
	"github.com/kiineld/telemt-panel/internal/store"
	"github.com/kiineld/telemt-panel/internal/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "proxies"), 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "panel.db"))
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	rt, err := docker.NewDockerRuntime()
	if err != nil {
		log.Fatalf("docker: %v", err)
	}

	// Bind mounts are resolved by the Docker daemon on the host, so the panel
	// needs the host's view of its data directory, not the container's.
	hostDataDir := os.Getenv("PANEL_HOST_DATA_DIR")
	if hostDataDir == "" {
		hostDataDir = cfg.DataDir
		log.Printf("warning: PANEL_HOST_DATA_DIR is unset; proxy config mounts will use %s", hostDataDir)
	}

	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: rt, Cfg: cfg, HostDataDir: hostDataDir,
	})
	auth := web.NewAuth(st)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if pw, err := auth.Bootstrap(ctx); err != nil {
		log.Fatalf("bootstrap: %v", err)
	} else if pw != "" {
		log.Printf("=====================================================")
		log.Printf("  first-boot admin password: %s", pw)
		log.Printf("  username: admin — you will be asked to change this")
		log.Printf("=====================================================")
	}

	if err := rt.EnsureNetwork(ctx, cfg.Network, cfg.NetworkSubnet); err != nil {
		log.Printf("warning: ensure network: %v", err)
	}
	if rep, err := svc.Reconcile(ctx); err != nil {
		log.Printf("warning: reconcile: %v", err)
	} else {
		log.Printf("reconcile: %d restarted, %d cleaned up, %d orphans",
			len(rep.Restarted), len(rep.CleanedUp), len(rep.Orphans))
	}

	pl := poller.New(svc, cfg.PollInterval)
	go pl.Run(ctx)

	h, err := web.NewServer(web.ServerDeps{Auth: auth, Proxy: svc, Poller: pl, Cfg: cfg})
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr, Handler: h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("panel listening on %s", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}
```

Run: `go build ./... && go vet ./...`
Expected: no output.

- [ ] **Step 9: Commit**

```bash
git add internal/web web/templates web/static cmd go.mod go.sum
git commit -m "feat: add proxy detail page with QR, limits, recreate and logs"
```

---

### Task 13: Integration and smoke tests

Deliverable: proof against a real Docker daemon that a created proxy runs, answers, and cleans up — plus a CI job that protects the one-click install promise.

**Files:**
- Create: `internal/proxy/integration_test.go`, `.github/workflows/ci.yml`, `scripts/smoke.sh`

**Interfaces:**
- Consumes: everything.
- Produces: no new exported API.

- [ ] **Step 1: Write the integration test**

Create `internal/proxy/integration_test.go`:

```go
//go:build docker

package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kiineld/telemt-panel/internal/config"
	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/store"
)

// TestRealDockerLifecycle needs a Docker daemon and network access to ghcr.io.
// Run with: go test -tags docker ./internal/proxy/ -v -timeout 10m
func TestRealDockerLifecycle(t *testing.T) {
	rt, err := docker.NewDockerRuntime()
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}

	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	svc := New(Deps{
		Store: st, Runtime: rt, HostDataDir: dir,
		Cfg: config.Config{
			DataDir: dir, Network: "mtpanel_test_net", NetworkSubnet: "172.29.0.0/16",
			TelemtImage: "ghcr.io/telemt/telemt:latest", PublicHost: "127.0.0.1",
			ReservedPorts: []int{80, 8443},
		},
		HealthBudget: 90 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	p, err := svc.Create(ctx, CreateRequest{
		Name: "integration", Port: port, TLSDomain: "petrovich.ru",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = svc.Delete(context.Background(), p.ID) })

	if p.State != store.StateRunning {
		logs, _ := svc.Logs(ctx, p.ID)
		t.Fatalf("State = %q (%s); container logs:\n%s", p.State, p.StateMessage, logs)
	}

	// The control API answers and reports our single user.
	c, err := svc.ClientFor(ctx, p)
	if err != nil {
		t.Fatalf("ClientFor() error = %v", err)
	}
	users, err := c.Users(ctx)
	if err != nil {
		t.Fatalf("Users() error = %v", err)
	}
	if len(users) != 1 || users[0].Username != Username {
		t.Fatalf("users = %+v, want exactly one named %q", users, Username)
	}

	// telemt's own link agrees with the one we compute locally.
	if len(users[0].Links.TLS) == 0 {
		t.Error("telemt returned no TLS links")
	} else {
		t.Logf("telemt link: %s", users[0].Links.TLS[0])
		t.Logf("panel link:  %s", svc.Link(p))
	}

	// The proxy port is actually listening.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy port %d: %v", port, err)
	}
	_ = conn.Close()

	// Delete leaves nothing behind.
	if err := svc.Delete(ctx, p.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := svc.Get(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Get() after delete error = %v, want ErrNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "proxies", p.ID)); !os.IsNotExist(err) {
		t.Error("config directory survived deletion")
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
```

Add `"strconv"` to the imports.

- [ ] **Step 2: Run the integration test**

Run: `go test -tags docker ./internal/proxy/ -run TestRealDockerLifecycle -v -timeout 10m`
Expected: PASS. The first run pulls the telemt image, so allow several minutes. If `State = "error"`, the test prints the container logs — read them; the usual cause is a config field the template got wrong, which is exactly what this test exists to catch.

- [ ] **Step 3: Confirm the default suite still ignores Docker**

Run: `go test ./...`
Expected: PASS, and `internal/proxy` reports no integration test running (the build tag excludes it).

- [ ] **Step 4: Write the smoke script**

Create `scripts/smoke.sh`:

```bash
#!/usr/bin/env bash
# Verifies the one-click install: bring the stack up, log in, create a proxy.
set -euo pipefail

cleanup() {
  docker compose logs --no-color > /tmp/smoke-compose.log 2>&1 || true
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  docker ps -aq --filter "label=mtpanel.managed=true" | xargs -r docker rm -f >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker compose up -d --build

echo "waiting for the panel to answer..."
for i in $(seq 1 60); do
  if curl -fsSk https://localhost:8443/healthz >/dev/null 2>&1; then
    break
  fi
  if [ "$i" -eq 60 ]; then
    echo "panel never became healthy" >&2
    docker compose logs --no-color >&2
    exit 1
  fi
  sleep 2
done
echo "panel is up"

PASSWORD=$(docker compose logs panel --no-color 2>&1 \
  | grep -o 'first-boot admin password: .*' | tail -1 | awk '{print $NF}')
if [ -z "$PASSWORD" ]; then
  echo "could not read the generated admin password from the logs" >&2
  docker compose logs panel --no-color >&2
  exit 1
fi
echo "recovered the generated admin password"

JAR=$(mktemp)
curl -fsSk -c "$JAR" -X POST https://localhost:8443/login \
  -d "username=admin" --data-urlencode "password=$PASSWORD" -o /dev/null

# The first login forces a password change before anything else is reachable.
curl -fsSk -b "$JAR" -c "$JAR" -X POST https://localhost:8443/password \
  -d "password=smoke-test-password" -o /dev/null

STATUS=$(curl -sk -b "$JAR" -o /tmp/smoke-create.html -w '%{http_code}' \
  -X POST https://localhost:8443/proxies \
  -d "name=smoke&port=14999&tls_domain=petrovich.ru")

if [ "$STATUS" != "303" ] && [ "$STATUS" != "200" ]; then
  echo "proxy creation failed with HTTP $STATUS" >&2
  cat /tmp/smoke-create.html >&2
  exit 1
fi

BODY=$(curl -fsSk -b "$JAR" https://localhost:8443/)
if ! echo "$BODY" | grep -q "secret=ee"; then
  echo "the proxy list does not contain a fake-TLS link" >&2
  echo "$BODY" >&2
  exit 1
fi

echo "smoke test passed: panel up, login works, proxy created with a valid ee link"
```

Then:

```bash
chmod +x scripts/smoke.sh
```

- [ ] **Step 5: Run the smoke test locally**

Run: `./scripts/smoke.sh`
Expected: final line `smoke test passed: ...`. This exercises the whole stack the way a new user would.

- [ ] **Step 6: Write the CI workflow**

Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - run: go vet ./...
      - run: go test -race ./...

  smoke:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run the one-click install smoke test
        run: ./scripts/smoke.sh
```

- [ ] **Step 7: Verify the whole suite one last time**

Run:

```bash
go vet ./... && go test -race ./... && ./scripts/smoke.sh
```

Expected: vet silent, all tests pass, smoke test passes.

- [ ] **Step 8: Commit**

```bash
git add internal/proxy/integration_test.go scripts .github
git commit -m "test: add docker integration test and one-click install smoke test"
```

---

### Task 14: Degraded-state UI

Deliverable: the three failure conditions from the spec that the happy-path UI
does not yet surface — a dead Docker daemon, a slow image pull, and orphaned
containers.

**Files:**
- Create: `internal/web/handlers_orphans.go`, `internal/web/degraded_test.go`
- Modify: `internal/web/server.go` (add `DockerOK` to `page`, add the orphan routes), `internal/web/handlers_proxy.go` (set `DockerOK`), `web/templates/layout.html` (banner), `web/templates/proxies.html` (orphan section), `internal/docker/runtime.go` and `client.go` and `fake.go` (add `Ping`), `internal/proxy/lifecycle.go` (add `RemoveOrphan`)

**Interfaces:**
- Consumes: `docker.Runtime`, `proxy.Service`.
- Produces:

```go
// on docker.Runtime
Ping(ctx context.Context) error

// on proxy.Service
func (s *Service) RemoveOrphan(ctx context.Context, containerID string) error
func (s *Service) Orphans(ctx context.Context) ([]docker.ContainerInfo, error)

// on web.page
DockerOK bool
Orphans  []docker.ContainerInfo
```

Image-pull progress is handled by pulling **before** the create form is
submitted rather than streaming bytes: `EnsureImage` runs on panel startup and
the create button is disabled with an explanatory note until it completes. This
is simpler than a progress stream and removes the multi-minute first-create
stall the spec was worried about.

- [ ] **Step 1: Write the failing tests**

Create `internal/web/degraded_test.go`:

```go
package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kiineld/telemt-panel/internal/docker"
	"github.com/kiineld/telemt-panel/internal/proxy"
)

func TestBannerWhenDockerIsDown(t *testing.T) {
	h, auth, _, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)
	fake.FailPing = errors.New("cannot connect to the Docker daemon")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a dead daemon must not break the page", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Docker") {
		t.Error("page should carry a banner naming Docker as unreachable")
	}
}

func TestProxyListStillRendersWhenDockerIsDown(t *testing.T) {
	h, auth, svc, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "survivor", Port: 14501, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fake.FailPing = errors.New("daemon down")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), p.Name) {
		t.Error("the proxy list must still render from SQLite when Docker is unreachable")
	}
}

func TestOrphansListed(t *testing.T) {
	h, auth, _, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	id, err := fake.Create(context.Background(), docker.ContainerSpec{
		Name:   "telemt-ghost",
		Labels: map[string]string{proxy.LabelManaged: "true", proxy.LabelProxyID: "ghost"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), id[:6]) {
		t.Errorf("orphan container %s is not surfaced on the index page", id)
	}
}

func TestRemoveOrphan(t *testing.T) {
	h, auth, _, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	id, _ := fake.Create(context.Background(), docker.ContainerSpec{
		Name:   "telemt-ghost",
		Labels: map[string]string{proxy.LabelManaged: "true", proxy.LabelProxyID: "ghost"},
	})

	req := httptest.NewRequest(http.MethodPost, "/orphans/"+id+"/delete", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if fake.Count() != 0 {
		t.Errorf("fake.Count() = %d, want 0 — the orphan should be removed", fake.Count())
	}
}

func TestRemoveOrphanRefusesManagedProxy(t *testing.T) {
	h, auth, svc, fake := newTestServerWithFake(t)
	c := authedSession(t, h, auth)

	p, err := svc.Create(context.Background(), proxy.CreateRequest{
		Name: "real", Port: 14502, TLSDomain: "a.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/orphans/"+p.ContainerID+"/delete", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusSeeOther && fake.Count() == 0 {
		t.Fatal("a live proxy's container was removed through the orphan route")
	}
}
```

- [ ] **Step 2: Add the test helper that exposes the fake**

In `internal/web/server_test.go`, add alongside `newTestServer`:

```go
func newTestServerWithFake(t *testing.T) (http.Handler, *Auth, *proxy.Service, *docker.Fake) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	fake := docker.NewFake()
	cfg := config.Config{
		DataDir: dir, Network: "n", NetworkSubnet: "172.28.0.0/16",
		TelemtImage: "img", PublicHost: "1.2.3.4",
		ReservedPorts: []int{80, 8443}, PollInterval: time.Hour,
	}
	svc := proxy.New(proxy.Deps{
		Store: st, Runtime: fake, Cfg: cfg, HostDataDir: dir,
		NewClient:    func(store.Proxy, string) proxy.TelemtClient { return okClient{} },
		HealthBudget: 50 * time.Millisecond,
	})
	auth := NewAuth(st)

	h, err := NewServer(ServerDeps{Auth: auth, Proxy: svc, Poller: poller.New(svc, time.Hour), Cfg: cfg})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return h, auth, svc, fake
}
```

Then rewrite `newTestServer` to delegate:

```go
func newTestServer(t *testing.T) (http.Handler, *Auth, *proxy.Service) {
	h, a, s, _ := newTestServerWithFake(t)
	return h, a, s
}
```

- [ ] **Step 3: Run and confirm failure**

Run: `go test ./internal/web/ -run 'TestBanner|TestProxyListStill|TestOrphan|TestRemoveOrphan' -v`
Expected: FAIL — undefined `newTestServerWithFake` fields, `FailPing`, and 404s on `/orphans/`.

- [ ] **Step 4: Add Ping to the runtime**

In `internal/docker/runtime.go`, add to the `Runtime` interface:

```go
	Ping(ctx context.Context) error
```

In `internal/docker/client.go`:

```go
func (d *dockerRuntime) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx); err != nil {
		return fmt.Errorf("docker: ping: %w", err)
	}
	return nil
}
```

In `internal/docker/fake.go`, add the field and method:

```go
	FailPing error
```

```go
func (f *Fake) Ping(context.Context) error { return f.FailPing }
```

- [ ] **Step 5: Add the service methods**

Append to `internal/proxy/lifecycle.go`:

```go
// Orphans returns panel-labelled containers with no matching proxy row.
func (s *Service) Orphans(ctx context.Context) ([]docker.ContainerInfo, error) {
	proxies, err := s.deps.Store.ListProxies(ctx)
	if err != nil {
		return nil, err
	}
	containers, err := s.deps.Runtime.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		return nil, err
	}

	known := make(map[string]bool, len(proxies))
	for _, p := range proxies {
		known[p.ID] = true
	}

	var out []docker.ContainerInfo
	for _, c := range containers {
		if !known[c.Labels[LabelProxyID]] {
			out = append(out, c)
		}
	}
	return out, nil
}

// RemoveOrphan deletes a container only if it is genuinely orphaned, so a
// forged or stale id can never take down a live proxy.
func (s *Service) RemoveOrphan(ctx context.Context, containerID string) error {
	orphans, err := s.Orphans(ctx)
	if err != nil {
		return err
	}
	for _, c := range orphans {
		if c.ID == containerID {
			return s.deps.Runtime.Remove(ctx, containerID)
		}
	}
	return fmt.Errorf("proxy: container %s is not an orphan", containerID)
}

// DockerOK reports whether the daemon is reachable right now.
func (s *Service) DockerOK(ctx context.Context) bool {
	return s.deps.Runtime.Ping(ctx) == nil
}
```

- [ ] **Step 6: Add the handler and route**

Create `internal/web/handlers_orphans.go`:

```go
package web

import (
	"net/http"

	"github.com/kiineld/telemt-panel/internal/store"
)

func (s *server) postRemoveOrphan(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	if err := s.Proxy.RemoveOrphan(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
```

In `internal/web/server.go`, register the route and extend `page`:

```go
	mux.Handle("POST /orphans/{id}/delete", s.authed(s.requirePassword(s.postRemoveOrphan)))
```

```go
	DockerOK bool
	Orphans  []docker.ContainerInfo
```

Add `"github.com/kiineld/telemt-panel/internal/docker"` to `server.go`'s imports.

In `internal/web/handlers_proxy.go`, populate both in `getIndex`, just before rendering:

```go
	pg := page{Title: "Proxies", Admin: &adm, Rows: rows, Host: s.host()}
	pg.DockerOK = s.Proxy.DockerOK(r.Context())
	if pg.DockerOK {
		pg.Orphans, _ = s.Proxy.Orphans(r.Context())
	}
	s.render(w, http.StatusOK, "proxies.html", pg)
```

Note: `DockerOK` defaults to `false` on every other page, so the banner must
render only on pages that actually set it. Guard it with the `Rows` context by
placing the banner inside `proxies.html` rather than `layout.html`.

- [ ] **Step 7: Add the banner and orphan section to the template**

At the top of `{{define "content"}}` in `web/templates/proxies.html`:

```html
{{if not .DockerOK}}
<div class="error">
  Docker is unreachable, so proxies cannot be created, started or stopped right
  now. The list below is read from the panel's own database and may not reflect
  what is actually running.
</div>
{{end}}

{{if .Orphans}}
<article class="card">
  <h2>Unrecognised containers</h2>
  <p class="muted">These carry the panel's labels but have no matching proxy — usually left over from a database reset.</p>
  {{range .Orphans}}
  <div class="row">
    <span class="addr">{{.Name}} <span class="muted">{{slice .ID 0 12}}</span></span>
    <form method="post" action="/orphans/{{.ID}}/delete"
          onsubmit="return confirm('Remove this container?')">
      <button type="submit" style="background:var(--error)">Remove</button>
    </form>
  </div>
  {{end}}
</article>
{{end}}
```

- [ ] **Step 8: Pre-pull the image on startup**

In `cmd/panel/main.go`, after `EnsureNetwork`, add:

```go
	go func() {
		if err := rt.Pull(context.WithoutCancel(ctx), cfg.TelemtImage); err != nil {
			log.Printf("warning: pull %s: %v", cfg.TelemtImage, err)
			return
		}
		log.Printf("telemt image %s ready", cfg.TelemtImage)
	}()
```

This runs in the background so the panel is reachable immediately, and the
first proxy creation finds the image already present instead of stalling for
minutes.

- [ ] **Step 9: Run the tests and confirm they pass**

Run: `go test ./internal/web/ ./internal/proxy/ ./internal/docker/ -v`
Expected: PASS. If `TestOrphansListed` fails on the `slice .ID 0 12` template
call, the fake's ids (`ctr1`) are shorter than 12 characters — change the
template to `{{.ID}}` and the test assertion accordingly.

- [ ] **Step 10: Full verification and commit**

Run: `go vet ./... && go test -race ./...`
Expected: no vet output, all packages pass.

```bash
git add internal web cmd
git commit -m "feat: surface docker outages, orphan containers and image pre-pull"
```

---

## Verification Checklist

Before calling this done, confirm each spec requirement has landed:

- [ ] `git clone && docker compose up -d` works with no `.env` file (Task 1, smoke test)
- [ ] Panel on `:8443`, ACME on `:80`, port `443` assignable to a proxy (Task 1, Task 7 reserved-port check)
- [ ] Create a proxy with port, fake domain and ad-tag (Task 7, Task 11)
- [ ] Get a `tg://` link immediately, verified against telemt's own (Task 2, Task 13)
- [ ] See live unique-IP count per proxy (Task 9, Task 11)
- [ ] Data quota, expiry, max connections, max unique IPs (Task 3, Task 8, Task 12)
- [ ] One container per proxy, one user per container (Task 7)
- [ ] Control API never exposed to host or internet (Task 3 whitelist, Task 6 no port binding)
- [ ] Create failures roll back completely (Task 7)
- [ ] Crash-looping container kept with readable logs (Task 7, Task 12)
- [ ] Reconcile survives panel restart and host reboot (Task 8)
- [ ] Docker-socket risk documented honestly in the README (Task 1)
- [ ] Docker outage shows a banner, list still renders from SQLite (Task 14)
- [ ] Orphaned containers surfaced with a remove action (Task 14)
- [ ] telemt image pre-pulled so the first create does not stall (Task 14)
