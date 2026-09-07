package astonish

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	sboxdocker "github.com/SAP/astonish/pkg/sandbox/docker"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/charmbracelet/huh"
)

func handleSandboxCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printSandboxUsage()
		return nil
	}

	// k8s-smoke is a Phase-D manifest + API smoke probe that runs
	// against a remote Kubernetes API. It uses kubeconfig/RBAC for
	// auth and never touches host mounts / Incus sockets, so it must
	// NOT go through the Linux sudo escalation path below; doing so
	// would either drop the user's KUBECONFIG / HOME settings or just
	// fail on non-root CI where sudo is unavailable.
	if args[0] == "k8s-smoke" {
		return handleSandboxK8sSmoke(args[1:])
	}

	// Docker session containers do not need host overlay mounts or Incus
	// sockets. Skip sudo for status/init; remaining Incus-era subcommands
	// still escalate on Linux until they are rewritten.
	switch args[0] {
	case "status", "init", "list", "ls", "create", "shell", "destroy", "rm", "prune":
	default:
		if sandbox.NeedsEscalation() {
			return sandbox.Escalate()
		}
	}

	switch args[0] {
	case "status":
		return handleSandboxStatus()
	case "init":
		return handleSandboxInit()
	case "list", "ls":
		return handleSandboxList()
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox create <template> [--name <label>]")
		}
		name := ""
		for i, a := range args[2:] {
			if (a == "--name" || a == "-n") && i+1 < len(args[2:]) {
				name = args[2:][i+1]
			}
		}
		return handleSandboxCreate(args[1], name)
	case "expose":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox expose <container> <port> [<port>...]\n       astonish sandbox expose <container> --list")
		}
		// Check for --list flag
		for _, a := range args[2:] {
			if a == "--list" || a == "-l" {
				return handleSandboxExposeList(args[1])
			}
		}
		if len(args) < 3 {
			return fmt.Errorf("usage: astonish sandbox expose <container> <port> [<port>...]")
		}
		return handleSandboxExpose(args[1], args[2:])
	case "unexpose":
		if len(args) < 3 {
			return fmt.Errorf("usage: astonish sandbox unexpose <container> <port> [<port>...]")
		}
		return handleSandboxUnexpose(args[1], args[2:])
	case "url":
		if len(args) < 3 {
			return fmt.Errorf("usage: astonish sandbox url <container> <port>")
		}
		return handleSandboxURL(args[1], args[2])
	case "refresh":
		return handleSandboxRefresh()
	case "destroy", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox destroy <session-id>")
		}
		return handleSandboxDestroy(args[1])
	case "shell":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox shell <session-id>")
		}
		return handleSandboxShell(args[1])
	case "cp":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox cp <session-id>:<container-path> [local-path]")
		}
		localPath := ""
		if len(args) >= 3 {
			localPath = args[2]
		}
		return handleSandboxCp(args[1], localPath)
	case "prune":
		return handleSandboxPrune()
	case "reset":
		return handleSandboxReset()
	case "save":
		if len(args) < 3 {
			return fmt.Errorf("usage: astonish sandbox save <session-id> <template-name> [--description \"...\"]")
		}
		description := ""
		for i, a := range args[3:] {
			if (a == "--description" || a == "-d") && i+1 < len(args[3:]) {
				description = args[3:][i+1]
			}
		}
		return handleSandboxSave(args[1], args[2], description)
	case "template", "tpl":
		return handleSandboxTemplateCommand(args[1:])
	default:
		printSandboxUsage()
		return fmt.Errorf("unknown sandbox subcommand: %s", args[0])
	}
}

func handleSandboxTemplateCommand(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printSandboxTemplateUsage()
		return nil
	}

	switch args[0] {
	case "list", "ls":
		return handleTemplateList()
	case "create":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox template create <name> [--description \"...\"]")
		}
		description := ""
		for i, a := range args[2:] {
			if a == "--description" || a == "-d" {
				if i+1 < len(args[2:]) {
					description = args[2:][i+1]
				}
			}
		}
		return handleTemplateCreate(args[1], description)
	case "shell":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox template shell <name>")
		}
		return handleTemplateShell(args[1])
	case "snapshot":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox template snapshot <name>")
		}
		return handleTemplateSnapshot(args[1])
	case "promote":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox template promote <name>")
		}
		return handleTemplatePromote(args[1])
	case "delete", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox template delete <name>")
		}
		return handleTemplateDelete(args[1])
	case "info":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish sandbox template info <name>")
		}
		return handleTemplateInfo(args[1])
	default:
		printSandboxTemplateUsage()
		return fmt.Errorf("unknown template subcommand: %s", args[0])
	}
}

func printSandboxUsage() {
	fmt.Println("usage: astonish sandbox {status,init,list,create,shell,save,reset,expose,unexpose,url,cp,refresh,destroy,prune,template} ...")
	fmt.Println("")
	fmt.Println("Manage session container isolation.")
	fmt.Println("")
	fmt.Println("subcommands:")
	fmt.Println("  status              Show sandbox environment info")
	fmt.Println("  init                One-time setup: create base + browser templates")
	fmt.Println("  list (ls)           List active session containers")
	fmt.Println("  create <template>   Create a sandbox container from a template and open a shell")
	fmt.Println("  shell <session-id>  Open interactive shell in a session container")
	fmt.Println("  save <id> <name>    Save a session container as a template (use 'base' to override @base)")
	fmt.Println("  reset               Destroy and recreate @base template from scratch")
	fmt.Println("  expose <id> <port>  Expose a container port through the Studio reverse proxy")
	fmt.Println("  unexpose <id> <port> Remove a port from the reverse proxy")
	fmt.Println("  url <id> <port>     Print the proxy URL for an exposed port")
	fmt.Println("  cp <id>:<path> [.]  Copy files from a session container to local machine")
	fmt.Println("  refresh             Re-snapshot templates with updated binary (--force for all)")
	fmt.Println("  destroy (rm) <id>   Destroy a session container")
	fmt.Println("  prune               Remove orphaned session containers")
	fmt.Println("  template (tpl)      Manage container templates")
	fmt.Println("  k8s-smoke           End-to-end smoke test against a Kubernetes cluster")
}

func printSandboxTemplateUsage() {
	fmt.Println("usage: astonish sandbox template {list,create,shell,snapshot,promote,delete,info} ...")
	fmt.Println("")
	fmt.Println("Manage container templates for session isolation.")
	fmt.Println("")
	fmt.Println("subcommands:")
	fmt.Println("  list (ls)           List all templates")
	fmt.Println("  create <name>       Create a new template from @base")
	fmt.Println("  shell <name>        Open interactive shell in template")
	fmt.Println("  snapshot <name>     Freeze template state for cloning")
	fmt.Println("  promote <name>      Override @base with this template")
	fmt.Println("  delete (rm) <name>  Delete a template")
	fmt.Println("  info <name>         Show detailed template info")
}

// --- Status ---

func handleSandboxStatus() error {
	det := sboxdocker.DetectDocker("")
	fmt.Println("Platform:         Docker + OverlayFS")
	if !det.Available {
		fmt.Println("Status:           Docker daemon not reachable")
		if det.Reason != "" {
			fmt.Printf("Reason:           %s\n", det.Reason)
		}
		fmt.Println("")
		fmt.Println("Install Docker and retry:")
		fmt.Println("  Linux:         install docker-ce and add your user to the docker group")
		fmt.Println("  macOS/Windows: install Docker Desktop (or another Docker-compatible runtime)")
		return nil
	}
	fmt.Printf("Docker version:   %s\n", det.Version)

	b, err := sboxdocker.Open()
	if err != nil {
		return err
	}
	health, err := b.Health(context.Background())
	if err != nil {
		return err
	}
	if health != nil && health.Healthy {
		fmt.Println("Docker connected: yes")
	} else {
		reason := ""
		if health != nil {
			reason = health.Reason
		}
		fmt.Printf("Docker connected: no (%s)\n", reason)
		return nil
	}

	fmt.Printf("Layers dir:       %s\n", b.LayersDir())
	rootfs := filepath.Join(b.LayersDir(), sandbox.BaseTemplateID, "rootfs")
	if entries, err := os.ReadDir(rootfs); err == nil && len(entries) > 0 {
		fmt.Println("Session creation: overlay ready (@base layer present)")
	} else {
		fmt.Println("Session creation: not configured (run 'astonish sandbox init')")
	}

	sessions, err := b.ListSessions(context.Background(), sandbox.SessionFilter{})
	if err != nil {
		return err
	}
	fmt.Printf("Session containers: %d\n", len(sessions))
	return nil
}

// --- Init ---

func handleSandboxInit() error {
	det := sboxdocker.DetectDocker("")
	if !det.Available {
		return fmt.Errorf("docker is not available.\nLinux: install docker-ce and add your user to the docker group\nmacOS/Windows: install Docker Desktop\n%v", det.Reason)
	}

	fmt.Println("Setting up Docker + OverlayFS sandbox...")
	b, err := sboxdocker.Open()
	if err != nil {
		return err
	}
	fmt.Printf("Pulling sandbox image %s (may take a few minutes on first run)...\n", b.SandboxImage())
	if err := b.SeedBaseLayerFromImage(context.Background()); err != nil {
		return err
	}
	fmt.Printf("Base overlay layer ready at %s/%s/rootfs\n", b.LayersDir(), sandbox.BaseTemplateID)
	fmt.Println("Sandbox initialized. Session containers will use Docker OverlayFS.")
	return nil
}

// promptOptionalTools walks the user through each optional tool with an
// individual confirm prompt. Each prompt includes the tool description and URL
// in the form's Description field, keeping the wizard clean.
func promptOptionalTools() sandbox.BaseTemplateOptions {
	opts := sandbox.DefaultBaseTemplateOptions()
	tools := sandbox.OptionalTools()

	if len(tools) == 0 {
		return opts
	}

	for _, tool := range tools {
		// Build description with tool info and URL
		desc := tool.Description + "\n" + tool.URL

		var install bool
		affirmative := "Yes, install"
		negative := "Skip"
		if tool.Recommended {
			affirmative = "Yes, install (recommended)"
		}

		clearScreen()
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Install %s?", tool.Name)).
					Description(desc).
					Affirmative(affirmative).
					Negative(negative).
					Value(&install),
			),
		).Run()
		if err != nil {
			// User aborted — return what we have so far
			return opts
		}

		opts.InstallTools[tool.ID] = install
	}

	return opts
}

// --- List ---

func openDockerCLI() (*sboxdocker.DockerBackend, error) {
	det := sboxdocker.DetectDocker("")
	if !det.Available {
		return nil, fmt.Errorf("docker is not available: %s\nInstall Docker and run 'astonish sandbox init'", det.Reason)
	}
	return sboxdocker.Open()
}

func resolveDockerSession(ctx context.Context, b *sboxdocker.DockerBackend, identifier string) (*sandbox.Session, error) {
	sessions, err := b.ListSessions(ctx, sandbox.SessionFilter{})
	if err != nil {
		return nil, err
	}
	for _, s := range sessions {
		if s.SessionID == identifier || s.BackendRef == identifier {
			return s, nil
		}
	}
	var match *sandbox.Session
	for _, s := range sessions {
		if strings.HasPrefix(s.SessionID, identifier) || strings.HasPrefix(s.BackendRef, identifier) {
			if match != nil {
				return nil, fmt.Errorf("ambiguous session %q", identifier)
			}
			match = s
		}
	}
	if match == nil {
		return nil, fmt.Errorf("no container found for %q\nUse 'astonish sandbox list' to see active sessions", identifier)
	}
	return match, nil
}

func handleSandboxList() error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	sessions, err := b.ListSessions(context.Background(), sandbox.SessionFilter{})
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("No active session containers.")
		return nil
	}

	fmt.Printf("%-28s %-38s %-12s %-10s\n", "CONTAINER", "SESSION", "TEMPLATE", "STATUS")
	fmt.Printf("%-28s %-38s %-12s %-10s\n", strings.Repeat("-", 28), strings.Repeat("-", 38), strings.Repeat("-", 12), strings.Repeat("-", 10))
	for _, s := range sessions {
		fmt.Printf("%-28s %-38s %-12s %-10s\n", s.BackendRef, s.SessionID, s.TemplateID, s.State)
	}
	return nil
}

// --- Create ---

func handleSandboxCreate(templateName, label string) error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()

	sessionID := label
	if sessionID == "" {
		sessionID = fmt.Sprintf("%s-%d", templateName, time.Now().UnixNano())
	}

	chain := []string{templateName}
	if templateName == "" {
		chain = []string{sandbox.BaseTemplateID}
	}

	fmt.Printf("Creating sandbox from template %q...\n", templateName)
	sess, err := b.CreateSession(ctx, sandbox.SessionSpec{
		SessionID:  sessionID,
		Type:       sandbox.SessionTypeChat,
		TemplateID: templateName,
		LayerChain: chain,
	})
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
		return fmt.Errorf("sandbox not ready: %w", err)
	}
	fmt.Printf("Container %q ready (session: %s)\n", sess.BackendRef, sess.SessionID)
	fmt.Printf("To re-enter later:  astonish sandbox shell %s\n", sess.SessionID)
	fmt.Printf("To destroy:         astonish sandbox destroy %s\n", sess.SessionID)
	return dockerShell(sess.BackendRef)
}

func dockerShell(containerName string) error {
	fmt.Printf("Entering container %s. Type 'exit' to leave.\n", containerName)
	cmd := exec.Command("docker", "exec", "-it", containerName, "/usr/local/bin/astonish-shell", "bash", "-l")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shell session ended with error: %w", err)
	}
	return nil
}

// --- Expose / Unexpose / URL ---

func handleSandboxExpose(containerID string, portArgs []string) error {
	registry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	// Resolve container name
	containerName := resolveContainerNameCLI(registry, containerID)
	if containerName == "" {
		return fmt.Errorf("container %q not found\nUse 'astonish sandbox list' to see active containers", containerID)
	}

	for _, portStr := range portArgs {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port: %s (must be 1-65535)", portStr)
		}

		added, err := registry.ExposePort(containerName, port)
		if err != nil {
			return fmt.Errorf("failed to expose port %d: %w", port, err)
		}

		if added {
			fmt.Printf("Exposed port %d on %s\n", port, containerName)
		} else {
			fmt.Printf("Port %d already exposed on %s\n", port, containerName)
		}
		fmt.Printf("  Access via Studio UI for the direct proxy URL\n")
	}

	return nil
}

func handleSandboxExposeList(containerID string) error {
	registry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	containerName := resolveContainerNameCLI(registry, containerID)
	if containerName == "" {
		return fmt.Errorf("container %q not found", containerID)
	}

	entry := registry.GetByContainerName(containerName)
	if entry == nil {
		return fmt.Errorf("container %q not found", containerID)
	}

	if len(entry.ExposedPorts) == 0 {
		fmt.Printf("No ports exposed on %s\n", containerName)
		return nil
	}

	fmt.Printf("Exposed ports on %s:\n", containerName)
	for _, port := range entry.ExposedPorts {
		fmt.Printf("  %d (access via Studio UI for direct proxy URL)\n", port)
	}

	return nil
}

func handleSandboxUnexpose(containerID string, portArgs []string) error {
	registry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	containerName := resolveContainerNameCLI(registry, containerID)
	if containerName == "" {
		return fmt.Errorf("container %q not found\nUse 'astonish sandbox list' to see active containers", containerID)
	}

	for _, portStr := range portArgs {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("invalid port: %s (must be 1-65535)", portStr)
		}

		removed, err := registry.UnexposePort(containerName, port)
		if err != nil {
			return fmt.Errorf("failed to unexpose port %d: %w", port, err)
		}

		if removed {
			fmt.Printf("Unexposed port %d on %s\n", port, containerName)
		} else {
			fmt.Printf("Port %d was not exposed on %s\n", port, containerName)
		}
	}

	return nil
}

func handleSandboxURL(containerID string, portStr string) error {
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port: %s (must be 1-65535)", portStr)
	}

	registry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	containerName := resolveContainerNameCLI(registry, containerID)
	if containerName == "" {
		return fmt.Errorf("container %q not found", containerID)
	}

	if !registry.IsPortExposed(containerName, port) {
		return fmt.Errorf("port %d is not exposed on %s\nRun 'astonish sandbox expose %s %d' first", port, containerName, containerName, port)
	}

	// The per-port proxy listener runs in the Studio daemon process.
	// The CLI cannot resolve the allocated host port directly.
	fmt.Printf("Port %d is exposed on %s.\n", port, containerName)
	fmt.Printf("The proxy URL is shown in Studio > Settings > Sandbox.\n")
	fmt.Printf("Studio allocates a dedicated host port (19000+) for each exposed service.\n")
	return nil
}

// resolveContainerNameCLI resolves a user-provided identifier to a container name.
// Accepts: session ID, container name, session ID prefix, or container name prefix.
func resolveContainerNameCLI(registry *sandbox.SessionRegistry, input string) string {
	if entry := registry.Get(input); entry != nil {
		return entry.ContainerName
	}
	for _, entry := range registry.List() {
		if entry.ContainerName == input {
			return entry.ContainerName
		}
		if strings.HasPrefix(entry.SessionID, input) {
			return entry.ContainerName
		}
		if strings.HasPrefix(entry.ContainerName, input) {
			return entry.ContainerName
		}
	}
	return ""
}

// --- Refresh ---

func handleSandboxRefresh() error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	// Check for --force flag (refresh all templates unconditionally)
	// Without --force, only refresh templates with stale binaries
	for _, arg := range os.Args {
		if arg == "--force" || arg == "-f" {
			fmt.Println("Force-refreshing all templates...")
			return sandbox.RefreshAll(client, registry)
		}
	}

	// Smart refresh: compute current binary hash and only refresh stale templates
	currentHash, hashErr := sandbox.ComputeBinaryHash()
	if hashErr != nil {
		fmt.Printf("Warning: could not compute binary hash: %v\nFalling back to refreshing @base only.\n", hashErr)
		return sandbox.RefreshTemplate(client, registry, incus.BaseTemplate)
	}

	templates := registry.List()
	refreshed := 0
	for _, meta := range templates {
		if meta.BinaryHash == currentHash {
			fmt.Printf("Template %q: binary is current, skipping.\n", meta.Name)
			continue
		}

		containerName := incus.TemplateName(meta.Name)
		if !client.InstanceExists(containerName) {
			fmt.Printf("Template %q: container missing, skipping.\n", meta.Name)
			continue
		}

		fmt.Printf("Refreshing template %q (stale binary)...\n", meta.Name)
		if err := sandbox.RefreshTemplate(client, registry, meta.Name); err != nil {
			fmt.Printf("  Warning: failed to refresh %q: %v\n", meta.Name, err)
		} else {
			fmt.Printf("  Done.\n")
			refreshed++
		}
	}

	if refreshed == 0 {
		fmt.Println("All templates are up to date.")
	} else {
		fmt.Printf("Refreshed %d template(s).\n", refreshed)
	}

	return nil
}

// --- Destroy ---

func handleSandboxDestroy(identifier string) error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess, err := resolveDockerSession(ctx, b, identifier)
	if err != nil {
		return err
	}
	if err := b.DestroySession(ctx, sess.SessionID); err != nil {
		return err
	}
	fmt.Printf("Destroyed container %s (session %s)\n", sess.BackendRef, sess.SessionID)
	return nil
}

// --- Shell (session) ---

func handleSandboxShell(sessionID string) error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess, err := resolveDockerSession(ctx, b, sessionID)
	if err != nil {
		return err
	}
	if sess.State != sandbox.SessionStateRunning {
		fmt.Printf("Starting session %s...\n", sess.SessionID)
		if err := b.StartSession(ctx, sess.SessionID); err != nil {
			return err
		}
		if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
			return err
		}
	}
	return dockerShell(sess.BackendRef)
}

// --- Prune ---

func handleSandboxPrune() error {
	appCfg, cfgErr := config.LoadAppConfig()
	if cfgErr != nil {
		return fmt.Errorf("failed to load config: %w", cfgErr)
	}

	registry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	// Load existing session IDs from the session store so we don't destroy
	// containers that still belong to active sessions.
	existingSessionIDs := make(map[string]bool)
	if sessDir, dirErr := config.GetSessionsDir(&appCfg.Sessions); dirErr == nil {
		if store, fsErr := persistentsession.NewFileStore(sessDir); fsErr == nil {
			if indexData, loadErr := store.Index().Load(); loadErr == nil {
				for id := range indexData.Sessions {
					existingSessionIDs[id] = true
				}
			}
		}
	}

	kind := sandbox.BackendKind(appCfg.Sandbox.BackendKind())
	switch kind {
	case sandbox.BackendKindK8s, sandbox.BackendKindDocker:
		b, cleanup, bErr := sandbox.BackendFromAppConfig(appCfg)
		if bErr != nil {
			return fmt.Errorf("backend init: %w", bErr)
		}
		if cleanup != nil {
			defer cleanup()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		pruned, pErr := sandbox.PruneOrphansForBackend(ctx, b, registry, existingSessionIDs)
		if pErr != nil {
			return pErr
		}
		if pruned == 0 {
			fmt.Println("No orphaned sandbox pods found.")
		} else {
			fmt.Printf("Pruned %d orphaned sandbox pod(s).\n", pruned)
		}

	default:
		// Incus path
		client, cErr := connectOrFail()
		if cErr != nil {
			return cErr
		}
		pruned, pErr := sandbox.PruneOrphans(client, registry, existingSessionIDs)
		if pErr != nil {
			return pErr
		}
		if pruned == 0 {
			fmt.Println("No orphaned containers found.")
		} else {
			fmt.Printf("Pruned %d orphaned container(s).\n", pruned)
		}
	}

	return nil
}

// --- Reset (@base → fresh install) ---

func handleSandboxReset() error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	sessRegistry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	baseName := incus.TemplateName(incus.BaseTemplate)

	// Check if @base even exists
	baseExists := client.InstanceExists(baseName) || tplRegistry.Get(incus.BaseTemplate) != nil

	// Warn about affected custom templates
	var affectedTemplates []*sandbox.TemplateMeta
	for _, t := range tplRegistry.List() {
		if t.Name != incus.BaseTemplate && t.BasedOn == incus.BaseTemplate {
			affectedTemplates = append(affectedTemplates, t)
		}
	}

	// Warn about active sessions
	activeSessions := sessRegistry.List()

	fmt.Println("")
	fmt.Println("WARNING: This will destroy the current @base template and recreate it")
	fmt.Println("from a fresh OS image with core tools reinstalled.")

	if len(affectedTemplates) > 0 {
		fmt.Printf("\nThe following custom templates are based on @base and will need to be recreated:\n")
		for _, t := range affectedTemplates {
			desc := t.Description
			if desc != "" {
				desc = " (" + desc + ")"
			}
			fmt.Printf("  - %s%s\n", t.Name, desc)
		}
	}

	if len(activeSessions) > 0 {
		fmt.Printf("\n%d active session container(s) may lose their overlay base layer.\n", len(activeSessions))
	}

	// Confirm
	var proceed bool
	fmt.Println("")
	confirmErr := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Proceed with reset?").
				Affirmative("Yes, reset @base").
				Negative("Cancel").
				Value(&proceed),
		),
	).Run()
	if confirmErr != nil || !proceed {
		fmt.Println("Reset cancelled.")
		return nil
	}

	// Tear down existing @base
	if baseExists {
		fmt.Println("\nDestroying current @base template...")

		// Resolve pool path for overlay cleanup
		poolName, poolErr := incus.GetPoolForProfile(client)
		if poolErr == nil {
			poolPath, pathErr := incus.GetPoolSourcePath(client, poolName)
			if pathErr == nil && incus.IsOverlayMounted(poolPath, baseName) {
				if err := incus.UnmountSessionOverlay(poolPath, baseName); err != nil {
					fmt.Printf("  Warning: failed to unmount overlay: %v\n", err)
				}
			}
		}

		if client.IsRunning(baseName) {
			if err := client.StopInstance(baseName, false); err != nil {
				fmt.Printf("  Warning: failed to stop @base: %v\n", err)
			}
		}

		if client.HasSnapshot(baseName, incus.SnapshotName) {
			if err := client.DeleteSnapshot(baseName, incus.SnapshotName); err != nil {
				fmt.Printf("  Warning: failed to delete snapshot: %v\n", err)
			}
		}

		if client.InstanceExists(baseName) {
			if err := client.StopAndDeleteInstance(baseName); err != nil {
				return fmt.Errorf("failed to destroy @base container: %w", err)
			}
		}

		// Remove from registry
		if err := tplRegistry.Remove(incus.BaseTemplate); err != nil {
			fmt.Printf("  Warning: failed to remove registry entry: %v\n", err)
		}

		fmt.Println("Current @base template destroyed.")
	}

	// Prompt for optional tools (same as sandbox init)
	fmt.Println("")
	opts := promptOptionalTools()

	// Wire browser engine into base template options so browser packages
	// (Chromium, KasmVNC, X11 deps) are installed in the base template.
	appCfg, _ := config.LoadAppConfig()
	if appCfg != nil {
		bCfg := incus.BrowserContainerConfig{
			ChromePath:          appCfg.Browser.ChromePath,
			FingerprintSeed:     appCfg.Browser.FingerprintSeed,
			FingerprintPlatform: appCfg.Browser.FingerprintPlatform,
		}
		engine := incus.DetectBrowserEngine(bCfg)
		if incus.IsContainerCompatibleEngine(engine) {
			opts.BrowserEngine = engine
		}
	}

	// Recreate from scratch
	return sandbox.InitBaseTemplate(client, tplRegistry, opts)
}

// --- Save (session → template) ---

func handleSandboxSave(identifier, templateName, description string) error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	sessRegistry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	// Resolve the identifier (session ID, container name, or prefix)
	sessionID, found := sessRegistry.ResolveSessionID(identifier)
	if !found {
		return fmt.Errorf("no container found for %q\nUse 'astonish sandbox list' to see active containers", identifier)
	}

	entry := sessRegistry.Get(sessionID)
	if entry == nil {
		return fmt.Errorf("session %q found but has no registry entry", sessionID)
	}

	containerName := entry.ContainerName
	sourceTemplate := entry.TemplateName
	if sourceTemplate == "" {
		sourceTemplate = incus.BaseTemplate
	}

	// Normalize: accept both "base" and "@base"
	isPromoteToBase := templateName == "base" || templateName == "@base"

	if isPromoteToBase {
		// Promote session → @base via a temporary intermediate template.
		// 1. Save the session as a temp template
		// 2. Promote the temp template to @base
		// 3. Clean up the temp template
		const tmpName = "_promote-from-session"

		fmt.Printf("Saving session %s (%s) as new @base template...\n",
			sessionID[:min(8, len(sessionID))], containerName)

		// Clean up any leftover temp template from a previous failed run
		if tplRegistry.Get(tmpName) != nil {
			_ = sandbox.DeleteTemplate(client, tplRegistry, tmpName)
		}

		// Step 1: Save session as temp template
		if _, err := sandbox.CreateTemplateFromContainer(
			client, tplRegistry, containerName, tmpName, "temporary promote", sourceTemplate, false,
		); err != nil {
			return fmt.Errorf("failed to save session as template: %w", err)
		}

		// Step 2: Promote to @base
		if err := sandbox.PromoteTemplate(client, tplRegistry, tmpName); err != nil {
			// Clean up temp on failure
			_ = sandbox.DeleteTemplate(client, tplRegistry, tmpName)
			return fmt.Errorf("failed to promote to @base: %w", err)
		}

		// Step 3: Clean up temp template (promote already took its contents)
		if tplRegistry.Get(tmpName) != nil {
			_ = sandbox.DeleteTemplate(client, tplRegistry, tmpName)
		}

		fmt.Println("Done. The @base template now contains the session's state.")
		fmt.Println("All new sessions will use this as their starting point.")
	} else {
		// Save session as a named custom template
		fmt.Printf("Saving session %s (%s) as template %q...\n",
			sessionID[:min(8, len(sessionID))], containerName, templateName)

		if _, err := sandbox.CreateTemplateFromContainer(
			client, tplRegistry, containerName, templateName, description, sourceTemplate, false,
		); err != nil {
			return fmt.Errorf("failed to save session as template: %w", err)
		}

		fmt.Printf("Template %q created from session %s.\n", templateName, sessionID[:min(8, len(sessionID))])
		fmt.Printf("Use 'astonish sandbox template shell %s' to inspect it.\n", templateName)
	}

	return nil
}

// --- Template List ---

func handleTemplateList() error {
	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	templates := registry.List()
	if len(templates) == 0 {
		fmt.Println("No templates. Run 'astonish sandbox init' to create the base template.")
		return nil
	}

	fmt.Printf("%-16s %-30s %-20s %-20s %-12s\n", "NAME", "DESCRIPTION", "CREATED", "LAST SNAPSHOT", "FLEET PLANS")
	fmt.Printf("%-16s %-30s %-20s %-20s %-12s\n", strings.Repeat("-", 16), strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 20), strings.Repeat("-", 12))

	for _, t := range templates {
		desc := t.Description
		if len(desc) > 28 {
			desc = desc[:28] + ".."
		}
		if desc == "" && t.Name == incus.BaseTemplate {
			desc = "(default base template)"
		}

		snapshotStr := "-"
		if !t.SnapshotAt.IsZero() {
			snapshotStr = t.SnapshotAt.Format("2006-01-02 15:04:05")
		}

		plans := "-"
		if len(t.FleetPlans) > 0 {
			plans = strings.Join(t.FleetPlans, ", ")
		}

		fmt.Printf("%-16s %-30s %-20s %-20s %-12s\n",
			t.Name,
			desc,
			t.CreatedAt.Format("2006-01-02 15:04:05"),
			snapshotStr,
			plans,
		)
	}

	return nil
}

// --- Template Create ---

func handleTemplateCreate(name, description string) error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	return sandbox.CreateTemplate(client, registry, name, description)
}

// --- Template Shell ---

func handleTemplateShell(name string) error {
	// Shell uses the incus CLI directly, but we still verify connectivity first.
	platform := incus.DetectPlatform()
	if platform == incus.PlatformUnsupported {
		return fmt.Errorf("no container runtime available")
	}

	// On Docker+Incus, ensure the Docker container is running
	if platform == incus.PlatformDockerIncus {
		if !incus.IsIncusDockerContainerRunning() {
			return fmt.Errorf("Docker+Incus container is not running.\nRun 'astonish sandbox init' to set up the runtime")
		}
	}

	incus.SetActivePlatform(platform)
	client, err := incus.Connect(platform)
	if err != nil {
		return fmt.Errorf("failed to connect to Incus: %w", err)
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return fmt.Errorf("failed to load template registry: %w", err)
	}

	return sandbox.ShellIntoTemplate(client, registry, name)
}

// --- Template Snapshot ---

func handleTemplateSnapshot(name string) error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	return sandbox.SnapshotTemplate(client, registry, name)
}

// --- Template Promote ---

func handleTemplatePromote(name string) error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	return sandbox.PromoteTemplate(client, registry, name)
}

// --- Template Delete ---

func handleTemplateDelete(name string) error {
	client, err := connectOrFail()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	return sandbox.DeleteTemplate(client, registry, name)
}

// --- Template Info ---

func handleTemplateInfo(name string) error {
	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	meta := registry.Get(name)
	if meta == nil {
		return fmt.Errorf("template %q not found", name)
	}

	fmt.Printf("Name:          %s\n", meta.Name)
	if meta.Description != "" {
		fmt.Printf("Description:   %s\n", meta.Description)
	}
	fmt.Printf("Created:       %s\n", meta.CreatedAt.Format(time.RFC3339))
	if !meta.SnapshotAt.IsZero() {
		fmt.Printf("Last snapshot: %s\n", meta.SnapshotAt.Format(time.RFC3339))
	} else {
		fmt.Printf("Last snapshot: (none)\n")
	}
	if meta.BasedOn != "" {
		fmt.Printf("Based on:      @%s\n", meta.BasedOn)
	}
	if meta.BinaryHash != "" {
		fmt.Printf("Binary hash:   %s\n", meta.BinaryHash[:min(16, len(meta.BinaryHash))]+"...")
	}
	if len(meta.FleetPlans) > 0 {
		fmt.Printf("Fleet plans:   %s\n", strings.Join(meta.FleetPlans, ", "))
	}

	// Try to get Incus-level info
	platform := incus.DetectPlatform()
	if platform != incus.PlatformUnsupported {
		client, err := incus.Connect(platform)
		if err == nil {
			containerName := incus.TemplateName(name)
			if client.InstanceExists(containerName) {
				inst, err := client.GetInstance(containerName)
				if err == nil {
					fmt.Printf("Container:     %s\n", inst.Name)
					fmt.Printf("Status:        %s\n", inst.Status)
				}

				hasSnap := client.HasSnapshot(containerName, incus.SnapshotName)
				if hasSnap {
					fmt.Printf("Snapshot:      ready (cloneable)\n")
				} else {
					fmt.Printf("Snapshot:      (none — run 'astonish sandbox template snapshot %s')\n", name)
				}
			} else {
				fmt.Printf("Container:     MISSING (metadata exists but container was deleted)\n")
			}
		}
	}

	return nil
}

// --- Copy files from container ---

// handleSandboxCp copies files from a session container to the local machine.
// The source argument uses scp-style syntax: <identifier>:<container-path>
// The identifier can be a session ID, container name, or prefix of either.
func handleSandboxCp(source, localPath string) error {
	// Parse scp-style source argument: <identifier>:<container-path>
	colonIdx := strings.Index(source, ":")
	if colonIdx < 1 {
		return fmt.Errorf("invalid source format: expected <session-id>:<path>\n" +
			"Example: astonish sandbox cp ff5c1146:/tmp/video.mp4 ./video.mp4")
	}

	identifier := source[:colonIdx]
	containerPath := source[colonIdx+1:]
	if containerPath == "" {
		return fmt.Errorf("missing container path after ':'")
	}

	client, err := connectOrFail()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewSessionRegistry()
	if err != nil {
		return err
	}

	// Resolve the identifier to a session/container
	sessionID, found := registry.ResolveSessionID(identifier)
	if !found {
		return fmt.Errorf("no container found for %q\nUse 'astonish sandbox list' to see active containers", identifier)
	}

	entry := registry.Get(sessionID)
	if entry == nil {
		return fmt.Errorf("session %q not found in registry", sessionID)
	}
	containerName := entry.ContainerName

	// Check container is running
	if !client.IsRunning(containerName) {
		return fmt.Errorf("container %s is not running", containerName)
	}

	// Probe the source path to determine if it's a file or directory
	reader, resp, err := client.PullFile(containerName, containerPath)
	if err != nil {
		return fmt.Errorf("failed to access %s:%s: %w", containerName, containerPath, err)
	}

	if resp.Type == "directory" {
		// Directory copy
		if localPath == "" {
			localPath = filepath.Base(containerPath)
		}
		fmt.Printf("Copying %s from %s...\n", containerPath, containerName)
		stats, err := copyDirectoryFromContainer(client, containerName, containerPath, localPath)
		if err != nil {
			return err
		}
		fmt.Printf("Done (%d files, %s total)\n", stats.fileCount, formatBytes(stats.totalBytes))
		return nil
	}

	// Single file copy
	defer reader.Close()

	if localPath == "" {
		localPath = filepath.Base(containerPath)
	}

	// If localPath is a directory, append the filename
	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		localPath = filepath.Join(localPath, filepath.Base(containerPath))
	}

	fmt.Printf("Copying %s from %s... ", containerPath, containerName)

	outFile, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(resp.Mode))
	if err != nil {
		return fmt.Errorf("failed to create local file %s: %w", localPath, err)
	}
	defer outFile.Close()

	written, err := io.Copy(outFile, reader)
	if err != nil {
		return fmt.Errorf("failed to write to %s: %w", localPath, err)
	}

	fmt.Printf("done (%s)\n", formatBytes(written))
	return nil
}

// copyStats tracks progress during recursive directory copy.
type copyStats struct {
	fileCount  int
	totalBytes int64
}

// copyDirectoryFromContainer recursively copies a directory from a container
// to the local filesystem. Uses the Incus file API to list entries and pull
// each file individually.
func copyDirectoryFromContainer(client *incus.IncusClient, containerName, containerDir, localDir string) (*copyStats, error) {
	stats := &copyStats{}

	if err := os.MkdirAll(localDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create local directory %s: %w", localDir, err)
	}

	entries, err := client.ListDirectory(containerName, containerDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		containerEntryPath := path.Join(containerDir, entry)
		localEntryPath := filepath.Join(localDir, entry)

		reader, resp, err := client.PullFile(containerName, containerEntryPath)
		if err != nil {
			return nil, fmt.Errorf("failed to access %s: %w", containerEntryPath, err)
		}

		if resp.Type == "directory" {
			// Recurse into subdirectory
			subStats, err := copyDirectoryFromContainer(client, containerName, containerEntryPath, localEntryPath)
			if err != nil {
				return nil, err
			}
			stats.fileCount += subStats.fileCount
			stats.totalBytes += subStats.totalBytes
			continue
		}

		// Copy file
		outFile, err := os.OpenFile(localEntryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(resp.Mode))
		if err != nil {
			reader.Close()
			return nil, fmt.Errorf("failed to create %s: %w", localEntryPath, err)
		}

		written, err := io.Copy(outFile, reader)
		reader.Close()
		outFile.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to write %s: %w", localEntryPath, err)
		}

		stats.fileCount++
		stats.totalBytes += written
		fmt.Printf("  %s (%s)\n", path.Join(filepath.Base(containerDir), entry), formatBytes(written))
	}

	return stats, nil
}

// formatBytes returns a human-readable byte size string.
func formatBytes(b int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)

	switch {
	case b >= gb:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	case b >= mb:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
	case b >= kb:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(kb))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// connectOrFail is a helper that detects the platform and connects to Incus,
// returning an error if not available. On Docker+Incus, ensures the Docker
// container is running and sets the active platform.
func connectOrFail() (*incus.IncusClient, error) {
	platform := incus.DetectPlatform()
	if platform == incus.PlatformUnsupported {
		return nil, fmt.Errorf("no container runtime available")
	}

	// Load sandbox config so IsPrivileged() and containerSecurityConfig()
	// reflect user settings. Without this, containers created via
	// "sandbox create" would ignore sandbox.privileged in the config.
	if appCfg, err := config.LoadAppConfig(); err == nil && appCfg != nil {
		sandbox.SetSandboxConfig(&appCfg.Sandbox)
	}

	// On Docker+Incus, ensure the Docker container is running
	if platform == incus.PlatformDockerIncus {
		if !incus.IsIncusDockerContainerRunning() {
			return nil, fmt.Errorf("Docker+Incus container is not running.\nRun 'astonish sandbox init' to set up the runtime")
		}
	}

	incus.SetActivePlatform(platform)
	client, err := incus.Connect(platform)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Incus: %w", err)
	}

	return client, nil
}
