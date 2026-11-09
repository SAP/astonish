# Retained Bot execution working directory

Docker and direct Kubernetes retained Bot sessions default buffered, PTY, and
streaming execution to `/root`, inside the composed overlay. An explicit `WorkDir`
takes precedence. Ordinary sessions (without a retained home binding) retain their
existing cwd behavior; OpenShell is unchanged.

The backend uses the immutable retained-home binding, not Chat mode or a
caller-supplied `HOME`, to select this behavior. It checks the overlay and home
mounts, enters `chroot`, and only then changes directory and executes the command.
Changing Docker's `-w` or doing `cd` before `astonish-shell` would not work:
`chroot` resets cwd to `/`.

Browser and MCP launch paths already invoke `Backend.ExecStreaming`, sometimes
with an `astonish-shell` prefix. The retained command builder removes this prefix
to avoid a second chroot and applies the same cwd rule. This works with existing
sandbox-base images; no image or wrapper rebuild is required. Missing mounts and
failed directory changes fail closed rather than executing in the outer container.

`TestBotHomeDockerPersistenceLive` verifies buffered relative writes, streaming
relative writes through the browser/MCP-style wrapper, explicit `/tmp` cwd, and
retention of `/root` markers after destroying and recreating the physical run.
Kubernetes unit coverage verifies command selection; this Docker acceptance does
not establish live Kubernetes or browser-navigation acceptance.
