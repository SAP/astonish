package astonish

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/version"
	"golang.org/x/term"
)

// Execute is the main entry point for the CLI
func Execute() error {
	if len(os.Args) < 2 {
		stdinTTY, stdoutTTY := bareCodeTTYStatus()
		if shouldLaunchBareCodeWith(stdinTTY, stdoutTTY) {
			checkForUpdates()
			return handleCodeCommand(nil)
		}
		printUsage()
		if hint := bareCodeUnavailableHint(stdinTTY, stdoutTTY); hint != "" {
			fmt.Fprintln(os.Stderr, hint)
		}
		return fmt.Errorf("no command provided")
	}
	if os.Args[1] == "-h" || os.Args[1] == "--help" {
		printUsage()
		return nil
	}

	// Handle --version flag
	if os.Args[1] == "--version" || os.Args[1] == "-v" {
		printVersion()
		return nil
	}

	// Check for updates — skip for non-interactive / structured-stdout
	// subcommands where stdout is a protocol channel (e.g. "node" emits
	// NDJSON) or already handles its own output ("version").
	if os.Args[1] != "version" && os.Args[1] != "node" {
		checkForUpdates()
	}

	command := os.Args[1]
	switch command {
	case "login":
		return handleLoginCommand(os.Args[2:])
	case "logout":
		return handleLogoutCommand(os.Args[2:])
	case "status":
		return handleStatusCommand(os.Args[2:])
	case "org":
		return handleOrgCommand(os.Args[2:])
	case "team":
		return handleTeamCommand(os.Args[2:])
	case "chat":
		return handleChatCommand(os.Args[2:])
	case "code":
		// Code mode is intentionally ungated: it runs fully in-process
		// against the host filesystem and never contacts a platform, so it
		// works whether or not the user is logged in.
		return handleCodeCommand(os.Args[2:])
	case "sessions":
		return handleSessionsCommand(os.Args[2:])
	case "flows", "agents": // "agents" is a hidden alias for backwards compatibility
		return handleFlowsCommand(os.Args[2:])
	case "tap":
		mustNotBeRemote("tap")
		return handleTapCommand(os.Args[2:])
	case "setup":
		mustNotBeRemote("setup")
		return handleSetupCommand()
	case "config":
		return handleConfigCommand(os.Args[2:])
	case "tools":
		return handleToolsCommand(os.Args[2:])
	case "memory":
		mustNotBeRemote("memory")
		return handleMemoryCommand(os.Args[2:])
	case "daemon":
		mustNotBeRemote("daemon")
		return handleDaemonCommand(os.Args[2:])
	case "channels":
		mustNotBeRemote("channels")
		return handleChannelsCommand(os.Args[2:])
	case "scheduler":
		return handleSchedulerCommand(os.Args[2:])
	case "fleet":
		return handleFleetCommand(os.Args[2:])
	case "credential", "credentials":
		mustNotBeRemote("credential")
		return handleCredentialCommand(os.Args[2:])
	case "skills":
		mustBeRemote("skills")
		return handleSkillsCommand(os.Args[2:])
	case "drill", "test":
		mustBeRemote("drill")
		return handleDrillCommand(os.Args[2:])
	case "sandbox":
		mustNotBeRemote("sandbox")
		return handleSandboxCommand(os.Args[2:])
	case "node":
		return handleNodeCommand(os.Args[2:])
	case "demo":
		return handleDemoCommand(os.Args[2:])
	case "platform":
		mustNotBeRemote("platform")
		return handlePlatformCommand(os.Args[2:])
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", command)
	}
}

func bareCodeTTYStatus() (stdinTTY, stdoutTTY bool) {
	return term.IsTerminal(int(os.Stdin.Fd())),
		term.IsTerminal(int(os.Stdout.Fd()))
}

func shouldLaunchBareCodeWith(stdinTTY, stdoutTTY bool) bool {
	return stdinTTY && stdoutTTY
}

func bareCodeUnavailableHint(stdinTTY, stdoutTTY bool) string {
	if stdinTTY && stdoutTTY {
		return ""
	}
	return "hint: interactive code mode requires a terminal. Run 'astonish code' from an interactive TTY."
}

func printUsage() {
	fmt.Println("usage: astonish [-h] [-v] {code,chat,login,logout,status,org,team,sessions,flows,...} ...")
	fmt.Println("")
	fmt.Println("positional arguments:")
	fmt.Println("  {code,chat,sessions,flows,tap,daemon,channels,scheduler,fleet,credential,skills,sandbox,drill,config,setup,tools,memory,platform}")
	fmt.Println("                        Astonish CLI commands")
	fmt.Println("    code                Local coding tool (in-process, host filesystem) [default]")
	fmt.Println("    chat                Start an interactive chat session (requires login)")
	fmt.Println("    login               Connect to a remote Astonish server")
	fmt.Println("    logout              Disconnect from the remote server")
	fmt.Println("    status              Show connection mode and status")
	fmt.Println("    org                 Manage organizations (remote mode)")
	fmt.Println("    team                Manage teams (remote mode)")
	fmt.Println("    sessions            Manage persistent sessions")
	fmt.Println("    flows               Design and run AI flows")
	fmt.Println("    tap                 Manage extension repositories")
	fmt.Println("    daemon              Manage the background daemon service")
	fmt.Println("    channels            Manage communication channels")
	fmt.Println("    scheduler           Manage scheduled jobs")
	fmt.Println("    fleet               Manage fleet plans and agent teams")
	fmt.Println("    credential          Manage the encrypted credential store")
	fmt.Println("    skills              Manage CLI tool skill guides")
	fmt.Println("    sandbox             Manage session container isolation")
	fmt.Println("    drill               Run deterministic drill suites")
	fmt.Println("    config              Manage configuration")
	fmt.Println("    setup               Run interactive setup")
	fmt.Println("    tools               Manage MCP tools")
	fmt.Println("    memory              Manage semantic memory and knowledge")
	fmt.Println("    platform            Manage the multi-tenant platform")
	fmt.Println("")
	fmt.Println("options:")
	fmt.Println("  -h, --help            show this help message and exit")
	fmt.Println("  -v, --version         show version information and exit")
}

// mustNotBeRemote exits with an error if the CLI is in remote mode.
// Some commands (daemon, sandbox, etc.) only make sense locally.
func mustNotBeRemote(cmd string) {
	if !client.IsRemoteMode() {
		return
	}
	cfg, _ := client.LoadRemoteConfig()
	url := "a remote server"
	if cfg != nil {
		url = cfg.URL
	}
	fmt.Fprintf(os.Stderr, "Error: '%s' is not available in remote mode.\n", cmd)
	fmt.Fprintf(os.Stderr, "You are connected to %s.\n", url)
	fmt.Fprintf(os.Stderr, "Use 'astonish logout' to disconnect.\n")
	os.Exit(1)
}

// mustBeRemote exits with an error if the CLI is not connected to a remote server.
// Skills now require remote mode (org/team scope) because they are stored in the
// platform database and require an authenticated user to resolve org membership
// and admin privileges.
func mustBeRemote(cmd string) {
	if client.IsRemoteMode() {
		return
	}
	fmt.Fprintf(os.Stderr, "Error: '%s' is only available when connected to a remote server.\n", cmd)
	fmt.Fprintf(os.Stderr, "Run 'astonish login' first.\n")
	os.Exit(1)
}

type updateCheckData struct {
	LastCheck time.Time `json:"lastCheck"`
}

func checkForUpdates() {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	astonishDir := filepath.Join(configDir, "astonish")
	updateFile := filepath.Join(astonishDir, "update_check.json")

	var lastCheck updateCheckData

	// Read last check time
	if data, err := os.ReadFile(updateFile); err == nil {
		json.Unmarshal(data, &lastCheck)
	}

	// Only check every 4 hours
	if time.Since(lastCheck.LastCheck) < 4*time.Hour {
		return
	}

	// Perform update check
	resp, err := http.Get("https://api.github.com/repos/SAP/astonish/releases/latest")
	if err != nil {
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var result struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return
	}

	// Update last check time
	os.MkdirAll(astonishDir, 0755)
	lastCheck.LastCheck = time.Now()
	if data, err := json.Marshal(lastCheck); err == nil {
		os.WriteFile(updateFile, data, 0644)
	}

	// Compare versions (remove 'v' prefix and trim whitespace)
	current := strings.TrimSpace(version.GetVersion())
	if len(current) > 0 && current[0] == 'v' {
		current = current[1:]
	}
	current = strings.TrimSpace(current)

	latest := strings.TrimSpace(result.TagName)
	if len(latest) > 0 && latest[0] == 'v' {
		latest = latest[1:]
	}
	latest = strings.TrimSpace(latest)

	// Skip if running development version
	if current == "" || current == "dev" {
		return
	}

	// Use semantic version comparison
	if isNewerVersion(latest, current) {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintf(os.Stderr, "\033[93mA new version of Astonish is available: %s\033[0m\n", result.TagName)
		fmt.Fprintf(os.Stderr, "\033[93mRun \033[1mbrew upgrade SAP/astonish/astonish\033[0m\033[93m to update.\033[0m\n")
		fmt.Fprintln(os.Stderr)
	}
}

// isNewerVersion returns true when latest is a strictly greater semver than
// current. Pre-release suffixes (e.g. "-beta.1") on current are handled:
// a pre-release is considered older than the same base version without a
// suffix, but newer than any version with a smaller base.
func isNewerVersion(latest, current string) bool {
	// Non-semver current (e.g. "dev", "") — never suggest an update.
	c0 := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(current), "v"))
	if c0 == "" || (c0[0] < '0' || c0[0] > '9') {
		return false
	}
	// Beta builds — never suggest an update to avoid noise.
	if strings.Contains(strings.ToLower(c0), "beta") {
		return false
	}

	type semver struct {
		major, minor, patch int
		pre                 string
	}
	parse := func(v string) semver {
		v = strings.TrimSpace(v)
		v = strings.TrimPrefix(v, "v")
		var s semver
		parts := strings.SplitN(v, ".", 3)
		if len(parts) > 0 {
			s.major, _ = strconv.Atoi(parts[0])
		}
		if len(parts) > 1 {
			s.minor, _ = strconv.Atoi(parts[1])
		}
		if len(parts) > 2 {
			patchStr := parts[2]
			if idx := strings.Index(patchStr, "-"); idx >= 0 {
				s.pre = patchStr[idx+1:]
				patchStr = patchStr[:idx]
			}
			s.patch, _ = strconv.Atoi(patchStr)
		}
		return s
	}

	l := parse(latest)
	c := parse(current)

	if l.major != c.major {
		return l.major > c.major
	}
	if l.minor != c.minor {
		return l.minor > c.minor
	}
	if l.patch != c.patch {
		return l.patch > c.patch
	}
	// Same base version: stable (no pre-release) is newer than pre-release.
	if c.pre != "" && l.pre == "" {
		return true
	}
	return false
}
