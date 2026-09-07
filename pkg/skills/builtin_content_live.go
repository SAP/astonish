package skills

// BuiltinInspectLiveSurface is the protocol for live session/sandbox/browser
// questions. Keep this short: the agent already has the tools; this teaches
// how to treat evidence so a PATH probe is not mistaken for a missing overlay.
const BuiltinInspectLiveSurface = `# Inspect a live surface

Use this when the user asks whether something is installed or working **in a running session** (sandbox, overlay, container, browser, CDP, "in the session", ` + "`shell_command`" + ` probes).

This is **not** ` + "`debug-regression`" + `. Do not start with ` + "`git log`" + ` / ` + "`git blame`" + `. Do not conclude the overlay is unmounted because a Debian package name is missing from PATH.

## 1. Identify the product path

The capability the user asked about has a product launch path (nested ` + "`AGENTS.md`" + `, architecture docs, codegraph, this skill). Use that path.

Sandbox browser is **CloakBrowser**, not Debian ` + "`chromium`" + `:

- Binary: ` + "`/home/browser/.cloakbrowser/*/chrome`" + ` (or ` + "`HOME=/home/browser python3 -c 'from cloakbrowser.config import get_binary_path; print(get_binary_path())'`" + `)
- ` + "`which chromium`" + ` / ` + "`which google-chrome`" + ` / ` + "`dpkg -l | grep chrom`" + ` staying empty is **expected**
- To prove the browser works, call ` + "`browser_navigate`" + `. Do not ` + "`apt-get install chromium`" + `.

## 2. Inspect the live session

Through the same ` + "`shell_command`" + ` / ` + "`astonish-shell`" + ` the user has:

1. Overlay: ` + "`findmnt /sandbox/rootfs`" + ` or ` + "`mount | grep overlay`" + `
2. Layer chain: ` + "`echo $ASTONISH_LAYER_CHAIN`" + `
3. Product binary exists and is executable
4. Session record: container name **or** pod name (Docker records ` + "`container_name`" + `; Kubernetes records ` + "`pod_name`" + `)
5. Process: is the session container actually running?

## 3. Use the product tool

- Browser → ` + "`browser_navigate`" + ` to a public URL (or ` + "`localhost`" + ` for in-container services)
- Overlay workspace → ` + "`ls`" + ` / a known file from the rebuilt layer
- Session → ` + "`docker ps`" + ` / backend session list showing ` + "`astonish-session-*`" + `

## 4. Only then conclude missing

If the **product** path is absent (CloakBrowser directory missing, overlay not mounted, no session container), report that. Lead with what **is** true (overlay mounted, python present, CloakBrowser at X) before saying what is absent.

Do not open with "No, Chromium is not available."
`

// BuiltinVerifyLiveWithDrill teaches drills as the preferred behavior verify
// for running surfaces. Composition is LLM-authored; execution is mechanical.
const BuiltinVerifyLiveWithDrill = `# Verify live with a drill

Use this when the outcome is a **running surface**: sandbox session, overlay, in-container browser, daemon, CLI, Studio UI. ` + "`go test ./pkg/...`" + ` of library code is not proof that a container exists.

Astonish drills are the native live harness: the agent composes YAML, the runner replays it without an LLM.

## When a drill is the verify command

- Plan phase ` + "`verify_kind=behavior`" + ` on ` + "`pkg/sandbox/`" + `, ` + "`pkg/browser/`" + `, ` + "`pkg/daemon/`" + `, ` + "`pkg/api/`" + `, ` + "`cmd/`" + `, ` + "`web/`" + `
- User-visible outcome: "session containers run", "browser_navigate works", "overlay has CloakBrowser"

Prefer ` + "`run_drill`" + ` (or a small inline drill the runtime can exec) over ` + "`go test`" + ` / ` + "`go build`" + ` as that phase's ` + "`verify`" + `.

## Minimal sandbox-browser smoke

Compose (or reuse) a drill that asserts, mechanically:

1. Overlay is mounted at ` + "`/sandbox/rootfs`" + ` (shell: ` + "`findmnt`" + ` / ` + "`mount`" + `)
2. CloakBrowser chrome exists and is executable under ` + "`/home/browser/.cloakbrowser`" + `
3. ` + "`browser_navigate`" + ` to a public URL succeeds (not ` + "`which chromium`" + `)
4. Optional: session container name starts with ` + "`astonish-session-`" + `

Then ` + "`validate_drill`" + ` and ` + "`run_drill`" + `. If no drill exists yet, compose a minimal one — do not skip live proof because writing YAML feels extra.

## What a drill is not

- Not a substitute for unit tests of library logic (` + "`debug-regression`" + ` still owns those)
- Not ` + "`apt-get install chromium`" + `
- Not a screenshot of the Studio UI claiming the sandbox is up
`

// BuiltinWatchLongRunning maps long rebuilds/servers onto process_* tools.
// Starting the job is not done.
const BuiltinWatchLongRunning = `# Watch long-running work

Use this before you start, watch, or report on anything that keeps running after you launch it: base-layer rebuilds, docker builds, captures, ` + "`make`" + `, dev servers, SSE waits.

**Starting the job is not done.** Done is: the process exited successfully **and** the expected artifact exists (layer id on disk, ` + "`astonish-session-*`" + ` container, listening port, drill pass).

## How to watch (Astonish tools)

1. Launch with ` + "`shell_command(command, background=true)`" + `
2. Read output with ` + "`process_read`" + ` — heartbeats and logs, not a tight LLM sleep loop
3. List with ` + "`process_list`" + `; stop with ` + "`process_kill`" + ` only when replacing or finishing
4. On stall: read the log **before** retrying the same install step. A 504, ENOSPC, or SIGILL is a different bug than "apt is slow"

## Definition of done

- Exit code 0 **and** the artifact the user can check (layer id, container, URL, file)
- If the stream drops, poll the status endpoint / process list before claiming failure
- If verify is a 10-minute ` + "`bash -c`" + ` that cannot cover a 30-minute rebuild, watch the job with ` + "`process_*`" + ` and use a short drill afterward to prove the artifact

## Do not

- Claim "stuck" with no log tail
- Busy-wait by calling ` + "`shell_command(sleep N)`" + ` in a loop
- Mark a plan phase complete because the command was *started*
`
