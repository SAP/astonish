package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	incus "github.com/SAP/astonish/pkg/sandbox/incus"
	persistentsession "github.com/SAP/astonish/pkg/session"
)

// validTemplateName matches only safe template names: lowercase alphanumeric,
// hyphens, and underscores, 1-64 chars. This prevents shell metacharacters and
// path traversal sequences from reaching exec.Command via sandbox operations.
var validTemplateName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// --- Existing types ---

// SandboxStatusResponse is the JSON response for GET /api/sandbox/status.
type SandboxStatusResponse struct {
	Backend            string `json:"backend"`
	Platform           string `json:"platform"`
	Reason             string `json:"reason,omitempty"`
	SandboxEnabled     bool   `json:"sandboxEnabled"`
	RuntimeAvailable   bool   `json:"runtimeAvailable"`
	BaseTemplateExists bool   `json:"baseTemplateExists"`
	// Deprecated: use RuntimeAvailable. Kept for backward-compat with older UIs.
	IncusAvailable bool `json:"incusAvailable"`
}

// SandboxOptionalToolResponse represents one optional tool for the UI.
type SandboxOptionalToolResponse struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	URL             string `json:"url"`
	Recommended     bool   `json:"recommended"`
	RequiresNesting bool   `json:"requiresNesting"`
}

// SandboxInitRequest is the JSON body for POST /api/sandbox/init.
type SandboxInitRequest struct {
	InstallTools map[string]bool `json:"installTools"`
}

// --- New types for container/template management ---

// SandboxDetailResponse is the JSON response for GET /api/sandbox/details.
type SandboxDetailResponse struct {
	Backend            string `json:"backend"`
	Platform           string `json:"platform"`
	Reason             string `json:"reason,omitempty"`
	SandboxEnabled     bool   `json:"sandboxEnabled"`
	RuntimeAvailable   bool   `json:"runtimeAvailable"`
	BaseTemplateExists bool   `json:"baseTemplateExists"`
	// Deprecated: use RuntimeAvailable. Kept for backward-compat.
	IncusAvailable bool   `json:"incusAvailable"`
	IncusVersion   string `json:"incus_version,omitempty"`
	StorageBackend string `json:"storage_backend,omitempty"`
	OverlayReady   bool   `json:"overlay_ready"`
	TemplateCount  int    `json:"template_count"`
	ContainerCount int    `json:"container_count"`
	OrphanCount    int    `json:"orphan_count"`
	// K8s-specific fields (populated when backend == "k8s").
	ServerVersion string `json:"server_version,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	OverlayMode   string `json:"overlay_mode,omitempty"`
}

// ContainerInfo represents a session container in the list.
type ContainerInfo struct {
	Name         string            `json:"name"`
	SessionID    string            `json:"session_id"`
	Template     string            `json:"template"`
	Status       string            `json:"status"`
	Created      string            `json:"created"`
	Pinned       bool              `json:"pinned,omitempty"`
	ExposedPorts []int             `json:"exposed_ports,omitempty"`
	HostPorts    map[string]int    `json:"host_ports,omitempty"`
	ProxyHosts   map[string]string `json:"proxy_hosts,omitempty"`
}

// ContainerListResponse is the JSON response for GET /api/sandbox/containers.
type ContainerListResponse struct {
	Containers []ContainerInfo `json:"containers"`
	Orphans    []string        `json:"orphans,omitempty"`
}

// TemplateInfo represents a template in the list.
type TemplateInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Created      string   `json:"created"`
	LastSnapshot string   `json:"last_snapshot,omitempty"`
	FleetPlans   []string `json:"fleet_plans,omitempty"`
}

// TemplateListResponse is the JSON response for GET /api/sandbox/templates.
type TemplateListResponse struct {
	Templates []TemplateInfo `json:"templates"`
}

// TemplateDetailResponse is the JSON response for GET /api/sandbox/templates/{name}.
type TemplateDetailResponse struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Created         string   `json:"created"`
	LastSnapshot    string   `json:"last_snapshot,omitempty"`
	FleetPlans      []string `json:"fleet_plans,omitempty"`
	BasedOn         string   `json:"based_on,omitempty"`
	BinaryHash      string   `json:"binary_hash,omitempty"`
	ContainerName   string   `json:"container_name"`
	ContainerStatus string   `json:"container_status"`
	SnapshotReady   bool     `json:"snapshot_ready"`
}

// CreateTemplateRequest is the JSON body for POST /api/sandbox/templates.
type CreateTemplateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// --- Existing handlers ---

// SandboxStatusHandler handles GET /api/sandbox/status.
func SandboxStatusHandler(w http.ResponseWriter, r *http.Request) {
	appCfg, err := config.LoadAppConfig()
	sandboxEnabled := true
	if err == nil && appCfg != nil {
		sandboxEnabled = sandbox.IsSandboxEnabled(&appCfg.Sandbox)
	}

	var resp SandboxStatusResponse
	resp.SandboxEnabled = sandboxEnabled

	backendKind := "incus"
	if err == nil && appCfg != nil {
		backendKind = appCfg.Sandbox.BackendKind()
	}
	resp.Backend = backendKind

	switch backendKind {
	case "k8s":
		resp.Platform = "kubernetes"
		health := sandboxK8sHealth(appCfg)
		resp.RuntimeAvailable = health.Healthy
		resp.BaseTemplateExists = health.Healthy // seeded when backend is healthy
		if !health.Healthy {
			resp.Reason = health.Reason
		}

	case "openshell":
		resp.Platform = "openshell_gateway"
		health := sandboxOpenShellHealth(appCfg)
		resp.RuntimeAvailable = health.Healthy
		resp.BaseTemplateExists = true // OpenShell has no template system
		if !health.Healthy {
			resp.Reason = health.Reason
		}

	default: // "incus"
		platform, reason := incus.DetectPlatformReason()
		resp.Platform = platformString(platform)
		resp.Reason = reason

		incusAvailable := platform == incus.PlatformLinuxNative || platform == incus.PlatformDockerIncus
		if incusAvailable {
			if platform == incus.PlatformDockerIncus && !incus.IsIncusDockerContainerRunning() {
				incusAvailable = false
			}
		}
		if incusAvailable {
			client, connErr := incus.Connect(platform)
			if connErr == nil {
				incus.SetActivePlatform(platform)
				containerName := incus.TemplateName(incus.BaseTemplate)
				resp.BaseTemplateExists = client.InstanceExists(containerName)
			} else {
				incusAvailable = false
			}
		}
		resp.RuntimeAvailable = incusAvailable
	}

	// Backward compat: mirror runtimeAvailable into deprecated field.
	resp.IncusAvailable = resp.RuntimeAvailable

	respondJSON(w, http.StatusOK, resp)
}

// SandboxOptionalToolsHandler handles GET /api/sandbox/optional-tools.
func SandboxOptionalToolsHandler(w http.ResponseWriter, r *http.Request) {
	tools := sandbox.OptionalTools()
	resp := make([]SandboxOptionalToolResponse, 0, len(tools))
	for _, t := range tools {
		resp = append(resp, SandboxOptionalToolResponse{
			ID:              t.ID,
			Name:            t.Name,
			Description:     strings.ReplaceAll(t.Description, "\n", " "),
			URL:             t.URL,
			Recommended:     t.Recommended,
			RequiresNesting: t.RequiresNesting,
		})
	}

	respondJSON(w, http.StatusOK, map[string]any{"tools": resp})
}

// SandboxInitHandler handles POST /api/sandbox/init.
func SandboxInitHandler(w http.ResponseWriter, r *http.Request) {
	var req SandboxInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}

	platform, reason := incus.DetectPlatformReason()
	if platform == incus.PlatformDockerIncus {
		// Ensure the Docker container is running for init
		if !incus.IsIncusDockerContainerRunning() {
			if err := incus.EnsureIncusDockerContainer(); err != nil {
				respondError(w, http.StatusServiceUnavailable, fmt.Sprintf("failed to start Docker+Incus: %v", err))
				return
			}
		}
	}
	if platform == incus.PlatformUnsupported {
		respondError(w, http.StatusServiceUnavailable, fmt.Sprintf("sandbox unavailable: %s", reason))
		return
	}

	// Store sandbox config for privilege mode detection in container creation.
	appCfg, cfgErr := config.LoadAppConfig()
	if cfgErr == nil && appCfg != nil {
		sandbox.SetSandboxConfig(&appCfg.Sandbox)
	}
	incus.SetActivePlatform(platform)

	client, err := incus.Connect(platform)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to connect to Incus: %v", err))
		return
	}

	containerName := incus.TemplateName(incus.BaseTemplate)
	if client.InstanceExists(containerName) {
		respondError(w, http.StatusConflict, "base template already exists")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	registry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		SendSSE(w, flusher, "error", map[string]string{"error": fmt.Sprintf("failed to create template registry: %v", err)})
		return
	}

	opts := sandbox.BaseTemplateOptions{
		InstallTools: req.InstallTools,
		ProgressFunc: func(msg string) {
			msg = strings.TrimRight(msg, "\n")
			if msg != "" {
				SendSSE(w, flusher, "progress", map[string]string{"message": msg})
			}
		},
	}
	if opts.InstallTools == nil {
		opts.InstallTools = make(map[string]bool)
	}

	// Wire browser engine so browser packages are installed in the base template.
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

	if err := sandbox.InitBaseTemplate(client, registry, opts); err != nil {
		SendSSE(w, flusher, "error", map[string]string{"error": err.Error()})
		return
	}

	SendSSE(w, flusher, "done", map[string]string{"status": "success"})
}

// --- New handlers: Details, Containers, Templates ---

// SandboxDetailsHandler handles GET /api/sandbox/details.
// Returns extended sandbox status including version, storage, counts.
func SandboxDetailsHandler(w http.ResponseWriter, r *http.Request) {
	appCfg, err := config.LoadAppConfig()
	sandboxEnabled := true
	if err == nil && appCfg != nil {
		sandboxEnabled = sandbox.IsSandboxEnabled(&appCfg.Sandbox)
	}

	var resp SandboxDetailResponse
	resp.SandboxEnabled = sandboxEnabled

	backendKind := "incus"
	if err == nil && appCfg != nil {
		backendKind = appCfg.Sandbox.BackendKind()
	}
	resp.Backend = backendKind

	switch backendKind {
	case "k8s":
		resp.Platform = "kubernetes"
		health := sandboxK8sHealth(appCfg)
		resp.RuntimeAvailable = health.Healthy
		resp.BaseTemplateExists = health.Healthy
		if !health.Healthy {
			resp.Reason = health.Reason
		}
		// Populate K8s-specific fields from health details.
		if health.Details != nil {
			resp.ServerVersion = health.Details["server_version"]
			resp.Namespace = health.Details["namespace"]
			resp.OverlayMode = appCfg.Sandbox.Kubernetes.OverlayMode
			if resp.OverlayMode == "" {
				resp.OverlayMode = "fuse"
			}
			resp.StorageBackend = "pvc"
		}
		resp.OverlayReady = health.Healthy

	case "openshell":
		resp.Platform = "openshell_gateway"
		health := sandboxOpenShellHealth(appCfg)
		resp.RuntimeAvailable = health.Healthy
		resp.BaseTemplateExists = true // OpenShell has no template system
		if !health.Healthy {
			resp.Reason = health.Reason
		}
		if health.Details != nil {
			resp.Namespace = health.Details["gateway_addr"]
			resp.StorageBackend = "openshell"
		}

	default: // "incus"
		platform, reason := incus.DetectPlatformReason()
		resp.Platform = platformString(platform)
		resp.Reason = reason

		incusAvailable := platform == incus.PlatformLinuxNative || platform == incus.PlatformDockerIncus
		if platform == incus.PlatformDockerIncus && !incus.IsIncusDockerContainerRunning() {
			incusAvailable = false
		}
		resp.RuntimeAvailable = incusAvailable

		if incusAvailable {
			incus.SetActivePlatform(platform)
			client, connErr := incus.Connect(platform)
			if connErr == nil {
				containerName := incus.TemplateName(incus.BaseTemplate)
				resp.BaseTemplateExists = client.InstanceExists(containerName)

				tplRegistry, tplErr := sandbox.NewTemplateRegistry()
				if tplErr != nil {
					slog.Warn("failed to create template registry", "error", tplErr)
				}
				sessRegistry, sessErr := sandboxSessionRegistryForRequest(r)
				if sessErr != nil {
					slog.Warn("failed to create session registry", "error", sessErr)
				}
				if tplRegistry != nil && sessRegistry != nil {
					status, statusErr := sandbox.Status(client, tplRegistry, sessRegistry)
					if statusErr == nil {
						resp.IncusVersion = status.IncusVersion
						resp.StorageBackend = status.StorageBackend
						resp.OverlayReady = status.OverlayReady
						resp.TemplateCount = status.TemplateCount
						resp.ContainerCount = status.SessionCount
						resp.OrphanCount = status.OrphanCount
					}
				}
			}
		}
	}

	// Backward compat: mirror runtimeAvailable into deprecated field.
	resp.IncusAvailable = resp.RuntimeAvailable

	respondJSON(w, http.StatusOK, resp)
}

// SandboxContainerListHandler handles GET /api/sandbox/containers.
// Lists all session containers and identifies orphans.
func SandboxContainerListHandler(w http.ResponseWriter, r *http.Request) {
	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	// Auto-reap stale entries
	sessRegistry.Reap(client)

	entries := sessRegistry.List()
	containers := make([]ContainerInfo, 0, len(entries))
	for _, e := range entries {
		status := "stopped"
		if !client.InstanceExists(e.ContainerName) {
			status = "missing"
		} else if client.IsRunning(e.ContainerName) {
			status = "running"
		}
		info := ContainerInfo{
			Name:      e.ContainerName,
			SessionID: e.SessionID,
			Template:  e.TemplateName,
			Status:    status,
			Created:   e.CreatedAt.Format("2006-01-02 15:04:05"),
			Pinned:    e.Pinned,
		}
		if len(e.ExposedPorts) > 0 {
			info.ExposedPorts = e.ExposedPorts
			mgr := GetPortProxyManager()
			sr := GetSubdomainRouter()
			hostPorts := make(map[string]int, len(e.ExposedPorts))
			proxyHosts := make(map[string]string, len(e.ExposedPorts))
			for _, port := range e.ExposedPorts {
				portStr := strconv.Itoa(port)
				hp := mgr.GetHostPort(e.ContainerName, port)
				if hp == 0 && status == "running" {
					// Auto-start: listener lost after daemon restart
					var proxyErr error
					hp, proxyErr = mgr.StartProxy(e.ContainerName, port)
					if proxyErr != nil {
						slog.Warn("failed to auto-start proxy", "container", e.ContainerName, "port", port, "error", proxyErr)
					}
				}
				if hp > 0 {
					hostPorts[portStr] = hp
				}

				// Auto-recover subdomain routes from persisted base domain
				if e.BaseDomain != "" {
					hostname := SubdomainHostname(e.ContainerName, port, e.BaseDomain)
					if _, _, ok := sr.Lookup(hostname); !ok && status == "running" {
						sr.RegisterHost(hostname, e.ContainerName, port)
					}
					proxyHosts[portStr] = hostname
				}
			}
			if len(hostPorts) > 0 {
				info.HostPorts = hostPorts
			}
			if len(proxyHosts) > 0 {
				info.ProxyHosts = proxyHosts
			}
		}
		containers = append(containers, info)
	}

	// Find orphans (containers in Incus not in registry)
	registeredNames := make(map[string]bool)
	for _, e := range entries {
		registeredNames[e.ContainerName] = true
	}
	incusContainers, listErr := client.ListSessionContainers()
	if listErr != nil {
		slog.Warn("failed to list session containers from Incus", "error", listErr)
	}
	var orphans []string
	for _, inst := range incusContainers {
		if !registeredNames[inst.Name] {
			orphans = append(orphans, inst.Name)
		}
	}

	respondJSON(w, http.StatusOK, ContainerListResponse{
		Containers: containers,
		Orphans:    orphans,
	})
}

// SandboxContainerDeleteHandler handles DELETE /api/sandbox/containers/{id}.
// Destroys a session container by session ID or container name.
func SandboxContainerDeleteHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "missing container/session id")
		return
	}

	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	// Resolve to session ID (accepts session ID, container name, or prefix)
	sessionID, found := sessRegistry.ResolveSessionID(id)
	if !found {
		respondError(w, http.StatusNotFound, "container not found: "+id)
		return
	}

	if err := sandbox.DestroyForSession(client, sessRegistry, sessionID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to destroy container: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// SandboxPruneHandler handles POST /api/sandbox/prune.
// Prunes orphaned containers whose sessions no longer exist.
func SandboxPruneHandler(w http.ResponseWriter, r *http.Request) {
	appCfg, cfgErr := config.LoadAppConfig()
	if cfgErr != nil {
		respondError(w, http.StatusInternalServerError, "failed to load config: "+cfgErr.Error())
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	// Load existing session IDs from the session store
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
	case sandbox.BackendKindK8s:
		b, cleanup, bErr := sandbox.BackendFromAppConfig(appCfg)
		if bErr != nil {
			respondError(w, http.StatusServiceUnavailable, "backend init: "+bErr.Error())
			return
		}
		if cleanup != nil {
			defer cleanup()
		}
		pruned, pErr := sandbox.PruneOrphansForBackend(r.Context(), b, sessRegistry, existingSessionIDs)
		if pErr != nil {
			respondError(w, http.StatusInternalServerError, "prune failed: "+pErr.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"pruned": pruned})

	default:
		// Incus path
		client, cErr := sandboxConnect()
		if cErr != nil {
			respondError(w, http.StatusServiceUnavailable, cErr.Error())
			return
		}
		pruned, pErr := sandbox.PruneOrphans(client, sessRegistry, existingSessionIDs)
		if pErr != nil {
			respondError(w, http.StatusInternalServerError, "prune failed: "+pErr.Error())
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{"pruned": pruned})
	}
}

// SandboxTemplateListHandler handles GET /api/sandbox/templates.
// Lists all registered templates.
func SandboxTemplateListHandler(w http.ResponseWriter, r *http.Request) {
	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	entries := tplRegistry.List()
	templates := make([]TemplateInfo, 0, len(entries))
	for _, t := range entries {
		info := TemplateInfo{
			Name:        t.Name,
			Description: t.Description,
			Created:     t.CreatedAt.Format("2006-01-02 15:04:05"),
			FleetPlans:  t.FleetPlans,
		}
		if !t.SnapshotAt.IsZero() {
			info.LastSnapshot = t.SnapshotAt.Format("2006-01-02 15:04:05")
		}
		templates = append(templates, info)
	}

	respondJSON(w, http.StatusOK, TemplateListResponse{Templates: templates})
}

// SandboxTemplateInfoHandler handles GET /api/sandbox/templates/{name}.
// Returns detailed information about a single template.
func SandboxTemplateInfoHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		respondError(w, http.StatusBadRequest, "missing template name")
		return
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	meta := tplRegistry.Get(name)
	if meta == nil {
		respondError(w, http.StatusNotFound, "template not found: "+name)
		return
	}

	resp := TemplateDetailResponse{
		Name:        meta.Name,
		Description: meta.Description,
		Created:     meta.CreatedAt.Format("2006-01-02 15:04:05"),
		FleetPlans:  meta.FleetPlans,
		BasedOn:     meta.BasedOn,
	}
	if !meta.SnapshotAt.IsZero() {
		resp.LastSnapshot = meta.SnapshotAt.Format("2006-01-02 15:04:05")
	}
	if meta.BinaryHash != "" && len(meta.BinaryHash) > 16 {
		resp.BinaryHash = meta.BinaryHash[:16] + "..."
	} else {
		resp.BinaryHash = meta.BinaryHash
	}

	// Get live container state from Incus
	containerName := incus.TemplateName(name)
	resp.ContainerName = containerName
	resp.ContainerStatus = "missing"

	client, connErr := sandboxConnect()
	if connErr == nil {
		if client.InstanceExists(containerName) {
			inst, instErr := client.GetInstance(containerName)
			if instErr == nil {
				resp.ContainerStatus = inst.Status
			} else {
				resp.ContainerStatus = "unknown"
			}
			resp.SnapshotReady = client.HasSnapshot(containerName, "snap0")
		}
	}

	respondJSON(w, http.StatusOK, resp)
}

// SandboxTemplateCreateHandler handles POST /api/sandbox/templates.
// Creates a new template from @base.
func SandboxTemplateCreateHandler(w http.ResponseWriter, r *http.Request) {
	var req CreateTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if req.Name == "" {
		respondError(w, http.StatusBadRequest, "template name is required")
		return
	}
	if !validTemplateName.MatchString(req.Name) {
		respondError(w, http.StatusBadRequest, "invalid template name: must be alphanumeric with hyphens/underscores, 1-64 chars")
		return
	}
	if req.Name == "base" {
		respondError(w, http.StatusBadRequest, "cannot create a template named 'base'")
		return
	}

	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	if err := sandbox.CreateTemplate(client, tplRegistry, req.Name, req.Description); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create template: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok", "name": req.Name})
}

// SandboxTemplateDeleteHandler handles DELETE /api/sandbox/templates/{name}.
// Deletes a template (cannot delete @base).
func SandboxTemplateDeleteHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		respondError(w, http.StatusBadRequest, "missing template name")
		return
	}
	if !validTemplateName.MatchString(name) {
		respondError(w, http.StatusBadRequest, "invalid template name")
		return
	}
	if name == "base" {
		respondError(w, http.StatusBadRequest, "cannot delete the base template")
		return
	}

	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	if err := sandbox.DeleteTemplate(client, tplRegistry, name); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete template: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// SandboxTemplateSnapshotHandler handles POST /api/sandbox/templates/{name}/snapshot.
// Snapshots a template, freezing its state for cloning into session containers.
func SandboxTemplateSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		respondError(w, http.StatusBadRequest, "missing template name")
		return
	}
	if !validTemplateName.MatchString(name) {
		respondError(w, http.StatusBadRequest, "invalid template name")
		return
	}

	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	if err := sandbox.SnapshotTemplate(client, tplRegistry, name); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to snapshot template: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// SandboxTemplatePromoteHandler handles POST /api/sandbox/templates/{name}/promote.
// Promotes a template to replace @base.
func SandboxTemplatePromoteHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if name == "" {
		respondError(w, http.StatusBadRequest, "missing template name")
		return
	}
	if !validTemplateName.MatchString(name) {
		respondError(w, http.StatusBadRequest, "invalid template name")
		return
	}
	if name == "base" {
		respondError(w, http.StatusBadRequest, "cannot promote @base to itself")
		return
	}

	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	if err := sandbox.PromoteTemplate(client, tplRegistry, name); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to promote template: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// SandboxRefreshHandler handles POST /api/sandbox/refresh.
// Refreshes all templates with the current astonish binary.
func SandboxRefreshHandler(w http.ResponseWriter, r *http.Request) {
	client, err := sandboxConnect()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}

	if err := sandbox.RefreshAll(client, tplRegistry); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to refresh templates: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// --- Port Exposure ---

// ExposePortRequest is the JSON body for POST /api/sandbox/containers/{id}/expose.
type ExposePortRequest struct {
	Port       int    `json:"port"`
	BaseDomain string `json:"base_domain,omitempty"`
}

// SandboxExposePortHandler handles POST /api/sandbox/containers/{id}/expose.
// Registers a port as accessible through the reverse proxy.
func SandboxExposePortHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "missing container id")
		return
	}

	var req ExposePortRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Port < 1 || req.Port > 65535 {
		respondError(w, http.StatusBadRequest, "port must be between 1 and 65535")
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	// Resolve container name — accept session ID, container name, or prefix
	containerName := resolveContainerName(sessRegistry, id)
	if containerName == "" {
		respondError(w, http.StatusNotFound, fmt.Sprintf("container %q not found", id))
		return
	}

	added, err := sessRegistry.ExposePort(containerName, req.Port)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Start per-port proxy listener
	mgr := GetPortProxyManager()
	hostPort, proxyErr := mgr.StartProxy(containerName, req.Port)

	var proxyErrMsg string
	if proxyErr != nil {
		proxyErrMsg = proxyErr.Error()
		slog.Error("failed to start port listener", "component", "sandbox-proxy", "container", containerName, "port", req.Port, "error", proxyErr)
	}

	// Register subdomain proxy route if base_domain was provided
	var proxyHost string
	if req.BaseDomain != "" {
		// Persist the base domain so it can be recovered after daemon restart
		if err := sessRegistry.SetBaseDomain(containerName, req.BaseDomain); err != nil {
			slog.Warn("failed to set base domain", "container", containerName, "domain", req.BaseDomain, "error", err)
		}

		proxyHost = SubdomainHostname(containerName, req.Port, req.BaseDomain)
		GetSubdomainRouter().RegisterHost(proxyHost, containerName, req.Port)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"added":       added,
		"port":        req.Port,
		"host_port":   hostPort,
		"proxy_host":  proxyHost,
		"proxy_error": proxyErrMsg,
	})
}

// SandboxUnexposePortHandler handles DELETE /api/sandbox/containers/{id}/expose/{port}.
// Removes a port from the reverse proxy access list.
func SandboxUnexposePortHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	portStr := mux.Vars(r)["port"]

	if id == "" || portStr == "" {
		respondError(w, http.StatusBadRequest, "missing container id or port")
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		respondError(w, http.StatusBadRequest, "invalid port number")
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	containerName := resolveContainerName(sessRegistry, id)
	if containerName == "" {
		respondError(w, http.StatusNotFound, fmt.Sprintf("container %q not found", id))
		return
	}

	removed, err := sessRegistry.UnexposePort(containerName, port)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Stop per-port proxy listener
	if removed {
		GetPortProxyManager().StopProxy(containerName, port)

		// Unregister any subdomain proxy route for this container+port
		sr := GetSubdomainRouter()
		for portNum, hostname := range sr.ListForContainer(containerName) {
			if portNum == port {
				sr.UnregisterHost(hostname)
			}
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"removed": removed,
		"port":    port,
	})
}

// SandboxPinContainerHandler handles POST /api/sandbox/containers/{id}/pin.
// Toggles the pinned state of a container.
func SandboxPinContainerHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "missing container id")
		return
	}

	var req struct {
		Pinned bool `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	containerName := resolveContainerName(sessRegistry, id)
	if containerName == "" {
		respondError(w, http.StatusNotFound, fmt.Sprintf("container %q not found", id))
		return
	}

	if err := sessRegistry.SetPinned(containerName, req.Pinned); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"pinned": req.Pinned,
	})
}

// SandboxListExposedPortsHandler handles GET /api/sandbox/containers/{id}/expose.
// Returns the list of exposed ports for a container.
func SandboxListExposedPortsHandler(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	if id == "" {
		respondError(w, http.StatusBadRequest, "missing container id")
		return
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	containerName := resolveContainerName(sessRegistry, id)
	if containerName == "" {
		respondError(w, http.StatusNotFound, fmt.Sprintf("container %q not found", id))
		return
	}

	entry := sessRegistry.GetByContainerName(containerName)
	if entry == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("container %q not found", id))
		return
	}

	ports := entry.ExposedPorts
	if ports == nil {
		ports = []int{}
	}

	mgr := GetPortProxyManager()
	sr := GetSubdomainRouter()
	hostPorts := make(map[string]int, len(ports))
	proxyHosts := make(map[string]string, len(ports))
	for _, p := range ports {
		portStr := strconv.Itoa(p)
		hp := mgr.GetHostPort(containerName, p)
		if hp > 0 {
			hostPorts[portStr] = hp
		}
		// Look up subdomain route
		subdomainHosts := sr.ListForContainer(containerName)
		if hostname, ok := subdomainHosts[p]; ok {
			proxyHosts[portStr] = hostname
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"container":     containerName,
		"exposed_ports": ports,
		"host_ports":    hostPorts,
		"proxy_hosts":   proxyHosts,
	})
}

// resolveContainerName resolves a user-provided identifier to a container name.
// Accepts: session ID, container name, session ID prefix, or container name prefix.
func resolveContainerName(registry *sandbox.SessionRegistry, input string) string {
	// Try exact session ID
	if entry := registry.Get(input); entry != nil {
		return entry.ContainerName
	}
	// Try container name or prefix
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

// --- Helpers ---

// sandboxConnect detects the platform and connects to Incus.
func sandboxConnect() (*incus.IncusClient, error) {
	platform, reason := incus.DetectPlatformReason()
	if platform == incus.PlatformUnsupported {
		return nil, fmt.Errorf("sandbox unavailable: %s", reason)
	}

	// For Docker+Incus, ensure the Docker container is reachable
	if platform == incus.PlatformDockerIncus {
		if !incus.IsIncusDockerContainerRunning() {
			return nil, fmt.Errorf("Docker+Incus container is not running; run 'astonish sandbox init'")
		}
	}

	incus.SetActivePlatform(platform)
	client, err := incus.Connect(platform)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Incus: %w", err)
	}
	return client, nil
}

// platformString converts a sandbox.Platform to a JSON-friendly string.
func platformString(p incus.Platform) string {
	switch p {
	case incus.PlatformLinuxNative:
		return "linux_native"
	case incus.PlatformDockerIncus:
		return "docker_incus"
	default:
		return "unsupported"
	}
}

// sandboxK8sHealth returns a BackendHealth for the K8s sandbox backend.
// It constructs a Backend via the factory, calls Health, and returns the
// result. On any construction error it returns an unhealthy status with
// the error as reason.
func sandboxK8sHealth(appCfg *config.AppConfig) *sandbox.BackendHealth {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    fmt.Sprintf("k8s backend construction failed: %v", err),
			CheckedAt: time.Now().UTC(),
		}
	}

	health, err := b.Health(ctx)
	if err != nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    fmt.Sprintf("k8s health check error: %v", err),
			CheckedAt: time.Now().UTC(),
		}
	}
	return health
}

// sandboxOpenShellHealth returns a BackendHealth for the OpenShell sandbox backend.
// Same pattern as sandboxK8sHealth: constructs the backend via factory, calls Health.
func sandboxOpenShellHealth(appCfg *config.AppConfig) *sandbox.BackendHealth {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	b, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    fmt.Sprintf("openshell backend construction failed: %v", err),
			CheckedAt: time.Now().UTC(),
		}
	}

	health, err := b.Health(ctx)
	if err != nil {
		return &sandbox.BackendHealth{
			Healthy:   false,
			Reason:    fmt.Sprintf("openshell health check error: %v", err),
			CheckedAt: time.Now().UTC(),
		}
	}
	return health
}
