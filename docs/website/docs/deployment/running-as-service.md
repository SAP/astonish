# Running as a Service

Astonish runs as a background daemon managed by your operating system's service manager. The daemon serves Studio at `http://localhost:9393`, handles scheduled flows, channel integrations, and persistent sessions. It starts automatically on boot and restarts on failure.

## Installation

The `daemon install` command registers Astonish with the appropriate service manager for your OS:

```bash
astonish daemon install
```

This creates:
- **macOS**: A launchd plist at `~/Library/LaunchAgents/dev.astonish.daemon.plist`
- **Linux**: A systemd user unit at `~/.config/systemd/user/astonish.service`

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9393` | HTTP port for Studio UI |

Example with custom port:

```bash
astonish daemon install --port 8080
```

## Starting and Stopping

```bash
# Start the daemon
astonish daemon start

# Stop the daemon
astonish daemon stop

# Restart the daemon
astonish daemon restart

# Check status
astonish daemon status
```

## Running in Foreground

For debugging, run the daemon in the foreground instead of as a background service:

```bash
astonish daemon run
```

This starts the server in the current terminal session. Local/default daemon mode writes application logs to the daemon log file so `astonish daemon logs` behaves the same as an installed service. To force logs to the terminal, set `ASTONISH_LOG_DEST=stdout`:

```bash
ASTONISH_LOG_DEST=stdout astonish daemon run
```

Container roles (`ASTONISH_MODE=api` or `ASTONISH_MODE=worker`) use stdout by default for Kubernetes and other orchestrators. Press `Ctrl+C` to stop a foreground run.

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9393` | HTTP port for Studio UI |

## Controlling Auto-Start

The daemon is configured to start automatically on login by default.

### macOS (launchd)

The plist includes `RunAtLoad: true`. To manually control:

```bash
# Disable auto-start
launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/dev.astonish.daemon.plist

# Re-enable
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.astonish.daemon.plist
```

### Linux (systemd)

The unit is enabled via `systemctl --user`. To manually control:

```bash
# Disable auto-start
systemctl --user disable astonish

# Re-enable
systemctl --user enable astonish
```

## Viewing Logs

```bash
# Show recent logs (default: last 50 lines)
astonish daemon logs

# Follow live logs
astonish daemon logs -f

# Show last 100 lines
astonish daemon logs -n 100
```

### OS-level log access

```bash
# macOS: view via system log
log show --predicate 'subsystem == "dev.astonish.daemon"' --last 1h

# Linux: view via journalctl
journalctl --user -u astonish -f
```

Log files are also written to `~/.local/share/astonish/logs/`.

## Uninstalling

Remove the daemon registration:

```bash
astonish daemon uninstall
```

This stops the service and removes the plist or systemd unit file.

## All Daemon Subcommands

| Command | Description |
|---------|-------------|
| `daemon install` | Register as a system service |
| `daemon uninstall` | Remove the system service |
| `daemon start` | Start the background service |
| `daemon stop` | Stop the background service |
| `daemon restart` | Restart the background service |
| `daemon status` | Show current daemon status |
| `daemon run` | Run in foreground (for debugging) |
| `daemon logs` | View daemon logs |

## See Also

- [Deployment Overview](./index.md) — choosing between local and cloud deployment
- [Kubernetes Deployment](./kubernetes.md) — cloud deployments use Kubernetes instead of the daemon
