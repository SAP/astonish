# cmd/astonish — AGENTS.md

CLI dispatch (Cobra). `main.go` calls `astonish.Execute()` here.

## Scope
- `root.go` — `Execute()`: top-level command switch (`chat`, `daemon`, `login`, `logout`, `flows`, `platform`, `sandbox`, `memory`, `scheduler`, `tap`, `skills`, `setup`, `status`, `version`, …). Also holds `mustBeRemote` / `mustNotBeRemote` gating.
- One file per top-level command: `chat.go`, `daemon.go`, `login.go`, `flows.go`, `platform.go`, `sandbox.go`, `memory.go`, …
- `sandbox_backends.go` — blank imports that guarantee the k8s/openshell/mock backend packages link into the binary.

## Key rules
1. **Local vs. remote mode is enforced via `mustBeRemote` / `mustNotBeRemote`.** Some commands only make sense against a remote daemon (skills/org management), some only make sense locally (daemon, sandbox, memory). Preserve the gating when adding new commands.
2. **Commands are thin.** They parse flags and delegate to `pkg/launcher` / `pkg/daemon` / etc. Do not put business logic in `cmd/astonish/*.go`.
3. **`chat` command flow**: always platform-backed. `handleChatCommand` requires `client.IsRemoteMode()` (login), then `launcher.RunChatTUI` (Studio SSE). There is no in-process personal chat path.
4. **`daemon` subcommands**: `run` (foreground), `install`/`start`/`stop` (launchd/systemd service). `daemon run` is what powers Studio.

## When editing
1. Adding a new command? Create `foo.go` here with a `handleFooCommand` function and register it in the `root.go` switch. Add `mustBeRemote` / `mustNotBeRemote` as appropriate.
2. Adding a sandbox backend? Add a blank import in `sandbox_backends.go` — otherwise the backend package won't link.

## References
- `pkg/launcher/AGENTS.md` — where `chat` and `daemon run` land.
