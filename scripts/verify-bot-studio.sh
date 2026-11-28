#!/usr/bin/env bash
# Run the complete Studio file-workspace acceptance from any working directory.
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "$repo_root"
: "${ASTONISH_BOT_BROWSER_PATH:?Set ASTONISH_BOT_BROWSER_PATH to an explicit browser executable}"
case "$ASTONISH_BOT_BROWSER_PATH" in
  /*) ;;
  *) printf '%s\n' 'ASTONISH_BOT_BROWSER_PATH must be absolute' >&2; exit 1 ;;
esac
if [[ ! -x "$ASTONISH_BOT_BROWSER_PATH" ]]; then
  printf 'Browser executable is not executable: %s\n' "$ASTONISH_BOT_BROWSER_PATH" >&2
  exit 1
fi
export ASTONISH_BOT_BROWSER_PATH
export ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1
printf 'Studio verification root: %s\nBrowser: %s\n' "$repo_root" "$ASTONISH_BOT_BROWSER_PATH"

npm --prefix web test -- \
  src/api/__tests__/botFiles.test.ts \
  src/api/__tests__/botEvents.test.ts \
  src/components/bots/__tests__/BotFilesPanel.test.tsx \
  src/components/bots/__tests__/BotWorkspace.test.tsx \
  src/components/chat/ArtifactDelivery.test.tsx \
  src/components/chat/EmbeddedFileViewer.test.tsx \
  src/components/__tests__/StudioChat.test.tsx \
  src/test/scenarios/harness-panel.test.tsx \
  src/test/scenarios/downloads-artifacts.test.tsx
npm --prefix web run build

browser_log="$(mktemp "${TMPDIR:-/tmp}/astonish-studio-browser.XXXXXX")"
trap 'rm -f "$browser_log"' EXIT
go test ./pkg/api -run '^TestBotFileWorkspaceBrowserAcceptance$' -count=1 -v -json -timeout=5m | tee "$browser_log"
# A zero exit with no matching test (or a skipped test) is not browser evidence.
node - "$browser_log" <<'NODE'
const fs = require('node:fs');
const events = fs.readFileSync(process.argv[2], 'utf8').trim().split('\n').filter(Boolean).map(JSON.parse);
const test = 'TestBotFileWorkspaceBrowserAcceptance';
for (const name of [test, `${test}/personal`, `${test}/team`]) {
  if (!events.some(e => e.Test === name && e.Action === 'pass')) {
    throw new Error(`Missing browser acceptance pass: ${name}`);
  }
}
if (events.some(e => e.Action === 'skip' || e.Action === 'fail')) {
  throw new Error('Browser acceptance failed or skipped');
}
console.log('Studio browser acceptance passed: personal and team; no skips.');
NODE
