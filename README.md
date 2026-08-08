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
