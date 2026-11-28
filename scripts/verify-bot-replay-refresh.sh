#!/usr/bin/env bash
# Strict, isolated production-HTTP + built-Studio acceptance. Never skip/download.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
: "${ASTONISH_BOT_BROWSER_PATH:?Set ASTONISH_BOT_BROWSER_PATH to an explicit Chrome executable}"
if [[ ! -f "$ASTONISH_BOT_BROWSER_PATH" || ! -x "$ASTONISH_BOT_BROWSER_PATH" ]]; then
  printf 'Browser executable unavailable: %s\n' "$ASTONISH_BOT_BROWSER_PATH" >&2
  exit 1
fi
npm --prefix web run build
results="$(mktemp "${TMPDIR:-/tmp}/astonish-bot-browser.XXXXXX")"
trap 'rm -f "$results"' EXIT
go test ./pkg/api -count=1 -timeout=3m -run '^(TestBotApprovalReplayBrowserAcceptance|TestBotFilePaginationBrowserAcceptance)$' -json | tee "$results"
python3 - "$results" <<'PY'
import json
import sys

roots = ('TestBotApprovalReplayBrowserAcceptance', 'TestBotFilePaginationBrowserAcceptance')
required = {name + suffix for name in roots for suffix in ('', '/personal', '/team')}
passed = set()
running = set()
with open(sys.argv[1], encoding='utf-8') as stream:
    for line in stream:
        event = json.loads(line)
        name, action = event.get('Test'), event.get('Action')
        if action in ('skip', 'fail'):
            raise SystemExit(f'Strict browser gate rejected {action}: {name or event.get("Package")}')
        if name:
            if name not in required:
                raise SystemExit(f'Unexpected test in strict gate: {name}')
            if action == 'run':
                running.add(name)
            elif action == 'pass':
                passed.add(name)
missing = required - (passed & running)
if missing:
    raise SystemExit('Missing executed passes (zero matches/skips forbidden): ' + ', '.join(sorted(missing)))
print('Strict built-SPA gate: replay and file pagination personal/team all executed and passed; no skips.')
PY
