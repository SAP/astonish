# Studio Overview

Studio is Astonish's web-based UI, served locally at `http://localhost:9393`. It provides a visual interface for chatting with agents, creating presentations, designing flows, managing fleet operations, and previewing generated applications.

## Main Tabs

| Tab | Purpose |
|-----|---------|
| **Chat** | Interactive agent conversations with streaming responses |
| **Flows** | Visual flow designer for multi-step agent pipelines |
| **Fleet** | Multi-agent plan dashboard and coordination |
| **Drill** | Test suites for agent validation and quality assurance |
| **Apps** | Preview and manage generated applications |
| **Slides** | Manage presentations, templates, versions, sharing, and exports |

## Launching Studio

Studio is served automatically by the daemon. Start the daemon and open your browser:

```bash
astonish daemon start
# Studio available at http://localhost:9393
```

Or run in the foreground for debugging:

```bash
astonish daemon run
# Studio available at http://localhost:9393 (logs printed to stdout)
```

## Cloud Deployment Features

When running with [PostgreSQL](../platform/), Studio gains additional capabilities:

### Login

Studio presents a login screen requiring authentication. Users authenticate with credentials managed by the platform identity system.

### Team Switching

After login, the top navigation shows your current organization and team. Click to switch between teams you have access to. All agent interactions — chat sessions, flows, and fleet plans — are scoped to the active team.

## Settings

The [Settings](./settings.md) panel is accessible from the gear icon in the navigation. It provides configuration for providers, MCP servers, credentials, and platform administration.

## Session Persistence

Chat sessions and flow executions persist across browser refreshes. Studio stores session data server-side, so you can close the browser and resume where you left off.

## Related Pages

- [Chat Interface](./chat.md)
- [Slides](../slides/)
- [Flow Editor](./flow-editor.md)
- [Settings](./settings.md)
- [Running & Debugging](./running-debugging.md)
- [Keyboard Shortcuts](./keyboard-shortcuts.md)
