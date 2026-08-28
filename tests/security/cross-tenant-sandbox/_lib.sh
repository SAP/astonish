#!/usr/bin/env bash
# Shared helpers for the cross-tenant sandbox repro.
# Sourced by every numbered script. Do not run this file directly.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STATE_DIR="${SCRIPT_DIR}/.state"
ENV_FILE="${SCRIPT_DIR}/.env"

mkdir -p "${STATE_DIR}"

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "ERROR: required command '$1' is not installed." >&2
    exit 1
  }
}

need_cmd curl
need_cmd jq
need_cmd python3

if [[ ! -f "${ENV_FILE}" ]]; then
  echo "ERROR: ${ENV_FILE} is missing." >&2
  echo "Copy env.example and fill in the two accounts:" >&2
  echo "  cp env.example .env" >&2
  exit 1
fi

# shellcheck disable=SC1090
set -a
source "${ENV_FILE}"
set +a

BASE_URL="${BASE_URL%/}"
EXPOSE_PORT="${EXPOSE_PORT:-8080}"

if [[ -z "${BASE_URL}" ]]; then
  echo "ERROR: BASE_URL is empty in .env" >&2
  exit 1
fi

require_var() {
  local name="$1"
  if [[ -z "${!name:-}" ]]; then
    echo "ERROR: ${name} is empty in .env" >&2
    exit 1
  fi
}

json_get() {
  # json_get <file> <jq-filter>
  jq -er "$2" "$1"
}

save_json() {
  # save_json <path>  — reads stdin
  python3 -c 'import json,sys; json.dump(json.load(sys.stdin), open(sys.argv[1],"w"), indent=2)' "$1"
}

login() {
  # login <role> <email> <password> [org] [team]
  # Writes ${STATE_DIR}/${role}.json with access_token, user, org, team.
  local role="$1"
  local email="$2"
  local password="$3"
  local org="${4:-}"
  local team="${5:-}"
  local out="${STATE_DIR}/${role}.json"
  local body

  body="$(jq -n \
    --arg email "${email}" \
    --arg password "${password}" \
    --arg org "${org}" \
    --arg team "${team}" \
    '{
      email: $email,
      password: $password,
      client_type: "cli"
    }
    + (if $org  != "" then {org:  $org}  else {} end)
    + (if $team != "" then {team: $team} else {} end)')"

  echo "→ logging in as ${role} (${email}) at ${BASE_URL}"
  local resp http_code
  resp="$(mktemp)"
  http_code="$(
    curl -sS -o "${resp}" -w "%{http_code}" \
      -X POST "${BASE_URL}/api/auth/login" \
      -H "Content-Type: application/json" \
      -d "${body}"
  )"

  if [[ "${http_code}" != "200" ]]; then
    echo "ERROR: login failed for ${role} (HTTP ${http_code})" >&2
    cat "${resp}" >&2
    echo >&2
    rm -f "${resp}"
    exit 1
  fi

  if ! jq -e '.access_token' "${resp}" >/dev/null; then
    echo "ERROR: login response for ${role} has no access_token." >&2
    echo "The server must honour client_type=cli (tokens in the JSON body)." >&2
    cat "${resp}" >&2
    echo >&2
    rm -f "${resp}"
    exit 1
  fi

  jq '{
    access_token: .access_token,
    refresh_token: .refresh_token,
    user: .user,
    org: .org,
    team: (.team // ""),
    available_orgs: (.available_orgs // []),
    available_teams: (.available_teams // [])
  }' "${resp}" > "${out}"
  rm -f "${resp}"

  echo "  user  : $(jq -r '.user.email' "${out}")  id=$(jq -r '.user.id' "${out}")  role=$(jq -r '.user.role' "${out}")"
  echo "  org   : $(jq -r '.org.slug' "${out}")"
  echo "  team  : $(jq -r '.team // "(jwt default)"' "${out}")"
  echo "  saved : ${out}"
}

token_for() {
  local role="$1"
  local file="${STATE_DIR}/${role}.json"
  if [[ ! -f "${file}" ]]; then
    echo "ERROR: ${file} not found. Run the login script for ${role} first." >&2
    exit 1
  fi
  jq -er '.access_token' "${file}"
}

team_for() {
  local role="$1"
  local override="$2"
  if [[ -n "${override}" ]]; then
    echo "${override}"
    return
  fi
  local file="${STATE_DIR}/${role}.json"
  if [[ -f "${file}" ]]; then
    jq -r '.team // empty' "${file}"
    return
  fi
  echo ""
}

api() {
  # api <role> <method> <path> [curl extra args...]
  # Prints the response body. HTTP status is written to ${STATE_DIR}/last_http_code.
  local role="$1"
  local method="$2"
  local path="$3"
  shift 3
  local token team header_team
  token="$(token_for "${role}")"
  case "${role}" in
    victim)   team="$(team_for victim   "${VICTIM_TEAM:-}")" ;;
    attacker) team="$(team_for attacker "${ATTACKER_TEAM:-}")" ;;
    *)        team="" ;;
  esac

  header_team=()
  if [[ -n "${team}" ]]; then
    header_team=(-H "X-Astonish-Team: ${team}")
  fi

  local resp
  resp="$(mktemp)"
  local http_code
  http_code="$(
    curl -sS -o "${resp}" -w "%{http_code}" \
      -X "${method}" \
      "${BASE_URL}${path}" \
      -H "Authorization: Bearer ${token}" \
      -H "Content-Type: application/json" \
      -H "X-Requested-With: XMLHttpRequest" \
      "${header_team[@]}" \
      "$@"
  )"
  echo "${http_code}" > "${STATE_DIR}/last_http_code"
  cat "${resp}"
  rm -f "${resp}"
}

last_http() {
  cat "${STATE_DIR}/last_http_code" 2>/dev/null || echo "000"
}

print_http() {
  echo "  HTTP $(last_http)"
}

require_state() {
  local name="$1"
  if [[ ! -f "${STATE_DIR}/${name}" ]]; then
    echo "ERROR: missing ${STATE_DIR}/${name}." >&2
    echo "Run the earlier numbered scripts in order." >&2
    exit 1
  fi
}

banner() {
  echo
  echo "================================================================"
  echo " $1"
  echo "================================================================"
}

verdict() {
  # verdict VULNERABLE|FIXED|INCONCLUSIVE "reason"
  local status="$1"
  local reason="$2"
  mkdir -p "${STATE_DIR}"
  printf '%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${status}" "${reason}" >> "${STATE_DIR}/verdicts.log"
  echo
  echo "VERDICT: ${status}"
  echo "  ${reason}"
}
