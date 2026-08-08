#!/usr/bin/env bash
# Verifies the one-click install: bring the stack up, log in, create a proxy,
# and confirm it actually comes up running with a working link — not just
# that the create request was accepted.
#
# Requires a real Docker daemon (compose v2 plugin) and network access to
# ghcr.io to pull the telemt image. Not run as part of `go test` — see
# .github/workflows/ci.yml's separate "smoke" job.
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

# cmd/panel/main.go logs the first-boot password as:
#   "  first-boot admin password: <pw>"
# (indented inside a banner). grep -o matches the substring regardless of
# the leading whitespace, so the pattern below stays in sync with that line.
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

# A first login forces a password change (server.go's requirePassword
# middleware redirects every other route to /password until this succeeds)
# before any app route — including proxy creation — is reachable.
curl -fsSk -b "$JAR" -c "$JAR" -X POST https://localhost:8443/password \
  -d "password=smoke-test-password" -o /dev/null

HEADERS=$(mktemp)
STATUS=$(curl -sk -b "$JAR" -c "$JAR" -D "$HEADERS" -o /tmp/smoke-create.html -w '%{http_code}' \
  -X POST https://localhost:8443/proxies \
  -d "name=smoke&port=14999&tls_domain=petrovich.ru")

# postCreate (internal/web/handlers_proxy.go) redirects with 303 See Other on
# success; any other status is a rejected or failed create.
if [ "$STATUS" != "303" ]; then
  echo "proxy creation failed with HTTP $STATUS" >&2
  cat /tmp/smoke-create.html >&2
  exit 1
fi

# A bare 303 is not proof of success on its own: the auth middleware
# (server.go's `authed`) also answers with 303 to /login whenever the
# session cookie is missing or invalid, so a session/auth regression would
# otherwise be indistinguishable from "proxy creation succeeded". Confirm
# the redirect actually goes where postCreate sends a real success (/),
# not where a rejected session goes (/login).
LOCATION=$(grep -i '^location:' "$HEADERS" | tail -1 | tr -d '\r\n' | awk '{print $2}')
if [ "$LOCATION" != "/" ]; then
  echo "POST /proxies redirected to '$LOCATION' instead of '/' — this looks like a session/auth regression (redirected to /login), not a successful create" >&2
  cat "$HEADERS" >&2
  exit 1
fi

# A 303 to "/" only means the create request was accepted; Service.Create
# (internal/proxy/service.go) deliberately returns a nil error and still
# redirects even when the container starts but never becomes healthy — it is
# kept at State=error so its logs stay readable. The single check that used
# to live here (grepping the list page for "secret=ee" right after the
# redirect) could not tell a working install from a completely broken one,
# since the link is computed locally and can render even for a dead
# container. Poll the list page instead, for the row to actually reach the
# "running" state _rows.html renders as <span class="badge">running</span>,
# and fail fast if it instead reaches "error".
#
# This also needs the proxy's link to actually be present, not just the
# state: with the shipped default of an empty PANEL_PUBLIC_HOST, the panel
# shows no link at all until telemt reports its own (see Finding 1's
# ReconcileLink) — so both "running" and "secret=ee" must show up together.
#
# Give this a realistic budget: the first run on a cold host has to pull
# ghcr.io/telemt/telemt:latest, which alone can take several minutes.
echo "waiting for the proxy to become running with a working link..."
BODY=""
STATE_OK=0
for i in $(seq 1 120); do
  BODY=$(curl -fsSk -b "$JAR" https://localhost:8443/)
  if echo "$BODY" | grep -q 'class="badge">error<'; then
    echo "the proxy reached the error state instead of running" >&2
    echo "$BODY" >&2
    exit 1
  fi
  if echo "$BODY" | grep -q 'class="badge">running<' && echo "$BODY" | grep -q "secret=ee"; then
    STATE_OK=1
    break
  fi
  sleep 5
done

if [ "$STATE_OK" -ne 1 ]; then
  echo "the proxy never reached a running state with a working link within the timeout" >&2
  echo "$BODY" >&2
  exit 1
fi

echo "smoke test passed: panel up, login works, proxy created, became running, and has a valid ee link"
