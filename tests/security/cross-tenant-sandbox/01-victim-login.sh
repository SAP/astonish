#!/usr/bin/env bash
# 01 — Log in as the victim (Team B).
# Saves the CLI access token to .state/victim.json.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "01  Login as victim (Team B)"
require_var VICTIM_EMAIL
require_var VICTIM_PASSWORD

login victim "${VICTIM_EMAIL}" "${VICTIM_PASSWORD}" "${VICTIM_ORG:-}" "${VICTIM_TEAM:-}"

echo
echo "Next: start a sandbox session as this user in Studio (chat with sandbox"
echo "enabled, or any flow that creates a container), then run:"
echo "  ./02-attacker-login.sh"
