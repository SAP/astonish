#!/usr/bin/env bash
# 03 — Victim lists their own containers (baseline).
# Picks the first running (else first) container and stores its name.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "03  Victim lists own containers (baseline)"
require_state victim.json

body="$(api victim GET /api/sandbox/containers)"
print_http
echo "${body}" | jq .
echo "${body}" > "${STATE_DIR}/victim-containers.json"

http="$(last_http)"
if [[ "${http}" != "200" ]]; then
  echo
  echo "ERROR: victim could not list containers (HTTP ${http})." >&2
  echo >&2
  if echo "${body}" | grep -qi 'Incus is not installed'; then
    echo "This is the Incus-only handler on a non-Incus host." >&2
    echo "GET /api/sandbox/containers calls sandboxConnect() → Incus." >&2
    echo "On Kubernetes/OpenShell it returns 503 BEFORE NewSessionRegistry()" >&2
    echo "and before any tenant check. The reported takeover path cannot" >&2
    echo "run on this deployment." >&2
    echo >&2
    echo "Run ./00-check-backend.sh (after 01) for status/details." >&2
    echo "See README.md section \"Kubernetes / OpenShell hosts\"." >&2
  else
    echo "Is sandbox enabled? Unexpected error body is above." >&2
  fi
  exit 1
fi

# Prefer a running container; fall back to the first listed one.
container="$(echo "${body}" | jq -r '
  (.containers // [])
  | (map(select(.status == "running")) + .)
  | .[0].name // empty
')"

if [[ -z "${container}" ]]; then
  echo
  echo "ERROR: victim has no sandbox containers." >&2
  echo "Log into Studio as ${VICTIM_EMAIL}, start a chat/session that" >&2
  echo "creates a sandbox, wait until it is running, then re-run this script." >&2
  exit 1
fi

echo "${container}" > "${STATE_DIR}/victim-container-name"
echo
echo "  recorded victim container: ${container}"
echo "  saved: ${STATE_DIR}/victim-container-name"
echo
echo "Next: ./04-attacker-list-containers.sh"
