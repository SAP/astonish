#!/usr/bin/env bash
# 00 — Probe the platform sandbox backend.
# Confirms auth works and tells you whether the reported Incus-only
# container APIs can even run here.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "00  Probe sandbox backend"
require_state victim.json

echo "→ GET /api/sandbox/status"
status_body="$(api victim GET /api/sandbox/status)"
print_http
echo "${status_body}" | jq . 2>/dev/null || echo "${status_body}"
echo "${status_body}" > "${STATE_DIR}/sandbox-status.json"

echo
echo "→ GET /api/sandbox/details"
details_body="$(api victim GET /api/sandbox/details)"
print_http
echo "${details_body}" | jq . 2>/dev/null || echo "${details_body}"
echo "${details_body}" > "${STATE_DIR}/sandbox-details.json"

backend="$(echo "${status_body}" | jq -r '.backend // .platform // empty' 2>/dev/null || true)"
platform="$(echo "${status_body}" | jq -r '.platform // empty' 2>/dev/null || true)"
enabled="$(echo "${status_body}" | jq -r '.sandboxEnabled // .sandbox_enabled // empty' 2>/dev/null || true)"
runtime="$(echo "${status_body}" | jq -r '.runtimeAvailable // .runtime_available // empty' 2>/dev/null || true)"

echo
echo "  sandboxEnabled : ${enabled}"
echo "  backend        : ${backend}"
echo "  platform       : ${platform}"
echo "  runtimeAvailable: ${runtime}"

case "${backend}|${platform}" in
  *k8s*|*kubernetes*)
    echo "${backend:-k8s}" > "${STATE_DIR}/backend-kind"
    echo
    echo "This host is Kubernetes, not Incus."
    echo
    echo "The reported handlers (GET/DELETE /api/sandbox/containers,"
    echo "expose, pin, proxy) all call sandboxConnect() → Incus."
    echo "On K8s they return HTTP 503 before any tenant lookup runs."
    echo
    echo "You CAN still run 03+ to capture that 503 as evidence that"
    echo "the reported path is Incus-only. You CANNOT take over another"
    echo "team's K8s pod through these endpoints."
    echo
    echo "Next: ./03-victim-list-containers.sh   # expect HTTP 503"
    ;;
  *openshell*)
    echo "openshell" > "${STATE_DIR}/backend-kind"
    echo
    echo "This host is OpenShell. Same situation as K8s: the reported"
    echo "container APIs are Incus-only and will 503."
    echo
    echo "Next: ./03-victim-list-containers.sh   # expect HTTP 503"
    ;;
  *)
    echo "${backend:-incus}" > "${STATE_DIR}/backend-kind"
    echo
    echo "Backend looks like Incus. The reported chain can be simulated."
    echo
    echo "Next: start a sandbox as the victim in Studio, then:"
    echo "  ./03-victim-list-containers.sh"
    ;;
esac
