#!/usr/bin/env bash
# Optional cleanup. Unexpose the port the attacker opened (as the victim),
# then wipe local tokens. Does NOT delete the container unless you also ran 08.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "09  Cleanup"

if [[ -f "${STATE_DIR}/victim.json" && -f "${STATE_DIR}/victim-container-name" && -f "${STATE_DIR}/exposed-port" ]]; then
  container="$(cat "${STATE_DIR}/victim-container-name")"
  port="$(cat "${STATE_DIR}/exposed-port")"
  echo "→ victim unexposing ${container} port ${port}"
  body="$(api victim DELETE "/api/sandbox/containers/${container}/expose/${port}" || true)"
  print_http
  echo "${body}" | jq . 2>/dev/null || echo "${body}"
else
  echo "  skip unexpose (no recorded container/port)"
fi

echo
echo "→ removing local tokens and response dumps in ${STATE_DIR}"
rm -rf "${STATE_DIR}"
mkdir -p "${STATE_DIR}"
echo "  done. .env is left in place."
