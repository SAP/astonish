#!/usr/bin/env bash
# Strict Bot release acceptance. Never loads .env or provisions infrastructure.
set -euo pipefail
set +x
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
command -v python3 >/dev/null 2>&1 || { echo 'BLOCKED: python3 required; NOT RELEASE READY' >&2; exit 2; }
exec python3 "$ROOT/tests/e2eboot/bots_acceptance_driver.py" "$@"
