#!/usr/bin/env bash
# Verifies the one-click install: bring the stack up, log in, create a proxy.
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

STATUS=$(curl -sk -b "$JAR" -o /tmp/smoke-create.html -w '%{http_code}' \
  -X POST https://localhost:8443/proxies \
  -d "name=smoke&port=14999&tls_domain=petrovich.ru")

# postCreate (internal/web/handlers_proxy.go) redirects with 303 See Other on
# success; any other status is a rejected or failed create.
if [ "$STATUS" != "303" ]; then
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
