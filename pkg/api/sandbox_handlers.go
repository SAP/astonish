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

	"os"
	"path/filepath"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	sboxdocker "github.com/SAP/astonish/pkg/sandbox/docker"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/gorilla/mux"
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

	backendKind := "docker"
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

	default: // docker OverlayFS
		fillDockerStatus(&resp)
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

	_ = req
	b, err := openStudioDocker()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if overlayLayerExists(b, sandbox.BaseTemplateID) {
		respondError(w, http.StatusConflict, "base overlay layer already exists")
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

	SendSSE(w, flusher, "progress", map[string]string{"message": fmt.Sprintf("Pulling sandbox image %s...", b.SandboxImage())})
	if err := b.SeedBaseLayerFromImage(r.Context()); err != nil {
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

	backendKind := "docker"
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

	default: // docker OverlayFS
		fillDockerDetails(&resp)
	}

	// Backward compat: mirror runtimeAvailable into deprecated field.
	resp.IncusAvailable = resp.RuntimeAvailable

	respondJSON(w, http.StatusOK, resp)
}

// SandboxContainerListHandler handles GET /api/sandbox/containers.
// Lists all session containers and identifies orphans.
func SandboxContainerListHandler(w http.ResponseWriter, r *http.Request) {
	appCfg, _ := config.LoadAppConfig()
	b, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if cleanup != nil {
		defer cleanup()
	}

	sessRegistry, err := sandboxSessionRegistryForRequest(r)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load session registry: "+err.Error())
		return
	}

	sessions, listErr := b.ListSessions(r.Context(), sandbox.SessionFilter{})
	if listErr != nil {
		respondError(w, http.StatusInternalServerError, "failed to list sessions: "+listErr.Error())
		return
	}
	byID := map[string]*sandbox.Session{}
	for _, s := range sessions {
		byID[s.SessionID] = s
	}

	entries := sessRegistry.List()
	containers := make([]ContainerInfo, 0, len(entries))
	registeredIDs := map[string]bool{}
	for _, e := range entries {
		registeredIDs[e.SessionID] = true
		status := "missing"
		if sess := byID[e.SessionID]; sess != nil {
			status = string(sess.State)
			if sess.BackendRef != "" {
				e.ContainerName = sess.BackendRef
			}
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

	var orphans []string
	for _, s := range sessions {
		if !registeredIDs[s.SessionID] {
			orphans = append(orphans, s.BackendRef)
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

	appCfg, _ := config.LoadAppConfig()
	b, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if cleanup != nil {
		defer cleanup()
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

	if err := b.DestroySession(r.Context(), sessionID); err != nil {
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

	resp.ContainerName = "template-" + strings.TrimPrefix(name, "@")
	resp.ContainerStatus = "missing"
	if b, err := openStudioDocker(); err == nil {
		if overlayLayerExists(b, name) || overlayLayerExists(b, sandbox.BaseTemplateID) && (name == "base" || name == sandbox.BaseTemplateID) {
			resp.SnapshotReady = overlayLayerExists(b, name) || name == "base" || name == sandbox.BaseTemplateID
		}
		if sess, sErr := resolveStudioDockerSession(r.Context(), b, resp.ContainerName); sErr == nil {
			resp.ContainerName = sess.BackendRef
			resp.ContainerStatus = string(sess.State)
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

	b, err := openStudioDocker()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !overlayLayerExists(b, sandbox.BaseTemplateID) {
		respondError(w, http.StatusServiceUnavailable, "base overlay layer is missing; run sandbox init first")
		return
	}
	tplRegistry, err := sandbox.NewTemplateRegistry()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to load template registry: "+err.Error())
		return
	}
	if tplRegistry.Exists(req.Name) {
		respondError(w, http.StatusConflict, "template already exists: "+req.Name)
		return
	}
	sessionID := "template-" + req.Name
	sess, err := b.CreateSession(r.Context(), sandbox.SessionSpec{
		SessionID:  sessionID,
		Type:       sandbox.SessionTypeChat,
		TemplateID: sandbox.BaseTemplateID,
		LayerChain: []string{sandbox.BaseTemplateID},
		Labels:     map[string]string{"astonish.io/purpose": "template"},
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to create template: "+err.Error())
		return
	}
	if err := b.WaitForSessionReady(r.Context(), sess.SessionID); err != nil {
		respondError(w, http.StatusInternalServerError, "template session not ready: "+err.Error())
		return
	}
	meta := &sandbox.TemplateMeta{
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   time.Now().UTC(),
		BasedOn:     sandbox.BaseTemplateID,
	}
	if err := tplRegistry.Add(meta); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to register template: "+err.Error())
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

	b, err := openStudioDocker()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if sess, sErr := resolveStudioDockerSession(r.Context(), b, "template-"+name); sErr == nil {
		_ = b.DestroySession(r.Context(), sess.SessionID)
	}
	if err := b.DeleteTemplate(r.Context(), name, true); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to delete template: "+err.Error())
		return
	}
	if tplRegistry, err := sandbox.NewTemplateRegistry(); err == nil {
		_ = tplRegistry.Remove(name)
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

	b, err := openStudioDocker()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	sess, err := resolveStudioDockerSession(r.Context(), b, "template-"+name)
	if err != nil {
		respondError(w, http.StatusNotFound, "template session is not running: "+err.Error())
		return
	}
	art, err := b.SaveSessionAsTemplate(r.Context(), sess.SessionID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to snapshot template: "+err.Error())
		return
	}
	if err := b.AliasLayer(name, art.LayerID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to name template layer: "+err.Error())
		return
	}
	if tplRegistry, err := sandbox.NewTemplateRegistry(); err == nil {
		meta := tplRegistry.Get(name)
		if meta == nil {
			meta = &sandbox.TemplateMeta{Name: name, CreatedAt: time.Now().UTC(), BasedOn: sandbox.BaseTemplateID}
		}
		meta.SnapshotAt = time.Now().UTC()
		_ = tplRegistry.Add(meta)
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

	b, err := openStudioDocker()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	sess, err := resolveStudioDockerSession(r.Context(), b, "template-"+name)
	if err != nil {
		sess, err = b.CreateSession(r.Context(), sandbox.SessionSpec{
			SessionID:  "template-" + name,
			Type:       sandbox.SessionTypeChat,
			TemplateID: name,
			LayerChain: []string{sandbox.BaseTemplateID, name},
		})
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to start template for promote: "+err.Error())
			return
		}
		if err := b.WaitForSessionReady(r.Context(), sess.SessionID); err != nil {
			respondError(w, http.StatusInternalServerError, "template session not ready: "+err.Error())
			return
		}
	}
	if err := b.ReplaceBaseFromSession(r.Context(), sess.SessionID); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to promote template: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// SandboxRefreshHandler handles POST /api/sandbox/refresh.
// Refreshes all templates with the current astonish binary.
func SandboxRefreshHandler(w http.ResponseWriter, r *http.Request) {
	b, err := openStudioDocker()
	if err != nil {
		respondError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err := b.ReseedBaseLayerFromImage(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to refresh @base: "+err.Error())
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

func openStudioDocker() (*sboxdocker.DockerBackend, error) {
	det := sboxdocker.DetectDocker("")
	if !det.Available {
		reason := det.Reason
		if reason == "" {
			reason = "docker daemon is not reachable"
		}
		return nil, fmt.Errorf("docker is not available: %s", reason)
	}
	return sboxdocker.Open()
}

func overlayLayerExists(b *sboxdocker.DockerBackend, name string) bool {
	if b == nil || name == "" {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(b.LayersDir(), name, "rootfs"))
	return err == nil && len(entries) > 0
}

func resolveStudioDockerSession(ctx context.Context, b *sboxdocker.DockerBackend, identifier string) (*sandbox.Session, error) {
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
		return nil, fmt.Errorf("no container found for %q", identifier)
	}
	return match, nil
}

func fillDockerStatus(resp *SandboxStatusResponse) {
	resp.Platform = "docker"
	det := sboxdocker.DetectDocker("")
	if !det.Available {
		resp.RuntimeAvailable = false
		resp.Reason = det.Reason
		return
	}
	b, err := sboxdocker.Open()
	if err != nil {
		resp.RuntimeAvailable = false
		resp.Reason = err.Error()
		return
	}
	resp.RuntimeAvailable = true
	resp.BaseTemplateExists = overlayLayerExists(b, sandbox.BaseTemplateID)
}

func fillDockerDetails(resp *SandboxDetailResponse) {
	resp.Platform = "docker"
	resp.StorageBackend = "overlay"
	det := sboxdocker.DetectDocker("")
	if !det.Available {
		resp.RuntimeAvailable = false
		resp.Reason = det.Reason
		return
	}
	b, err := sboxdocker.Open()
	if err != nil {
		resp.RuntimeAvailable = false
		resp.Reason = err.Error()
		return
	}
	resp.RuntimeAvailable = true
	resp.IncusVersion = det.Version
	resp.BaseTemplateExists = overlayLayerExists(b, sandbox.BaseTemplateID)
	resp.OverlayReady = resp.BaseTemplateExists
	if health, hErr := b.Health(context.Background()); hErr == nil && health != nil && health.Details != nil {
		resp.ServerVersion = health.Details["docker_version"]
	}
	if tplRegistry, err := sandbox.NewTemplateRegistry(); err == nil {
		resp.TemplateCount = len(tplRegistry.List())
	}
	if sessions, err := b.ListSessions(context.Background(), sandbox.SessionFilter{}); err == nil {
		resp.ContainerCount = len(sessions)
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
