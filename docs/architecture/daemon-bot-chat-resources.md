# Daemon Bot / Chat resource ownership

`newProductionBotWorker` accepts borrowed `launcher.ChatCatalogResources` plus the
existing turn-authorization registry. It does not create a sandbox backend or a
pool and its release callback never cleans up the borrowed pool. `Run` obtains
these from the **Studio** factory's `WithStudioChatInitialized` callback, then
constructs the worker only after successful synchronous `PreWarm`, before HTTP
serve handoff. The optional channel factory is not the resource source: it is
created later in the background and may never exist when channels are disabled.
Default and worker modes install Home/interactive adapters and scheduler callbacks
before starting the scheduler loop. API-only mode starts no Bot worker and borrows
Studio's backend for retained-file reads without allocating another pool/catalog.
If Studio's sandbox resources are absent, execution fails closed; only the offline
retained-file adapter may construct a storage-only backend from explicit config.
The callback is consumed once for startup, never used to start workers on settings
Reset or from request goroutines; Studio retains resource lifecycle ownership.
This startup regression test does not certify pool rebinding across a later Studio
settings Reset, external model execution, or retained-container provisioning.

Each production attempt supplies Chat configuration and the same scoped catalog
resources for interactive, autonomous and research turns. Generic filesystem,
shell, process, browser, workflow and drill registrations belong to shared Chat,
not a daemon Bot-specific recipe. Bot-specific publication, task workspace and
configured-research provenance/quota hooks remain extensions. The configured
Perplexity executable is constructed exactly once by `chatruntime.Inputs`, not
registered again as `bot_research`. `botSharedDispatch` attaches the admitted
research guard to that captured concrete executable's continuation: strict current
route/fingerprint and mandate checks, episode serialization, and one durable
public-research reservation remain intact. The continuation uses Chat's existing
budgeted provider factory, so there is no second model reservation. Native query
arguments may narrow, never expand, the admitted result ceiling. The confined MCP
research alias remains separate from qualified MCP tool identities; MCP execution
is not granted by a bare sibling name. The legacy confined search helper remains
available to acceptance harnesses, but is not a Perplexity production registration.

Preparation pins the Home binding and template on `HomeSessionID`, then aliases
the run ID to that lazy client. It no longer performs an eager tool-client
readiness call or shell `$HOME` probe merely to construct a catalog. The first
authorized sandbox tool is responsible for readiness through Chat's node path.
Attempt cleanup does not remove that alias's owner, destroy the session, or
close the shared pool. Existing title-only admission remains a separate
question-only startup path; this change does not make views or polls run tools.

This ownership change is not a policy grant. Live owner membership, the run
lease, mandates, budgets, dispatch authorization and fenced publication still
apply. Shared ordinary file writes must not inherit the old confined-file
adapter's name-only authorization.

## Verification boundary

`TestDaemonBotStartupOrder` launches real `Run` subprocesses with temporary XDG/HOME,
SQLite databases, ephemeral loopback ports and a controlled embedding HTTP provider.
It proves Studio pre-warm → Bot worker → HTTP ready → clean shutdown for default
and worker modes, with channels enabled/disabled, and no worker in API mode.
`ASTONISH_STARTUP_TEST_BINARY=/absolute/temp/astonish` selects the compiled
`daemon run` CLI instead of the test-binary helper. No existing Bot is replayed,
no generative provider is called, and no compute session is allocated by the test.
Enabled Telegram uses no token and cannot contact its external API.

`TestProductionBotWorkerBorrowsChatResources` checks exact backend/pool identity
and that worker release leaves the same lazy client accessible. The isolated
HTTP title, authorized-schedule and employee-operation fixtures run the actual
worker with temporary SQLite and scripted models; they are not Docker,
Kubernetes or external-provider evidence. Physical retained-session and browser
coverage must be recorded independently using test-owned infrastructure.
