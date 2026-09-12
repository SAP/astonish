#!/usr/bin/env bash
# Exercise OAuth discovery, client-credentials authentication, MCP initialize,
# and tools/list without logging the client secret or access token.
set -euo pipefail

base_url="${ASTONISH_MCP_BASE_URL:-http://127.0.0.1:9393}"
client_id="${ASTONISH_MCP_CLIENT_ID:?Set ASTONISH_MCP_CLIENT_ID}"
client_secret="${ASTONISH_MCP_CLIENT_SECRET:?Set ASTONISH_MCP_CLIENT_SECRET}"
resource="${base_url%/}/api/mcp"
metadata_url="${base_url%/}/.well-known/oauth-protected-resource/api/mcp"

require_python() {
  command -v python3 >/dev/null || {
    printf '%s\n' 'error: python3 is required to parse JSON responses' >&2
    exit 1
  }
}

json_field() {
  python3 -c 'import json, sys; print(json.load(sys.stdin)[sys.argv[1]])' "$1"
}

require_python

metadata="$(curl --fail --silent --show-error --max-time 15 "$metadata_url")"
metadata_resource="$(printf '%s' "$metadata" | json_field resource)"
if [[ "$metadata_resource" != "$resource" ]]; then
  printf 'error: discovery resource mismatch: expected %s, got %s\n' "$resource" "$metadata_resource" >&2
  exit 1
fi
printf 'PASS discovery: %s\n' "$metadata_url"

token_response="$({
  curl --fail --silent --show-error --max-time 15 \
    --user "$client_id:$client_secret" \
    --data-urlencode 'grant_type=client_credentials' \
    --data-urlencode 'scope=tool:execute' \
    --data-urlencode "resource=$resource" \
    "${base_url%/}/oauth/token"
} )"
access_token="$(printf '%s' "$token_response" | json_field access_token)"
if [[ -z "$access_token" ]]; then
  printf '%s\n' 'error: OAuth token response did not contain an access token' >&2
  exit 1
fi
printf '%s\n' 'PASS OAuth client-credentials token'

post_mcp() {
  local payload="$1"
  curl --fail --silent --show-error --max-time 15 \
    --request POST "$resource" \
    --header "Authorization: Bearer $access_token" \
    --header 'Content-Type: application/json' \
    --header 'Accept: application/json, text/event-stream' \
    --header 'MCP-Protocol-Version: 2025-03-26' \
    --data "$payload"
}

initialize="$(post_mcp '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"astonish-curl-smoke","version":"1.0"}}}')"
printf '%s' "$initialize" | python3 -c '
import json, sys
body = json.load(sys.stdin)
result = body.get("result", {})
if result.get("serverInfo", {}).get("name") != "astonish":
    raise SystemExit("error: initialize did not identify the Astonish MCP server")
if result.get("protocolVersion") != "2025-03-26":
    raise SystemExit("error: MCP protocol negotiation failed")
'
printf '%s\n' 'PASS MCP initialize'

tools="$(post_mcp '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}')"
printf '%s' "$tools" | python3 -c '
import json, sys
body = json.load(sys.stdin)
names = {tool["name"] for tool in body.get("result", {}).get("tools", [])}
expected = {"get_agent_context", "search_tools", "describe_tools", "execute_tool"}
if names != expected:
    raise SystemExit(f"error: expected exactly {sorted(expected)}, got {sorted(names)}")
'
printf '%s\n' 'PASS MCP tools/list: exactly four progressive tools'
printf '%s\n' 'MCP OAuth smoke test passed.'
