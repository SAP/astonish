package astonish

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	sboxdocker "github.com/SAP/astonish/pkg/sandbox/docker"
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
	// auth and never touches host mounts, so it must NOT go through
	// the Linux sudo escalation path below; doing so would either
	// drop the user's KUBECONFIG / HOME settings or just fail on
	// non-root CI where sudo is unavailable.
	if args[0] == "k8s-smoke" {
		return handleSandboxK8sSmoke(args[1:])
	}

	// Docker OverlayFS sessions do not need host overlay mounts.
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
	fmt.Println("  refresh             Re-seed @base from the sandbox-base image")
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
	fmt.Println("  snapshot <name>     Capture the template session upper as a named layer")
	fmt.Println("  promote <name>      Flatten this template over @base")
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

	chain := overlayLayerChainFromRegistry(templateName)
	displayName := templateName
	if displayName == "" {
		displayName = sandbox.BaseTemplateID
	}

	fmt.Printf("Creating sandbox from template %q...\n", displayName)
	sess, err := b.CreateSession(ctx, sandbox.SessionSpec{
		SessionID:  sessionID,
		Type:       sandbox.SessionTypeChat,
		TemplateID: displayName,
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
	b, err := openDockerCLI()
	if err != nil {
		return err
	}

	fmt.Println("Re-seeding @base from the sandbox-base image...")
	fmt.Println("Named overlay layers are deltas and do not embed the Astonish binary.")
	if err := b.ReseedBaseLayerFromImage(context.Background()); err != nil {
		return err
	}
	fmt.Printf("Base overlay layer ready at %s/%s/rootfs\n", b.LayersDir(), sandbox.BaseTemplateID)
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
		fmt.Println("No orphaned sandbox sessions found.")
	} else {
		fmt.Printf("Pruned %d orphaned sandbox session(s).\n", pruned)
	}

	return nil
}

// --- Reset (@base → fresh install) ---

func handleSandboxReset() error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}

	fmt.Println("")
	fmt.Println("WARNING: This will delete the current @base overlay layer and re-seed it")
	fmt.Println("from the sandbox-base Docker image.")

	var proceed bool
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

	fmt.Println("\nRemoving current @base layer...")
	if err := os.RemoveAll(filepath.Join(b.LayersDir(), sandbox.BaseTemplateID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove @base layer: %w", err)
	}
	if err := b.SeedBaseLayerFromImage(context.Background()); err != nil {
		return err
	}
	fmt.Println("Done. New sessions will start from the re-seeded @base layer.")
	return nil
}

// --- Save (session → template) ---

func handleSandboxSave(identifier, templateName, description string) error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess, err := resolveDockerSession(ctx, b, identifier)
	if err != nil {
		return err
	}

	if isBaseTemplateName(templateName) {
		fmt.Printf("Flattening session %s over @base...\n", sess.SessionID)
		if err := b.ReplaceBaseFromSession(ctx, sess.SessionID); err != nil {
			return err
		}
		fmt.Println("Done. New sessions will start from the updated @base layer.")
		return nil
	}

	fmt.Printf("Saving session %s as template layer %q...\n", sess.SessionID, templateName)
	art, err := b.SaveSessionAsTemplate(ctx, sess.SessionID)
	if err != nil {
		return err
	}
	if err := b.AliasLayer(templateName, art.LayerID); err != nil {
		return err
	}
	basedOn := sess.TemplateID
	if basedOn == "" {
		basedOn = sandbox.BaseTemplateID
	}
	if err := registerOverlayTemplate(templateName, description, basedOn); err != nil {
		return err
	}
	fmt.Printf("Captured layer %s (%d bytes) as %q.\n", art.LayerID, art.SizeBytes, templateName)
	return nil
}

// --- Template List ---

func handleTemplateList() error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}

	templates := registry.List()
	seen := map[string]bool{}
	type row struct {
		name, desc, created, snapshot, plans string
	}
	var rows []row

	baseRootfs := filepath.Join(b.LayersDir(), sandbox.BaseTemplateID, "rootfs")
	if entries, err := os.ReadDir(baseRootfs); err == nil && len(entries) > 0 {
		rows = append(rows, row{
			name:     sandbox.BaseTemplateID,
			desc:     "(default base overlay layer)",
			created:  "-",
			snapshot: "ready",
			plans:    "-",
		})
		seen[sandbox.BaseTemplateID] = true
	}

	for _, t := range templates {
		if seen[t.Name] {
			continue
		}
		desc := t.Description
		if len(desc) > 28 {
			desc = desc[:28] + ".."
		}
		snapshotStr := "-"
		if !t.SnapshotAt.IsZero() {
			snapshotStr = t.SnapshotAt.Format("2006-01-02 15:04:05")
		} else if overlayLayerExists(b, t.Name) {
			snapshotStr = "layer"
		}
		plans := "-"
		if len(t.FleetPlans) > 0 {
			plans = strings.Join(t.FleetPlans, ", ")
		}
		created := "-"
		if !t.CreatedAt.IsZero() {
			created = t.CreatedAt.Format("2006-01-02 15:04:05")
		}
		rows = append(rows, row{t.Name, desc, created, snapshotStr, plans})
		seen[t.Name] = true
	}

	if len(rows) == 0 {
		fmt.Println("No templates. Run 'astonish sandbox init' to create the base overlay layer.")
		return nil
	}

	fmt.Printf("%-16s %-30s %-20s %-20s %-12s\n", "NAME", "DESCRIPTION", "CREATED", "LAYER", "FLEET PLANS")
	fmt.Printf("%-16s %-30s %-20s %-20s %-12s\n", strings.Repeat("-", 16), strings.Repeat("-", 30), strings.Repeat("-", 20), strings.Repeat("-", 20), strings.Repeat("-", 12))
	for _, r := range rows {
		fmt.Printf("%-16s %-30s %-20s %-20s %-12s\n", r.name, r.desc, r.created, r.snapshot, r.plans)
	}
	return nil
}

func overlayLayerExists(b *sboxdocker.DockerBackend, name string) bool {
	if b == nil || name == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(b.LayersDir(), name, "rootfs"))
	return err == nil && len(entries) > 0
}

// --- Template Create ---

func handleTemplateCreate(name, description string) error {
	if isBaseTemplateName(name) {
		return fmt.Errorf("cannot create a template named %q (reserved)", name)
	}
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	if !overlayLayerExists(b, sandbox.BaseTemplateID) {
		return fmt.Errorf("base overlay layer is missing; run 'astonish sandbox init' first")
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}
	if registry.Exists(name) {
		return fmt.Errorf("template %q already exists", name)
	}

	ctx := context.Background()
	sessionID := templateSessionID(name)
	fmt.Printf("Creating template %q from @base...\n", name)
	sess, err := b.CreateSession(ctx, sandbox.SessionSpec{
		SessionID:  sessionID,
		Type:       sandbox.SessionTypeChat,
		TemplateID: sandbox.BaseTemplateID,
		LayerChain: []string{sandbox.BaseTemplateID},
		Labels:     map[string]string{"astonish.io/purpose": "template"},
	})
	if err != nil {
		return fmt.Errorf("failed to create template session: %w", err)
	}
	if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
		return fmt.Errorf("template session not ready: %w", err)
	}
	if err := registerOverlayTemplate(name, description, sandbox.BaseTemplateID); err != nil {
		return err
	}
	fmt.Printf("Template %q created (session %s).\n", name, sess.SessionID)
	fmt.Printf("Use 'astonish sandbox template shell %s' to customize, then 'template snapshot %s'.\n", name, name)
	return nil
}

// --- Template Shell ---

func handleTemplateShell(name string) error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sessionID := templateSessionID(name)
	sess, err := resolveDockerSession(ctx, b, sessionID)
	if err != nil {
		if !overlayLayerExists(b, sandbox.BaseTemplateID) {
			return fmt.Errorf("template %q does not exist; run 'astonish sandbox template create %s'", name, name)
		}
		chain := overlayLayerChainFromRegistry(name)
		if !overlayLayerExists(b, name) {
			chain = []string{sandbox.BaseTemplateID}
		}
		fmt.Printf("Starting template session %q...\n", name)
		sess, err = b.CreateSession(ctx, sandbox.SessionSpec{
			SessionID:  sessionID,
			Type:       sandbox.SessionTypeChat,
			TemplateID: name,
			LayerChain: chain,
			Labels:     map[string]string{"astonish.io/purpose": "template"},
		})
		if err != nil {
			return err
		}
		if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
			return err
		}
	} else if sess.State != sandbox.SessionStateRunning {
		fmt.Printf("Starting template session %s...\n", sess.SessionID)
		if err := b.StartSession(ctx, sess.SessionID); err != nil {
			return err
		}
		if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
			return err
		}
	}
	return dockerShell(sess.BackendRef)
}

// --- Template Snapshot ---

func handleTemplateSnapshot(name string) error {
	if isBaseTemplateName(name) {
		return fmt.Errorf("cannot snapshot %q; use 'astonish sandbox save <session> base' to replace @base", name)
	}
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess, err := resolveDockerSession(ctx, b, templateSessionID(name))
	if err != nil {
		return fmt.Errorf("template session %q is not running: %w\nUse 'astonish sandbox template shell %s' first", name, err, name)
	}
	if sess.State != sandbox.SessionStateRunning {
		if err := b.StartSession(ctx, sess.SessionID); err != nil {
			return err
		}
		if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
			return err
		}
	}

	fmt.Printf("Capturing template %q upper layer...\n", name)
	art, err := b.SaveSessionAsTemplate(ctx, sess.SessionID)
	if err != nil {
		return err
	}
	if err := b.AliasLayer(name, art.LayerID); err != nil {
		return err
	}
	if err := touchOverlayTemplateSnapshot(name, sess.TemplateID); err != nil {
		return err
	}
	fmt.Printf("Captured layer %s (%d bytes) as %q.\n", art.LayerID, art.SizeBytes, name)
	return nil
}

// --- Template Promote ---

func handleTemplatePromote(name string) error {
	if isBaseTemplateName(name) {
		return fmt.Errorf("%q is already @base", name)
	}
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess, err := resolveDockerSession(ctx, b, templateSessionID(name))
	if err != nil {
		chain := overlayLayerChainFromRegistry(name)
		if !overlayLayerExists(b, name) && !overlayLayerExists(b, sandbox.BaseTemplateID) {
			return fmt.Errorf("template %q has no overlay layer", name)
		}
		fmt.Printf("Starting template %q to flatten over @base...\n", name)
		sess, err = b.CreateSession(ctx, sandbox.SessionSpec{
			SessionID:  templateSessionID(name),
			Type:       sandbox.SessionTypeChat,
			TemplateID: name,
			LayerChain: chain,
			Labels:     map[string]string{"astonish.io/purpose": "template"},
		})
		if err != nil {
			return err
		}
		if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
			return err
		}
	} else if sess.State != sandbox.SessionStateRunning {
		if err := b.StartSession(ctx, sess.SessionID); err != nil {
			return err
		}
		if err := b.WaitForSessionReady(ctx, sess.SessionID); err != nil {
			return err
		}
	}

	fmt.Printf("Flattening template %q over @base...\n", name)
	if err := b.ReplaceBaseFromSession(ctx, sess.SessionID); err != nil {
		return err
	}
	fmt.Println("Done. New sessions will start from the promoted @base layer.")
	return nil
}

// --- Template Delete ---

func handleTemplateDelete(name string) error {
	if isBaseTemplateName(name) {
		return fmt.Errorf("cannot delete %q; use 'astonish sandbox reset' to re-seed it", name)
	}
	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	if sess, err := resolveDockerSession(ctx, b, templateSessionID(name)); err == nil {
		_ = b.DestroySession(ctx, sess.SessionID)
	}
	if err := b.DeleteTemplate(ctx, name, true); err != nil {
		return err
	}
	if registry, err := sandbox.NewTemplateRegistry(); err == nil {
		_ = registry.Remove(name)
	}
	fmt.Printf("Deleted template %q.\n", name)
	return nil
}

// --- Template Info ---

func handleTemplateInfo(name string) error {
	b, err := openDockerCLI()
	if err != nil {
		return err
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}
	meta := registry.Get(name)
	if meta == nil && !isBaseTemplateName(name) && !overlayLayerExists(b, name) {
		return fmt.Errorf("template %q not found", name)
	}

	display := name
	if isBaseTemplateName(name) {
		display = sandbox.BaseTemplateID
	}
	fmt.Printf("Name:          %s\n", display)
	if meta != nil && meta.Description != "" {
		fmt.Printf("Description:   %s\n", meta.Description)
	}
	if meta != nil && !meta.CreatedAt.IsZero() {
		fmt.Printf("Created:       %s\n", meta.CreatedAt.Format(time.RFC3339))
	}
	if meta != nil && !meta.SnapshotAt.IsZero() {
		fmt.Printf("Last snapshot: %s\n", meta.SnapshotAt.Format(time.RFC3339))
	} else if overlayLayerExists(b, display) {
		fmt.Printf("Layer:         ready (%s)\n", filepath.Join(b.LayersDir(), display, "rootfs"))
	} else {
		fmt.Printf("Layer:         (none — run 'astonish sandbox template snapshot %s')\n", name)
	}
	if meta != nil && meta.BasedOn != "" {
		fmt.Printf("Based on:      %s\n", meta.BasedOn)
	}
	if meta != nil && len(meta.FleetPlans) > 0 {
		fmt.Printf("Fleet plans:   %s\n", strings.Join(meta.FleetPlans, ", "))
	}

	if sess, err := resolveDockerSession(context.Background(), b, templateSessionID(name)); err == nil {
		fmt.Printf("Session:       %s (%s)\n", sess.SessionID, sess.State)
		fmt.Printf("Container:     %s\n", sess.BackendRef)
	}
	return nil
}

// --- Copy files from container ---

// handleSandboxCp copies files from a session container to the local machine.
// The source argument uses scp-style syntax: <identifier>:<container-path>
// The identifier can be a session ID, container name, or prefix of either.
func handleSandboxCp(source, localPath string) error {
	identifier, containerPath, err := parseSandboxCpSource(source)
	if err != nil {
		return err
	}

	b, err := openDockerCLI()
	if err != nil {
		return err
	}
	ctx := context.Background()
	sess, err := resolveDockerSession(ctx, b, identifier)
	if err != nil {
		return err
	}
	if sess.State != sandbox.SessionStateRunning {
		return fmt.Errorf("container %s is not running", sess.BackendRef)
	}

	probe, err := b.Exec(ctx, sess.SessionID, sandbox.ExecSpec{
		Command: []string{"sh", "-c", fmt.Sprintf(
			`if [ -d %q ]; then echo DIR; elif [ -e %q ]; then echo FILE; else echo MISSING; fi`,
			containerPath, containerPath)},
	})
	if err != nil {
		return fmt.Errorf("failed to access %s:%s: %w", sess.BackendRef, containerPath, err)
	}
	kind := strings.TrimSpace(string(probe.Stdout))
	if kind == "MISSING" {
		return fmt.Errorf("failed to access %s:%s: no such file or directory", sess.BackendRef, containerPath)
	}

	if localPath == "" {
		localPath = filepath.Base(containerPath)
	}

	if kind == "DIR" {
		if err := os.MkdirAll(localPath, 0o755); err != nil {
			return fmt.Errorf("failed to create local directory %s: %w", localPath, err)
		}
		fmt.Printf("Copying %s from %s...\n", containerPath, sess.BackendRef)
		res, err := b.Exec(ctx, sess.SessionID, sandbox.ExecSpec{
			Command: []string{"tar", "-C", containerPath, "-cf", "-", "."},
		})
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("tar in %s exited %d: %s", sess.BackendRef, res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
		cmd := exec.Command("tar", "-C", localPath, "-xf", "-")
		cmd.Stdin = bytes.NewReader(res.Stdout)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("extract to %s: %w", localPath, err)
		}
		fmt.Printf("Done (%s)\n", formatBytes(int64(len(res.Stdout))))
		return nil
	}

	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		localPath = filepath.Join(localPath, filepath.Base(containerPath))
	}

	fmt.Printf("Copying %s from %s... ", containerPath, sess.BackendRef)
	reader, err := b.PullFile(ctx, sess.SessionID, containerPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	outFile, err := os.OpenFile(localPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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

func parseSandboxCpSource(source string) (identifier, containerPath string, err error) {
	colonIdx := strings.Index(source, ":")
	if colonIdx < 1 {
		return "", "", fmt.Errorf("invalid source format: expected <session-id>:<path>\n" +
			"Example: astonish sandbox cp ff5c1146:/tmp/video.mp4 ./video.mp4")
	}
	identifier = source[:colonIdx]
	containerPath = source[colonIdx+1:]
	if containerPath == "" {
		return "", "", fmt.Errorf("missing container path after ':'")
	}
	return identifier, containerPath, nil
}

func isBaseTemplateName(name string) bool {
	switch strings.TrimSpace(name) {
	case "", "base", sandbox.BaseTemplateID:
		return true
	default:
		return false
	}
}

func templateSessionID(name string) string {
	return "template-" + strings.TrimPrefix(name, "@")
}

func overlayLayerChain(name string, basedOn map[string]string) []string {
	if isBaseTemplateName(name) {
		return []string{sandbox.BaseTemplateID}
	}
	seen := map[string]bool{}
	var walk func(string) []string
	walk = func(n string) []string {
		if isBaseTemplateName(n) {
			return []string{sandbox.BaseTemplateID}
		}
		if seen[n] {
			return []string{sandbox.BaseTemplateID}
		}
		seen[n] = true
		parent := ""
		if basedOn != nil {
			parent = basedOn[n]
		}
		if parent == "" {
			return []string{sandbox.BaseTemplateID, n}
		}
		return append(walk(parent), n)
	}
	return walk(name)
}

func overlayLayerChainFromRegistry(name string) []string {
	basedOn := map[string]string{}
	if registry, err := sandbox.NewTemplateRegistry(); err == nil {
		for _, t := range registry.List() {
			basedOn[t.Name] = t.BasedOn
		}
	}
	return overlayLayerChain(name, basedOn)
}

func registerOverlayTemplate(name, description, basedOn string) error {
	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}
	meta := registry.Get(name)
	if meta == nil {
		meta = &sandbox.TemplateMeta{
			Name:      name,
			CreatedAt: time.Now().UTC(),
		}
	}
	if description != "" {
		meta.Description = description
	}
	if basedOn != "" {
		meta.BasedOn = basedOn
	}
	return registry.Add(meta)
}

func touchOverlayTemplateSnapshot(name, basedOn string) error {
	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		return err
	}
	meta := registry.Get(name)
	if meta == nil {
		meta = &sandbox.TemplateMeta{
			Name:      name,
			CreatedAt: time.Now().UTC(),
			BasedOn:   basedOn,
		}
	}
	if basedOn != "" && meta.BasedOn == "" {
		meta.BasedOn = basedOn
	}
	meta.SnapshotAt = time.Now().UTC()
	return registry.Add(meta)
}
