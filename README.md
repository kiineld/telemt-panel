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
`docker compose up -d`. The panel then lives at `https://panel.example.com:8443`
— keep the port, since `443` belongs to a proxy. Reaching it by IP keeps
working with the self-signed certificate, so a wrong DNS record does not lock
you out.

## Connection links

Each proxy's `tg://` link and QR code need a real host to embed. Once a
proxy's container reports healthy, the panel picks up telemt's own
self-reported link (telemt detects the server's external address itself),
so **most installs need nothing set here** — create a proxy, wait for it to
turn healthy, and the link just works.

Until that first happens, though, the panel has no address it can vouch for
and shows a warning instead of a link that would not actually work. If you
want a link to appear immediately, or you're behind NAT/a load balancer
where telemt's own detection would guess wrong, set `PANEL_PUBLIC_HOST` in
`.env` to the address clients should use, and re-run `docker compose up -d`.

## Why port 8443?

Caddy serves the panel on `:8443`. It also listens on `:80`, which answers the
ACME challenge and redirects browsers that omit the port. That leaves host port
`443` free to assign to a proxy, where fake-TLS traffic blends in with ordinary
HTTPS.

## Security

The panel mounts the Docker socket, so **it is root-equivalent on this host**.
Anything able to create a container can mount the host filesystem. The panel
publishes no host port of its own (Caddy is the only ingress), hashes passwords
with argon2id, and rate-limits logins — but do not expose it to the internet
without a firewall you trust.
