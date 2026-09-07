package sandbox

// DefaultBaseImage is the default image used for the @base template.
const DefaultBaseImage = "ubuntu/24.04"

// OverlaySentinelPath is a file written into the template during setup.
// Its presence inside a running container verifies the overlay template layer
// is properly mounted. If missing, the overlay is stale or broken.
const OverlaySentinelPath = "/etc/astonish-overlay-ok"

// CoreTools are the tools installed in the @base template during setup.
var CoreTools = []string{
	"git",
	"curl",
	"wget",
	"jq",
	"unzip",
	"build-essential",
	"socat",
	"ripgrep",
	"docker.io",
}

// CoreToolInstallCommands returns the commands to install core tools in a template.
// These run inside the container after creation. Docker is included as a core
// tool because it's required for containerized MCP servers (stdio transport).
func CoreToolInstallCommands() [][]string {
	return [][]string{
		// Enable the 'universe' component on Ubuntu (Noble). Many packages
		// needed by KasmVNC (libswitch-perl, libhash-merge-simple-perl, etc.)
		// live in universe, which is not enabled by default in Incus base images.
		// This is a safe no-op on Debian (file does not exist → both commands
		// fail → || true).
		{"sh", "-c", "grep -q 'Components:.*universe' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || sed -i '/^Components:/ s/$/ universe/' /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || true"},
		{"apt-get", "update"},
		{"apt-get", "install", "-y",
			"git", "curl", "wget", "jq", "unzip", "build-essential",
			"python3", "python3-pip", "python3-venv",
			"ca-certificates", "gnupg",
			// socat — required for exec-based TCP tunneling to container services
			// (browser CDP, VNC proxy, sandbox HTTP proxy)
			"socat",
			// ffmpeg — standard multimedia tool (browser session recording, media processing)
			"ffmpeg",
			// ripgrep — fast code search used by grep_search and find_files tools
			"ripgrep",
			// Docker runtime (daemon + CLI + containerd) — required for
			// containerized MCP servers and Docker-based workflows
			"docker.io",
		},
		// Remove apparmor — cannot work inside nested LXC containers
		// and blocks Docker from starting containers
		{"apt-get", "remove", "-y", "apparmor"},
		// Install Node.js via NodeSource
		{"sh", "-c", "curl -fsSL https://deb.nodesource.com/setup_22.x | bash -"},
		{"apt-get", "install", "-y", "nodejs"},
		// Install uv (Python package manager) — provides uvx
		{"sh", "-c", "curl -LsSf https://astral.sh/uv/install.sh | sh"},
		// Write sentinel file used by container health checks to verify the
		// template layer is visible through the overlay. If this file is
		// missing at runtime, the overlay mount is stale or broken.
		{"sh", "-c", "echo 'astonish-template-ready' > /etc/astonish-overlay-ok"},
		// Clean up apt cache
		{"apt-get", "clean"},
		// Use sh -c so the shell expands the glob (shellJoin would single-quote
		// the * and turn this into a literal filename match).
		{"sh", "-c", "rm -rf /var/lib/apt/lists/*"},
	}
}

// OptionalTool describes an optional tool that can be installed into the base template.
type OptionalTool struct {
	// ID is the unique identifier for this tool (used in BaseTemplateOptions).
	ID string
	// Name is the display name shown in the setup prompt.
	Name string
	// Description is a short explanation of what the tool does and why it's useful.
	Description string
	// URL is a link to the tool's homepage or docs.
	URL string
	// InstallCommands returns the commands to install this tool inside a container.
	InstallCommands func() [][]string
	// Recommended indicates this tool should be pre-selected / promoted during setup.
	Recommended bool
	// RequiresNesting indicates this tool needs security.nesting=true on containers
	// (e.g., Docker daemon needs to create its own namespaces and cgroups).
	RequiresNesting bool
}

// OptionalTools returns the catalog of optional tools available for installation
// into the base template. The order here is the order they are presented.
// Note: Docker is NOT optional — it's installed as a core tool because it's
// required for containerized MCP servers (stdio transport).
// Currently empty; the catalog remains so install UX can grow without rewiring.
func OptionalTools() []OptionalTool {
	return nil
}

// BaseTemplateOptions configures which optional tools to install in the base template.
type BaseTemplateOptions struct {
	// InstallTools maps optional tool IDs to true if they should be installed.
	InstallTools map[string]bool

	// BrowserEngine is the detected browser engine ("default", "cloakbrowser",
	// "custom", "remote"). When set to a container-compatible engine ("default"
	// or "cloakbrowser"), browser packages (Chromium/CloakBrowser, KasmVNC,
	// socat, X11 deps) are included in the base template. Empty or incompatible
	// engines skip browser installation — the browser runs on the host instead.
	BrowserEngine string

	// ProgressFunc, when non-nil, receives progress messages instead of
	// printing them to stdout. This allows callers (e.g. API handlers)
	// to stream progress to clients.
	ProgressFunc func(string)
}

// DefaultBaseTemplateOptions returns options with no optional tools selected.
func DefaultBaseTemplateOptions() BaseTemplateOptions {
	return BaseTemplateOptions{
		InstallTools: make(map[string]bool),
	}
}

// BinaryDestPath is where the astonish binary is placed inside templates/containers.
const BinaryDestPath = "/usr/local/bin/astonish"

// TreeSitterLibraryDestPath is where code intelligence loads its native parser.
const TreeSitterLibraryDestPath = "/usr/lib/astonish/libastonish-treesitter.so"
