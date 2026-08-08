# telemt-panel — Design

**Date:** 2026-08-08
**Status:** Approved, ready for implementation planning

## Purpose

A self-hosted web panel for managing [telemt](https://github.com/telemt/telemt) MTProto
proxies. Install with `git clone` + `docker compose up -d`, then create proxies from a
browser — choosing port, fake-TLS domain (`ee` prefix) and ad-tag — and get back a
`tg://` link plus a live count of unique source IPs using each proxy.

## Background: what telemt already provides

telemt ships a Control API (`[server.api]`, default `127.0.0.1:9091`) that covers most of
the required functionality. The panel is a UI, auth layer and container orchestrator over
that API — not a reimplementation of proxy management.

Endpoints the panel calls:

| Endpoint | Use |
| --- | --- |
| `GET /v1/users` | Per-user `active_unique_ips`, `active_unique_ips_list`, `current_connections`, `total_octets`, and pre-generated `tg://proxy` links (`links.tls[]`, `links.tls_domains[]`) |
| `PATCH /v1/users/{username}` | Hot-update limits, ad-tag, expiry (JSON Merge Patch) |
| `GET /v1/health` | Readiness probe confirming a new proxy came up |

Deliberately **not** used, and why:

- `POST /v1/users` — the proxy's single user is written into `config.toml` before the
  container starts, so it exists at boot. Creating it over the API afterwards would add a
  race between container start and user creation for no benefit.
- `PATCH /v1/config` + `POST /v1/system/reload` — the only config fields the panel changes
  are port and `tls_domain`, neither of which is hot-reloadable. Those changes go through
  container recreation instead.

Constraints that shaped the architecture:

- **ad-tag is per user** (`access.user_ad_tags`) — hot-reloadable.
- **fake-TLS domain is per instance** (`censorship.tls_domain` + `tls_domains[]`) — *not*
  hot-reloadable.
- **port is per instance** (`server.port` / `[[server.listeners]]`) — *not* hot-reloadable.

Because port and domain are per-instance and not hot-reloadable, a proxy that genuinely
owns its own port and domain must be its own telemt process.

## Decisions

| Decision | Choice | Rationale |
| --- | --- | --- |
| Proxy model | One telemt container per proxy | Only way port + fake domain are genuinely per-proxy; failure isolation |
| Users per proxy | Exactly one | One proxy = one port + one domain + one ad-tag + one secret = one link; its unique-IP count *is* that proxy's user count |
| Panel ingress | Caddy, auto-HTTPS on `:8443`, ACME HTTP-01 on `:80` | Leaves `:443` free for a proxy, where fake-TLS is most censorship-resistant |
| Stack | Go + HTMX/Alpine | ~20MB image, ~15MB RSS, single static binary, no runtime or dependency churn on install-once infrastructure |
| Feature scope | Core + traffic/expiry limits | No traffic-history charts, no Telegram bot |

### Explicitly out of scope

- Traffic history / time-series charts.
- Telegram bot control surface.
- Multiple users per proxy.
- Multi-server ("node") management.

## Architecture

### Container topology

```
docker compose up -d  ─┬─ caddy    :80 (ACME) + :8443 (panel UI, auto-HTTPS)
                       ├─ panel    (Go binary, no published host ports)
                       └─ network mtpanel_net, 172.28.0.0/16

panel creates at runtime, one per proxy:
   telemt-<id>   host :<port> → mtproto   (only this port is published)
                 mtpanel_net :9091 → Control API (not exposed to host or internet)
```

`docker-compose.yml` contains only `caddy` and `panel`. telemt containers are created
through the Docker Engine API and labelled `mtpanel.managed=true` and
`mtpanel.proxy_id=<uuid>`, which is how the panel re-discovers them after a restart.

### Control API isolation

Each telemt container is configured with:

```toml
[server.api]
enabled = true
listen = "0.0.0.0:9091"
whitelist = ["172.28.0.0/16"]
auth_header = "<per-proxy random 32-byte token>"
```

Port 9091 is never published to the host. The API is reachable only from inside
`mtpanel_net`, and only with the per-proxy bearer token.

### Security posture

The panel mounts `/var/run/docker.sock` and is therefore **root-equivalent on the host** —
anything that can call `POST /containers/create` can mount the host filesystem. A
`docker-socket-proxy` in front of it would not change this, because the panel legitimately
needs container-create. Mitigations, to be stated plainly in the README rather than
implied:

- Panel publishes no host port; Caddy is the only ingress.
- argon2id password hashing; generated password on first boot; forced change on first login.
- Login rate limiting.
- Proxy Control APIs bound to the internal network only, with per-proxy tokens.

## Install flow

```bash
git clone https://github.com/kiineld/telemt-panel && cd telemt-panel && docker compose up -d
```

Must work with **no `.env` editing**:

- `PANEL_DOMAIN` unset → Caddy uses `tls internal` (self-signed) and serves
  `https://<server-ip>:8443`. Setting `PANEL_DOMAIN` and re-running `docker compose up -d`
  swaps in a real Let's Encrypt certificate.
- Admin password generated on first boot, printed to `docker compose logs panel`, and must
  be changed at first login.
- `general.links.public_host` left unset in telemt config so telemt's own external-IP
  detection supplies the link host. Overridable in panel settings for NAT or custom domain.

## Components

| Package | Responsibility | Depends on |
| --- | --- | --- |
| `internal/store` | SQLite: admins, sessions, proxies. Pure data access. | — |
| `internal/telemt/config` | Render `config.toml` from a `ProxySpec` via `text/template` | — |
| `internal/telemt/link` | Build `tg://` links; hex-encode secret and domain | — |
| `internal/telemt/client` | Typed Control API client, including error envelopes | — |
| `internal/docker` | Container lifecycle + network ensure, behind a `Runtime` interface | Docker SDK |
| `internal/proxy` | Service layer; the only place coordinating store + docker + telemt | store, docker, telemt |
| `internal/poller` | Polls each proxy's Control API; keeps an in-memory snapshot | telemt/client |
| `internal/web` | HTTP handlers, auth middleware, SSE, templates | proxy, poller |

`internal/docker` is an interface (`Create/Start/Stop/Remove/List/Inspect/Logs/Pull`) with
a fake implementation in tests. This is what keeps `internal/proxy` — where the real logic
lives — unit-testable without a Docker daemon.

## Data model

SQLite holds **intent only**. Runtime state is never duplicated.

`proxies`: `id`, `name`, `port`, `tls_domain`, `ad_tag`, `secret`, `api_token`,
`data_quota_bytes`, `expiration_rfc3339`, `max_tcp_conns`, `max_unique_ips`, `state`,
`container_id`, `created_at`, `updated_at`.

`admins`: `id`, `username`, `password_hash` (argon2id), `must_change_password`.

`sessions`: `token_hash`, `admin_id`, `expires_at`.

Not stored: unique-IP counts, connection counts, traffic totals. These come from telemt's
API on demand. telemt is the single source of truth for runtime state, which removes any
possibility of the two drifting.

Proxy states: `creating | running | stopped | error | recreating | deleting`.

## Data flow

### Live statistics

```
poller (every 5s) ──> GET /v1/users on each proxy ──> in-memory snapshot cache
                                                            │
browser ──SSE /events───────────────────────────────────────┘
```

One poll loop serves all proxies and all connected browsers, so additional open tabs cost
telemt nothing. Each proxy row renders `active_unique_ips` (the "how many users" figure),
`current_connections`, and `total_octets` against quota.

After 3 consecutive failures for a given proxy the poller backs off to 30s.

### Link generation

The panel computes the link itself so it appears immediately at creation, before the
container is healthy:

```
tg://proxy?server=<host>&port=<port>&secret=ee<secret_hex><domain_hex>
```

Once the container reports healthy, the panel reconciles against telemt's own
`links.tls[]` / `links.tls_domains[]` and prefers telemt's value. Result: sub-second link
display that is still authoritatively verified.

### Create saga

```
allocate port (DB uniqueness + live bind check; reject 80 and 8443)
  → generate secret (32 hex) and api_token
  → write ./data/proxies/<id>/config.toml
  → docker create + start
  → poll /v1/health until ok (30s budget)
  → state = running
```

Every completed step has a compensating action. On failure the saga unwinds — container
removed, config directory deleted, port released — and the actual error is surfaced (e.g.
`port 443 already bound`), never a generic 500.

Exception: if the container starts but fails its health budget, the container is **kept**
and the proxy moves to `error` with a "View logs" action. A crash-looping proxy whose logs
can be read is more useful than one that silently disappeared.

### Edit paths

Split by cost, and the UI shows which applies before the user commits:

- **Hot** (`PATCH /v1/users/{name}`, no downtime): data quota, expiry, max TCP conns, max
  unique IPs, ad-tag.
- **Recreate** (~2s downtime): port, fake domain. Changing the fake domain **invalidates
  the existing `tg://` link**; the UI requires explicit confirmation for this case
  specifically.

### Startup reconcile

On boot the panel lists containers by label and diffs against SQLite:

- Container running, no DB row → flag as orphan, offer adopt or remove.
- DB row, no container → recreate from stored spec.
- DB row in `creating` with no healthy container → clean up (crash mid-create).

This makes `docker compose down && up` and host reboots safe.

## Configuration writes

`config.toml` is written to a temp file and `rename()`d within the mounted directory, so
writes are atomic on the same filesystem. The config is mounted as a *directory* at
`/etc/telemt` (not a single file) precisely so telemt's own API can atomically rewrite it
too — this matches upstream's `docker-compose.yml` guidance.

## Error handling

| Condition | Behavior |
| --- | --- |
| Port already bound on host | Rejected at create time by live bind check, before any write; names the conflict |
| telemt image not present | Pulled on first create with progress streamed to UI; compose also warms it on boot |
| Container crash-loops | Health budget expires → state `error`, container kept, logs viewable |
| Control API unreachable | That row shows "stats unavailable"; other proxies unaffected; poller backs off |
| Docker daemon down | Banner shown; proxy list served read-only from SQLite |
| Panel killed mid-create | Startup reconcile cleans up the half-built proxy |
| Quota exhausted / user expired | Enforced natively by telemt; panel greys the row with the reason. No panel-side enforcement, so no drift |

## Testing

- **Unit** (no Docker, milliseconds): `config.toml` rendering against golden files; link
  builder against known vectors including hex-encoded domains and the `ee` prefix,
  validated against links telemt itself generates; port allocator; state-machine
  transitions; Control API client against `httptest`, covering the documented error
  envelopes (`revision_conflict`, `user_exists`, `read_only`, `unauthorized`).
- **Service layer**: `internal/proxy` against the fake Docker runtime, asserting each saga
  rollback path actually unwinds.
- **Integration** (build tag `docker`): real daemon — create a proxy, assert the container
  runs, its Control API answers, the link parses, and deletion leaves nothing behind.
  Skipped by default so `go test ./...` passes on any machine.
- **Smoke** (CI): `docker compose up -d`, wait for health, create a proxy over HTTP, assert
  200 and a well-formed link. This is the test that protects the one-click install promise,
  which is otherwise the most likely thing to rot unnoticed.

The link builder is built test-first against real telemt-generated links: a malformed link
is the one defect that looks correct in the UI and fails only on the user's phone.

## UI surface

Four screens. Dark by default, one stylesheet, no JS framework — HTMX for SSE-driven
counters, Alpine for the drawer and copy buttons.

1. **Login** — forced password change on first use.
2. **Proxy list** — one card per proxy: name, `host:port`, fake domain, live unique-IP
   count, connections, traffic vs quota, state indicator. Inline copy-link and QR.
3. **Create/edit drawer** — port, fake domain, ad-tag; limits behind an "Advanced" toggle
   so the common path is three fields.
4. **Proxy detail** — full link and QR, active IP list, container logs, delete.
