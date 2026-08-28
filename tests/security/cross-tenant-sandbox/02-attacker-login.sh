#!/usr/bin/env bash
# 02 — Log in as the attacker (Team A).
# Saves the CLI access token to .state/attacker.json.
set -euo pipefail
# shellcheck source=_lib.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_lib.sh"

banner "02  Login as attacker (Team A)"
require_var ATTACKER_EMAIL
require_var ATTACKER_PASSWORD

login attacker "${ATTACKER_EMAIL}" "${ATTACKER_PASSWORD}" "${ATTACKER_ORG:-}" "${ATTACKER_TEAM:-}"

victim_file="${STATE_DIR}/victim.json"
if [[ -f "${victim_file}" ]]; then
  v_org="$(jq -r '.org.slug // empty' "${victim_file}")"
  v_team="$(jq -r '.team // empty' "${victim_file}")"
  a_org="$(jq -r '.org.slug // empty' "${STATE_DIR}/attacker.json")"
  a_team="$(jq -r '.team // empty' "${STATE_DIR}/attacker.json")"
  echo
  echo "  victim   org=${v_org}  team=${v_team:-"(jwt default)"}"
  echo "  attacker org=${a_org}  team=${a_team:-"(jwt default)"}"
  if [[ -n "${v_team}" && -n "${a_team}" && "${v_team}" == "${a_team}" && "${v_org}" == "${a_org}" ]]; then
    echo
    echo "WARNING: attacker and victim resolved to the SAME org+team."
    echo "This repro is only meaningful across two different teams."
    echo "Set VICTIM_TEAM / ATTACKER_TEAM in .env to distinct slugs."
  fi
fi

echo
echo "Next: ./03-victim-list-containers.sh"
