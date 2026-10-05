#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Keep Go's existing module cache outside the disposable HOME. Some cached test
# fixtures are intentionally read-only and should not be copied into the temp tree.
gomodcache="$(go env GOMODCACHE)"
tmp="$(mktemp -d)"
port="${ASTONISH_VERIFY_PORT:-19393}"
ready_timeout="${ASTONISH_VERIFY_READY_TIMEOUT:-180}"
pid=""

cleanup() {
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    kill "$pid"
    wait "$pid" || true
  fi
  rm -rf "$tmp" || true
}
trap cleanup EXIT

export HOME="$tmp/home"
export GOMODCACHE="$gomodcache"
export ASTONISH_DATA_DIR="$tmp/data"
# OAuth settings inherited from the developer shell can require HTTPS and are
# unrelated to this local HTTP smoke test.
unset ASTONISH_OAUTH_RESOURCE ASTONISH_OAUTH_AUTHORIZATION_SERVERS ASTONISH_OAUTH_ISSUER
unset OAUTH_RESOURCE OAUTH_AUTHORIZATION_SERVERS OAUTH_ISSUER
mkdir -p "$HOME/.astonish" "$tmp/data"
cat >"$HOME/.astonish/config.yaml" <<EOF
storage:
  backend: sqlite
  sqlite:
    data_dir: $tmp/data
daemon:
  port: $port
EOF

cd "$root"
go build -o "$tmp/astonish" .
"$tmp/astonish" daemon run --port "$port" >"$tmp/daemon.log" 2>&1 &
pid=$!

ready=0
for _ in $(seq 1 "$ready_timeout"); do
  if curl --fail --silent "http://127.0.0.1:$port/api/healthz" >/dev/null; then
    ready=1
    break
  fi
  sleep 1
done
if [[ "$ready" != "1" ]]; then
  printf '%s\n' 'daemon did not become ready; log follows:' >&2
  cat "$tmp/daemon.log" >&2 || true
  exit 1
fi
curl --fail --silent "http://127.0.0.1:$port/api/healthz" >/dev/null

setup_status="$(curl --fail --silent "http://127.0.0.1:$port/api/auth/setup-status")"
printf '%s' "$setup_status" | grep -q '"auth_mode":"none"'

me="$(curl --fail --silent "http://127.0.0.1:$port/api/auth/me")"
printf '%s' "$me" | grep -q '"email":"local@astonish.local"'

status="$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:$port/api/studio/sessions")"
[[ "$status" == "200" ]]

foreign_origin_status="$(curl --silent --output /dev/null --write-out '%{http_code}' -X POST -H 'Origin: http://evil.example.com' -H 'Content-Type: application/json' "http://127.0.0.1:$port/api/studio/sessions")"
[[ "$foreign_origin_status" == "403" ]]

local_origin_status="$(curl --silent --output /dev/null --write-out '%{http_code}' -X POST -H "Origin: http://localhost:$port" -H 'Content-Type: application/json' "http://127.0.0.1:$port/api/studio/sessions")"
[[ "$local_origin_status" != "403" ]]
