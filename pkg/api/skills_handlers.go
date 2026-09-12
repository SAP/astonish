package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/skills"
	"github.com/SAP/astonish/pkg/store"
	"github.com/gorilla/mux"
)

// validationRateLimit tracks the last validation time per skill to prevent
// abuse (each validation triggers an LLM call with cost implications).
var (
	validationRateMu    sync.Mutex
	validationRateMap   = make(map[string]time.Time) // key: "scope:name"
	validationRateLimit = 60 * time.Second
)

// canValidateSkill checks if a skill can be validated (rate limit not exceeded).
// Returns true if allowed, false if rate-limited.
//
// The key is tenant-scoped to prevent cross-tenant collisions in multi-tenant
// deployments. Empty scope normalizes to "team" (the default). Note that "team"
// and "org" are distinct skill namespaces (different stores, different data), so
// the same skill name in different scopes legitimately needs separate rate slots.
func canValidateSkill(teamSlug, scope, name string) bool {
	if scope == "" {
		scope = "team"
	}
	if teamSlug == "" {
		teamSlug = "_"
	}
	key := teamSlug + ":" + scope + ":" + name
	validationRateMu.Lock()
	defer validationRateMu.Unlock()

	// Prune stale entries (simple inline cleanup — at most once per call)
	if len(validationRateMap) > 1000 {
		now := time.Now()
		for k, v := range validationRateMap {
			if now.Sub(v) > 5*time.Minute {
				delete(validationRateMap, k)
			}
		}
	}

	if last, ok := validationRateMap[key]; ok {
		return time.Since(last) >= validationRateLimit
	}
	return true
}

// recordSkillValidation starts the cooldown after a validation result has been
// successfully persisted. Failed or unavailable validations must be retryable.
func recordSkillValidation(teamSlug, scope, name string) {
	if scope == "" {
		scope = "team"
	}
	if teamSlug == "" {
		teamSlug = "_"
	}
	key := teamSlug + ":" + scope + ":" + name
	validationRateMu.Lock()
	defer validationRateMu.Unlock()
	validationRateMap[key] = time.Now()
}

var skillValidationLLMProvider = getValidationLLMProvider

// SkillListItem represents a skill in the listing response.
type SkillListItem struct {
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Source           string   `json:"source"`
	Scope            string   `json:"scope,omitempty"` // "platform", "org", or "team"
	Eligible         bool     `json:"eligible"`
	Editable         bool     `json:"editable"`
	Missing          []string `json:"missing,omitempty"`
	RequireBins      []string `json:"require_bins,omitempty"`
	RequireEnv       []string `json:"require_env,omitempty"`
	OS               []string `json:"os,omitempty"`
	FilePath         string   `json:"file_path,omitempty"`
	HasDirectory     bool     `json:"has_directory"`
	ValidationStatus string   `json:"validation_status,omitempty"`
}

// SkillContentResponse is the response for GET /api/skills/{name}/content.
type SkillContentResponse struct {
	Name              string                    `json:"name"`
	Description       string                    `json:"description"`
	Source            string                    `json:"source"`
	Scope             string                    `json:"scope,omitempty"`
	Content           string                    `json:"content"`
	RawFile           string                    `json:"raw_file"`
	FilePath          string                    `json:"file_path,omitempty"`
	Editable          bool                      `json:"editable"`
	Files             []SkillFileInfo           `json:"files,omitempty"` // Multi-file support (new)
	ValidationStatus  string                    `json:"validation_status,omitempty"`
	Validation        *skills.ValidationResult  `json:"validation,omitempty"`         // Persisted issues from last validation
	AcknowledgedRisks []skills.AcknowledgedRisk `json:"acknowledged_risks,omitempty"` // Persisted acknowledgments
}

// SkillContentUpdateRequest is the request for PUT /api/skills/{name}/content.
type SkillContentUpdateRequest struct {
	RawFile string `json:"raw_file"`
}

// SkillFileInfo represents metadata about one auxiliary file belonging to a skill.
type SkillFileInfo struct {
	Path         string `json:"path"`
	Filename     string `json:"filename"`
	Size         int64  `json:"size"`
	IsExecutable bool   `json:"is_executable"`
}

// SkillFilesResponse is the response for GET /api/skills/{name}/files
type SkillFilesResponse struct {
	Name  string          `json:"name"`
	Files []SkillFileInfo `json:"files"`
}

// SaveSkillFileRequest is the request body for saving an auxiliary skill file.
type SaveSkillFileRequest struct {
	Content      string `json:"content"`
	IsExecutable bool   `json:"is_executable"`
}

// SkillsListResponse is the response for GET /api/skills.
type SkillsListResponse struct {
	Skills      []SkillListItem `json:"skills"`
	IsTeamAdmin bool            `json:"is_team_admin"`
	IsOrgAdmin  bool            `json:"is_org_admin"`
}

// ListSkillsHandler handles GET /api/skills
//
// Query params:
//   - scope=team: return only team skills
//   - scope=org: return only org skills
//   - scope=platform: return only platform skills (superadmin)
//   - (empty): return merged view (platform + org + team, team overrides org overrides platform)
func ListSkillsHandler(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	svc := store.FromRequest(r)
	if svc != nil && (svc.PlatformSkills != nil || svc.Skills != nil || svc.TeamSkills != nil) {
		// Platform mode: scope-aware listing (only supported mode)
		items, err := listSkillsPlatform(r.Context(), svc, scope)
		if err != nil {
			slog.Error("failed to load skills", "error", err)
			respondError(w, http.StatusInternalServerError, "Failed to load skills")
			return
		}
		resp := SkillsListResponse{
			Skills:      items,
			IsTeamAdmin: IsTeamAdmin(r),
			IsOrgAdmin:  !isPlatformMode(r) || CanManageOrg(GetPlatformUser(r)),
		}
		respondJSON(w, http.StatusOK, resp)
		return
	}

	// No skill stores available (should not happen in platform mode)
	respondJSON(w, http.StatusOK, SkillsListResponse{Skills: []SkillListItem{}})
}

// GetSkillContentHandler handles GET /api/skills/{name}/content
//
// Query params:
//   - scope=team: look in team store only
//   - scope=org: look in org store only
//   - scope=platform: look in platform store only (superadmin)
//   - (empty): try team → org → platform
func GetSkillContentHandler(w http.ResponseWriter, r *http.Request) {
	// Auth: Any authenticated team member can read skill content (consistent
	// with ListSkillFilesHandler/GetSkillFileHandler).
	if GetPlatformUser(r) == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := store.FromRequest(r)
	if svc != nil && (svc.PlatformSkills != nil || svc.Skills != nil || svc.TeamSkills != nil) {
		// Platform mode: scope-aware get
		getSkillContentPlatform(w, r, svc, name, scope)
		return
	}

	// Personal mode removed (v3 platform-only). Only DB-backed skills are supported.
	respondError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
}

// UpdateSkillContentHandler handles PUT /api/skills/{name}/content
//
// Query params:
//   - scope=team: save to team store (requires team admin)
//   - scope=org: save to org store (requires org admin)
//   - scope=platform: save to platform store (requires superadmin)
func UpdateSkillContentHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	var req SkillContentUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	if strings.TrimSpace(req.RawFile) == "" {
		respondError(w, http.StatusBadRequest, "raw_file cannot be empty")
		return
	}

	// Validate the content parses correctly
	parsed, err := skills.ParseSkillFile("validation", []byte(req.RawFile))
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid skill file: "+err.Error())
		return
	}

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		// Platform mode: scope-aware update with auth check
		targetStore := resolveSkillStoreForWrite(w, r, svc, scope)
		if targetStore == nil {
			return // auth error already written
		}
		updateSkillContentPlatform(w, r, targetStore, name, parsed, req.RawFile, scope)
		return
	}

	// Personal mode removed (v3 platform-only).
	respondError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
}

// CreateSkillHandler handles POST /api/skills (create new skill from template)
type CreateSkillRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope,omitempty"` // "team" or "org" (platform mode)
}

// InstallSkillRequest is the request for POST /api/skills/install
type InstallSkillRequest struct {
	Input string `json:"input"` // slug, clawhub:slug, or full URL
}

// InstallSkillResponse is the response for successful ClawHub install via server.
type InstallSkillResponse struct {
	Status           string                   `json:"status"`
	Name             string                   `json:"name"`
	Scope            string                   `json:"scope"`
	FilesSaved       int                      `json:"files_saved"`
	Version          string                   `json:"version,omitempty"`
	Description      string                   `json:"description,omitempty"`
	ValidationStatus string                   `json:"validation_status,omitempty"`
	Validation       *skills.ValidationResult `json:"validation,omitempty"`
}

func CreateSkillHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024) // 16 KiB — only name + scope
	var req CreateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		respondError(w, http.StatusBadRequest, "Skill name is required")
		return
	}

	// Validate name (alphanumeric, hyphens, underscores)
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			respondError(w, http.StatusBadRequest, "Skill name must contain only letters, numbers, hyphens, and underscores")
			return
		}
	}

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		// Platform mode: scope-aware create with auth check
		scope := req.Scope
		if scope == "" {
			scope = "team" // default to team scope for new skills
		}
		targetStore := resolveSkillStoreForWrite(w, r, svc, scope)
		if targetStore == nil {
			return // auth error already written
		}
		createSkillPlatform(w, r, targetStore, name)
		return
	}

	// Personal mode: create on filesystem
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		slog.Warn("failed to load app config", "error", err)
	}
	var skillsCfg config.SkillsConfig
	if appCfg != nil {
		skillsCfg = appCfg.Skills
	}

	userDir := skillsCfg.GetUserSkillsDir()
	if userDir == "" {
		respondError(w, http.StatusInternalServerError, "Cannot determine user skills directory")
		return
	}

	skillDir := filepath.Join(userDir, filepath.Base(name))
	skillFile := filepath.Join(skillDir, "SKILL.md")

	// Defense-in-depth: ensure resolved paths stay within userDir.
	if !strings.HasPrefix(filepath.Clean(skillDir), filepath.Clean(userDir)+string(os.PathSeparator)) {
		respondError(w, http.StatusBadRequest, "Invalid skill name: path traversal detected")
		return
	}

	if _, err := os.Stat(skillFile); err == nil {
		respondError(w, http.StatusConflict, fmt.Sprintf("Skill %q already exists at %s", name, skillFile))
		return
	}

	if err := os.MkdirAll(skillDir, 0755); err != nil {
		slog.Error("failed to create skill directory", "dir", skillDir, "error", err)
		respondError(w, http.StatusInternalServerError, "Failed to create skill directory")
		return
	}

	if err := os.WriteFile(skillFile, []byte(skills.NewSkillTemplate(name)), 0644); err != nil {
		slog.Error("failed to write skill file", "file", skillFile, "error", err)
		respondError(w, http.StatusInternalServerError, "Failed to write skill file")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"file_path": skillFile,
	})
}

// DeleteSkillHandler handles DELETE /api/skills/{name}
//
// Query params:
//   - scope=team: delete from team store (requires team admin)
//   - scope=org: delete from org store (requires org admin)
func DeleteSkillHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		// Platform mode: scope-aware delete with auth check
		targetStore := resolveSkillStoreForWrite(w, r, svc, scope)
		if targetStore == nil {
			return // auth error already written
		}
		deleteSkillPlatform(w, r, targetStore, name, scope)
		return
	}

	// Personal mode removed (v3 platform-only).
	respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found", name))
}

// validateSkillFilePath checks path and filename for traversal and injection attempts.
// Returns true if valid, false (and writes error response) if invalid.
func validateSkillFilePath(w http.ResponseWriter, path, filename string) bool {
	// Length limits
	if len(path) > 255 || len(filename) > 255 {
		respondError(w, http.StatusBadRequest, "path or filename exceeds maximum length (255)")
		return false
	}
	// Null byte injection
	if strings.ContainsRune(path, 0) || strings.ContainsRune(filename, 0) {
		respondError(w, http.StatusBadRequest, "invalid characters in path or filename")
		return false
	}
	// Path traversal and absolute path
	if strings.Contains(path, "..") || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		respondError(w, http.StatusBadRequest, "invalid path: must not contain '..', '\\', or start with '/'")
		return false
	}
	// Filename: no separators or traversal
	if strings.Contains(filename, "/") || strings.Contains(filename, "\\") || strings.Contains(filename, "..") {
		respondError(w, http.StatusBadRequest, "invalid filename: must not contain '/', '\\', or '..'")
		return false
	}
	// Empty filename
	if filename == "" {
		respondError(w, http.StatusBadRequest, "filename must not be empty")
		return false
	}
	return true
}

// ListSkillFilesHandler handles GET /api/skills/{name}/files
func ListSkillFilesHandler(w http.ResponseWriter, r *http.Request) {
	// Auth: Any authenticated team member can read skill files. Skills are
	// available at runtime to all team members, so file content is not more
	// privileged than the skill itself. Write operations require admin (see
	// resolveSkillStoreForWrite).
	if GetPlatformUser(r) == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		var skillStore store.SkillStore
		if scope == "team" && svc.TeamSkills != nil {
			skillStore = svc.TeamSkills
		} else if scope == "org" && svc.Skills != nil {
			skillStore = svc.Skills
		} else if svc.TeamSkills != nil {
			skillStore = svc.TeamSkills
		} else {
			skillStore = svc.Skills
		}

		if skillStore == nil {
			respondError(w, http.StatusNotFound, "no skill store available for scope")
			return
		}

		files, err := skillStore.ListFiles(r.Context(), name)
		if err != nil {
			slog.Error("failed to list skill files", "skill", name, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to list skill files")
			return
		}

		resp := SkillFilesResponse{Name: name}
		for _, f := range files {
			resp.Files = append(resp.Files, SkillFileInfo{
				Path:         f.Path,
				Filename:     f.Filename,
				Size:         f.SizeBytes,
				IsExecutable: f.IsExecutable,
			})
		}
		respondJSON(w, http.StatusOK, resp)
		return
	}

	respondJSON(w, http.StatusOK, SkillFilesResponse{Name: name, Files: []SkillFileInfo{}})
}

// GetSkillFileHandler handles GET /api/skills/{name}/file?path=...&filename=...
func GetSkillFileHandler(w http.ResponseWriter, r *http.Request) {
	// Auth: Any authenticated team member can read skill files (see ListSkillFilesHandler).
	if GetPlatformUser(r) == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	name := mux.Vars(r)["name"]
	path := r.URL.Query().Get("path")
	filename := r.URL.Query().Get("filename")

	if filename == "" {
		respondError(w, http.StatusBadRequest, "filename query parameter is required")
		return
	}
	if !validateSkillFilePath(w, path, filename) {
		return
	}

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		var skillStore store.SkillStore
		scope := r.URL.Query().Get("scope")
		if scope == "team" && svc.TeamSkills != nil {
			skillStore = svc.TeamSkills
		} else if scope == "org" && svc.Skills != nil {
			skillStore = svc.Skills
		} else if svc.TeamSkills != nil {
			skillStore = svc.TeamSkills
		} else {
			skillStore = svc.Skills
		}

		if skillStore == nil {
			respondError(w, http.StatusNotFound, "no skill store available")
			return
		}

		file, err := skillStore.GetFile(r.Context(), name, path, filename)
		if err != nil {
			slog.Error("failed to get skill file", "skill", name, "path", path, "filename", filename, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to get skill file")
			return
		}
		if file == nil {
			respondError(w, http.StatusNotFound, "file not found")
			return
		}

		respondJSON(w, http.StatusOK, file)
		return
	}

	respondError(w, http.StatusNotFound, "skill file not found")
}

// SaveSkillFileHandler handles PUT /api/skills/{name}/file?path=...&filename=...
func SaveSkillFileHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	path := r.URL.Query().Get("path")
	filename := r.URL.Query().Get("filename")

	if filename == "" {
		respondError(w, http.StatusBadRequest, "filename query parameter is required")
		return
	}
	if !validateSkillFilePath(w, path, filename) {
		return
	}

	// Limit request body to 5 MiB to prevent memory exhaustion
	r.Body = http.MaxBytesReader(w, r.Body, 5*1024*1024)

	var req SaveSkillFileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		scope := r.URL.Query().Get("scope")
		targetStore := resolveSkillStoreForWrite(w, r, svc, scope)
		if targetStore == nil {
			return
		}

		file := &store.SkillFile{
			Path:         path,
			Filename:     filename,
			Content:      req.Content,
			IsExecutable: req.IsExecutable,
			SizeBytes:    int64(len(req.Content)),
		}

		if err := targetStore.SaveFile(r.Context(), name, file); err != nil {
			slog.Error("failed to save skill file", "skill", name, "error", err)
			respondError(w, http.StatusInternalServerError, "Failed to save skill file")
			return
		}

		// Invalidate validation status — auxiliary files changed, must re-validate
		if err := targetStore.UpdateValidationStatus(r.Context(), name, skills.ValidationStatusUnknown, ""); err != nil {
			slog.Warn("failed to invalidate validation status after file save", "skill", name, "error", err)
		}

		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	respondError(w, http.StatusNotImplemented, "Saving skill files not supported")
}

// DeleteSkillFileHandler handles DELETE /api/skills/{name}/file?path=...&filename=...
func DeleteSkillFileHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	path := r.URL.Query().Get("path")
	filename := r.URL.Query().Get("filename")

	if filename == "" {
		respondError(w, http.StatusBadRequest, "filename query parameter is required")
		return
	}
	if !validateSkillFilePath(w, path, filename) {
		return
	}

	svc := store.FromRequest(r)
	if svc != nil && (svc.Skills != nil || svc.TeamSkills != nil) {
		scope := r.URL.Query().Get("scope")
		targetStore := resolveSkillStoreForWrite(w, r, svc, scope)
		if targetStore == nil {
			return
		}

		if err := targetStore.DeleteFile(r.Context(), name, path, filename); err != nil {
			slog.Error("failed to delete skill file", "skill", name, "file", filename, "error", err)
			respondError(w, http.StatusInternalServerError, "Failed to delete skill file")
			return
		}

		// Invalidate validation status — auxiliary files changed, must re-validate
		if err := targetStore.UpdateValidationStatus(r.Context(), name, skills.ValidationStatusUnknown, ""); err != nil {
			slog.Warn("failed to invalidate validation status after file delete", "skill", name, "error", err)
		}

		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	respondError(w, http.StatusNotImplemented, "Deleting skill files not supported")
}

// --- Platform mode helpers ---

// resolveSkillStoreForWrite returns the appropriate SkillStore for a write operation
// based on the requested scope. Returns nil and writes an error if auth fails.
func resolveSkillStoreForWrite(w http.ResponseWriter, r *http.Request, svc *store.Services, scope string) store.SkillStore {
	switch scope {
	case "platform":
		if svc.PlatformSkills == nil {
			respondError(w, http.StatusServiceUnavailable, "Platform skills store not available")
			return nil
		}
		if RequirePlatformAdmin(w, r) == nil {
			return nil
		}
		return svc.PlatformSkills
	case "team":
		if svc.TeamSkills == nil {
			respondError(w, http.StatusServiceUnavailable, "Team skills store not available")
			return nil
		}
		if !RequireTeamAdmin(w, r) {
			return nil
		}
		return svc.TeamSkills
	case "org":
		if svc.Skills == nil {
			respondError(w, http.StatusServiceUnavailable, "Org skills store not available")
			return nil
		}
		// Org skills require org admin
		user := GetPlatformUser(r)
		if user == nil {
			respondError(w, http.StatusUnauthorized, "Authentication required")
			return nil
		}
		if !CanManageOrg(user) {
			respondError(w, http.StatusForbidden, "Organization admin access required to manage org skills")
			return nil
		}
		return svc.Skills
	default:
		// No scope specified — default to team
		if svc.TeamSkills == nil {
			respondError(w, http.StatusServiceUnavailable, "Team skills store not available")
			return nil
		}
		if !RequireTeamAdmin(w, r) {
			return nil
		}
		return svc.TeamSkills
	}
}

// listSkillsPlatform loads skills with three-tier merge: platform → org → team.
// Team skills override org skills of the same name; org skills override platform.
func listSkillsPlatform(ctx context.Context, svc *store.Services, scope string) ([]SkillListItem, error) {
	switch scope {
	case "team":
		return listSkillsFromStore(ctx, svc.TeamSkills, "team")
	case "org":
		return listSkillsFromStore(ctx, svc.Skills, "org")
	case "platform":
		return listSkillsFromStore(ctx, svc.PlatformSkills, "platform")
	default:
		return listSkillsMerged(ctx, svc)
	}
}

// listSkillsFromStore lists skills from a single store with given scope label.
func listSkillsFromStore(ctx context.Context, skillStore store.SkillStore, scope string) ([]SkillListItem, error) {
	if skillStore == nil {
		return []SkillListItem{}, nil
	}
	pgSkills, err := skillStore.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load %s skills: %w", scope, err)
	}
	items := make([]SkillListItem, 0, len(pgSkills))
	for _, s := range pgSkills {
		item := storeSkillToListItem(&s)
		item.Scope = scope
		item.Editable = true
		items = append(items, item)
	}
	sortListItems(items)
	return items, nil
}

// listSkillsMerged returns all skills merged: platform → org → team (team wins).
func listSkillsMerged(ctx context.Context, svc *store.Services) ([]SkillListItem, error) {
	byName := make(map[string]SkillListItem)

	// Platform skills (base layer)
	if svc.PlatformSkills != nil {
		platformSkills, err := svc.PlatformSkills.List(ctx)
		if err != nil {
			slog.Warn("failed to load platform skills", "error", err)
		} else {
			for _, s := range platformSkills {
				item := storeSkillToListItem(&s)
				item.Scope = "platform"
				item.Editable = false // Only editable through platform admin
				byName[s.Name] = item
			}
		}
	}

	// Org customs override platform
	if svc.Skills != nil {
		pgSkills, err := svc.Skills.List(ctx)
		if err != nil {
			slog.Warn("failed to load org skills", "error", err)
		} else {
			for _, s := range pgSkills {
				item := storeSkillToListItem(&s)
				item.Scope = "org"
				item.Editable = true
				byName[s.Name] = item
			}
		}
	}

	// Team customs override org and platform
	if svc.TeamSkills != nil {
		teamSkills, err := svc.TeamSkills.List(ctx)
		if err != nil {
			slog.Warn("failed to load team skills", "error", err)
		} else {
			for _, s := range teamSkills {
				item := storeSkillToListItem(&s)
				item.Scope = "team"
				item.Editable = true
				byName[s.Name] = item
			}
		}
	}

	items := make([]SkillListItem, 0, len(byName))
	for _, item := range byName {
		items = append(items, item)
	}
	sortListItems(items)
	return items, nil
}

// persistedValidation extracts the validation result from a skill's stored meta.
// Returns nil if no persisted issues exist.
func persistedValidation(skill *store.Skill) *skills.ValidationResult {
	if skill.ValidationMeta == "" {
		return nil
	}
	var meta skills.ValidationMeta
	if err := json.Unmarshal([]byte(skill.ValidationMeta), &meta); err != nil {
		return nil
	}
	if len(meta.Issues) == 0 {
		return nil
	}
	return &skills.ValidationResult{Issues: meta.Issues}
}

// persistedAcks extracts acknowledged risks from a skill's stored meta.
func persistedAcks(skill *store.Skill) []skills.AcknowledgedRisk {
	if skill.ValidationMeta == "" {
		return nil
	}
	var meta skills.ValidationMeta
	if err := json.Unmarshal([]byte(skill.ValidationMeta), &meta); err != nil {
		return nil
	}
	return meta.AcknowledgedRisks
}

// getSkillContentPlatform retrieves a skill's content with scope-aware resolution.
func getSkillContentPlatform(w http.ResponseWriter, r *http.Request, svc *store.Services, name, scope string) {
	switch scope {
	case "team":
		if svc.TeamSkills == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found in team", name))
			return
		}
		skill, err := svc.TeamSkills.Get(r.Context(), name)
		if err != nil || skill == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found in team", name))
			return
		}
		respondJSON(w, http.StatusOK, SkillContentResponse{
			Name:              skill.Name,
			Description:       skill.Description,
			Source:            "custom",
			Scope:             "team",
			Content:           extractBody(skill.Content),
			RawFile:           skill.Content,
			Editable:          true,
			ValidationStatus:  skill.ValidationStatus,
			Validation:        persistedValidation(skill),
			AcknowledgedRisks: persistedAcks(skill),
		})

	case "org":
		if svc.Skills == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found in org", name))
			return
		}
		skill, err := svc.Skills.Get(r.Context(), name)
		if err != nil || skill == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found in org", name))
			return
		}
		respondJSON(w, http.StatusOK, SkillContentResponse{
			Name:              skill.Name,
			Description:       skill.Description,
			Source:            "custom",
			Scope:             "org",
			Content:           extractBody(skill.Content),
			RawFile:           skill.Content,
			Editable:          !isPlatformMode(r) || CanManageOrg(GetPlatformUser(r)),
			ValidationStatus:  skill.ValidationStatus,
			Validation:        persistedValidation(skill),
			AcknowledgedRisks: persistedAcks(skill),
		})

	case "platform":
		if svc.PlatformSkills == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found in platform", name))
			return
		}
		skill, err := svc.PlatformSkills.Get(r.Context(), name)
		if err != nil || skill == nil {
			respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found in platform", name))
			return
		}
		respondJSON(w, http.StatusOK, SkillContentResponse{
			Name:              skill.Name,
			Description:       skill.Description,
			Source:            "custom",
			Scope:             "platform",
			Content:           extractBody(skill.Content),
			RawFile:           skill.Content,
			Editable:          IsPlatformAdmin(GetPlatformUser(r)),
			ValidationStatus:  skill.ValidationStatus,
			Validation:        persistedValidation(skill),
			AcknowledgedRisks: persistedAcks(skill),
		})

	default:
		// No scope: try team → org → platform
		if svc.TeamSkills != nil {
			if skill, err := svc.TeamSkills.Get(r.Context(), name); err == nil && skill != nil {
				respondJSON(w, http.StatusOK, SkillContentResponse{
					Name:              skill.Name,
					Description:       skill.Description,
					Source:            "custom",
					Scope:             "team",
					Content:           extractBody(skill.Content),
					RawFile:           skill.Content,
					Editable:          IsTeamAdmin(r),
					ValidationStatus:  skill.ValidationStatus,
					Validation:        persistedValidation(skill),
					AcknowledgedRisks: persistedAcks(skill),
				})
				return
			}
		}

		if svc.Skills != nil {
			if skill, err := svc.Skills.Get(r.Context(), name); err == nil && skill != nil {
				respondJSON(w, http.StatusOK, SkillContentResponse{
					Name:              skill.Name,
					Description:       skill.Description,
					Source:            "custom",
					Scope:             "org",
					Content:           extractBody(skill.Content),
					RawFile:           skill.Content,
					Editable:          !isPlatformMode(r) || CanManageOrg(GetPlatformUser(r)),
					ValidationStatus:  skill.ValidationStatus,
					Validation:        persistedValidation(skill),
					AcknowledgedRisks: persistedAcks(skill),
				})
				return
			}
		}

		// Fall back to platform store
		if svc.PlatformSkills != nil {
			if skill, err := svc.PlatformSkills.Get(r.Context(), name); err == nil && skill != nil {
				respondJSON(w, http.StatusOK, SkillContentResponse{
					Name:              skill.Name,
					Description:       skill.Description,
					Source:            "custom",
					Scope:             "platform",
					Content:           extractBody(skill.Content),
					RawFile:           skill.Content,
					Editable:          false,
					ValidationStatus:  skill.ValidationStatus,
					Validation:        persistedValidation(skill),
					AcknowledgedRisks: persistedAcks(skill),
				})
				return
			}
		}

		respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found", name))
	}
}

// updateSkillContentPlatform updates a skill in the given store.
// Save ALWAYS succeeds (content is preserved). Validation runs as a post-process
// to determine whether the skill is usable at runtime. Issues are persisted in
// validation_meta so the UI can show them without re-running the LLM.
func updateSkillContentPlatform(w http.ResponseWriter, r *http.Request, targetStore store.SkillStore, name string, parsed *skills.Skill, rawFile, scope string) {
	userID := ""
	if pu := GetPlatformUser(r); pu != nil {
		userID = pu.ID
	}

	// Load existing skill to check content hash for ack carry-forward
	existingSkill, _ := targetStore.Get(r.Context(), name)
	var existingMeta *skills.ValidationMeta
	if existingSkill != nil && existingSkill.ValidationMeta != "" {
		var m skills.ValidationMeta
		if err := json.Unmarshal([]byte(existingSkill.ValidationMeta), &m); err == nil {
			existingMeta = &m
		}
	}

	// Always save content first
	skill := &store.Skill{
		Name:             parsed.Name,
		Content:          rawFile,
		CreatedBy:        userID,
		ValidationStatus: skills.ValidationStatusUnknown,
		ValidationMeta:   "",
	}
	if err := targetStore.Save(r.Context(), skill); err != nil {
		slog.Error("failed to save skill", "skill", name, "error", err)
		respondError(w, http.StatusInternalServerError, "Failed to save skill")
		return
	}

	// Post-save: run validation to determine status and persist issues
	var validation *skills.ValidationResult
	validationStatus := skills.ValidationStatusUnknown

	// Compute composite hash including auxiliary files
	savedFiles, _ := targetStore.ListFiles(r.Context(), parsed.Name)
	fileHashes := make([]string, 0, len(savedFiles))
	filePaths := make([]string, 0, len(savedFiles))
	for _, f := range savedFiles {
		fileHashes = append(fileHashes, skillFileHash(f))
		if f.Path != "" {
			filePaths = append(filePaths, f.Path+"/"+f.Filename)
		} else {
			filePaths = append(filePaths, f.Filename)
		}
	}
	contentHash := skills.CompositeContentHash(rawFile, fileHashes)

	// If content hasn't changed, carry forward existing acknowledgments
	var carryForwardAcks []skills.AcknowledgedRisk
	if existingMeta != nil && existingMeta.ContentHash == contentHash {
		carryForwardAcks = existingMeta.AcknowledgedRisks
	}

	llmProvider := getValidationLLMProvider(r)
	if llmProvider != nil {
		body := extractSkillBody(rawFile)
		result, err := skills.ValidateSkill(r.Context(), skills.ValidatorConfig{
			SkillName: parsed.Name,
			Content:   body,
			Files:     filePaths,
			LLM:       llmProvider,
		})
		if err != nil {
			slog.Warn("skill validation failed during save", "skill", parsed.Name, "error", err)
		} else {
			validation = result
			validationStatus = skills.DetermineValidationStatus(result, carryForwardAcks, contentHash)
		}
	}

	// Persist validation state (issues + status + carried acks)
	valMeta := skills.ValidationMeta{
		LastValidatedAt:   time.Now().UTC().Format(time.RFC3339),
		ContentHash:       contentHash,
		AcknowledgedRisks: carryForwardAcks,
	}
	if validation != nil {
		valMeta.Issues = validation.Issues
	}
	valMetaJSON, _ := json.Marshal(valMeta)

	if err := targetStore.UpdateValidationStatus(r.Context(), parsed.Name, validationStatus, string(valMetaJSON)); err != nil {
		slog.Warn("failed to persist validation status after save", "skill", parsed.Name, "error", err)
	}

	resp := map[string]interface{}{
		"status":            "ok",
		"validation_status": validationStatus,
	}
	if validation != nil && len(validation.Issues) > 0 {
		resp["validation"] = validation
	}
	if len(carryForwardAcks) > 0 {
		resp["acknowledged_risks"] = carryForwardAcks
	}
	respondJSON(w, http.StatusOK, resp)
}

// createSkillPlatform creates a new skill in the given store.
func createSkillPlatform(w http.ResponseWriter, r *http.Request, targetStore store.SkillStore, name string) {
	// Check if already exists in this store
	existing, _ := targetStore.Get(r.Context(), name)
	if existing != nil {
		respondError(w, http.StatusConflict, fmt.Sprintf("Skill %q already exists", name))
		return
	}

	userID := ""
	if pu := GetPlatformUser(r); pu != nil {
		userID = pu.ID
	}

	template := skills.NewSkillTemplate(name)
	skill := &store.Skill{
		Name:      name,
		Content:   template,
		CreatedBy: userID,
	}

	if err := targetStore.Save(r.Context(), skill); err != nil {
		slog.Error("failed to create skill", "skill", name, "error", err)
		respondError(w, http.StatusInternalServerError, "Failed to create skill")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// InstallSkillHandler handles POST /api/skills/install
// It downloads a skill from ClawHub on the server and installs it into the
// appropriate platform store (team preferred). This is the preferred path
// for CLI in remote mode.
func InstallSkillHandler(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024) // 64 KiB — URL + optional config
	var req InstallSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	input := strings.TrimSpace(req.Input)
	if input == "" {
		respondError(w, http.StatusBadRequest, "input is required")
		return
	}

	slug, err := skills.ParseClawHubInput(input)
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid input: "+err.Error())
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || (svc.TeamSkills == nil && svc.Skills == nil) {
		respondError(w, http.StatusNotImplemented, "Skill installation not available in this context")
		return
	}

	// Default to team scope (requires team admin). Fall back to org only if no team store.
	var targetStore store.SkillStore
	scope := "team"
	if svc.TeamSkills != nil && IsTeamAdmin(r) {
		targetStore = svc.TeamSkills
	} else if svc.Skills != nil {
		// Fall back to org — requires org admin
		user := GetPlatformUser(r)
		if user == nil {
			respondError(w, http.StatusUnauthorized, "Authentication required")
			return
		}
		if !CanManageOrg(user) {
			respondError(w, http.StatusForbidden, "Team admin or organization admin access required to install skills")
			return
		}
		targetStore = svc.Skills
		scope = "org"
	} else {
		respondError(w, http.StatusForbidden, "Insufficient permissions to install skills")
		return
	}

	// Download into a temp directory on the server
	tmpDir, err := os.MkdirTemp("", "astonish-clawhub-install-*")
	if err != nil {
		slog.Error("failed to create temp dir for ClawHub install", "error", err)
		respondError(w, http.StatusInternalServerError, "server error during install")
		return
	}
	defer os.RemoveAll(tmpDir)

	result, err := skills.DownloadFromClawHub(slug, tmpDir)
	if err != nil {
		slog.Warn("ClawHub download failed", "slug", slug, "error", err)
		respondError(w, http.StatusBadGateway, "download from ClawHub failed")
		return
	}

	// Read and parse main SKILL.md
	skillPath := filepath.Join(tmpDir, slug, "SKILL.md")
	// Defense-in-depth: ensure skillPath is within tmpDir.
	if !strings.HasPrefix(filepath.Clean(skillPath), filepath.Clean(tmpDir)+string(os.PathSeparator)) {
		slog.Warn("path traversal attempt in ClawHub slug", "slug", slug)
		respondError(w, http.StatusBadRequest, "invalid skill slug")
		return
	}
	skillData, err := os.ReadFile(skillPath)
	if err != nil {
		slog.Warn("SKILL.md not found in ClawHub package", "slug", slug, "path", skillPath, "error", err)
		respondError(w, http.StatusInternalServerError, "SKILL.md not found in package")
		return
	}

	parsed, err := skills.ParseSkillFile(skillPath, skillData)
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to parse SKILL.md: "+err.Error())
		return
	}

	userID := ""
	if pu := GetPlatformUser(r); pu != nil {
		userID = pu.ID
	}

	// Save the main skill definition
	skill := &store.Skill{
		Name:        parsed.Name,
		Description: parsed.Description,
		Content:     string(skillData),
		OS:          parsed.OS,
		RequireBins: parsed.RequireBins,
		RequireEnv:  parsed.RequireEnv,
		Metadata:    parsed.Metadata,
		CreatedBy:   userID,
	}
	if err := targetStore.Save(r.Context(), skill); err != nil {
		slog.Error("failed to save skill during install", "skill", parsed.Name, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to save skill")
		return
	}

	// Walk and save all auxiliary files (supports multi-file skills)
	// Enforce limits to prevent abuse: max 100 files, 1 MiB per file, 10 MiB total.
	const (
		maxInstallFiles     = 100
		maxInstallFileSize  = 1 << 20  // 1 MiB per file
		maxInstallTotalSize = 10 << 20 // 10 MiB aggregate
	)
	filesSaved := 0
	var totalSize int64
	skillDir := filepath.Join(tmpDir, slug)

	// Use os.OpenRoot to scope all file reads to skillDir, preventing symlink
	// TOCTOU traversal attacks (gosec G122).
	skillRoot, err := os.OpenRoot(skillDir)
	if err != nil {
		slog.Error("failed to open skill directory root", "dir", skillDir, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to read skill files")
		return
	}
	defer skillRoot.Close()

	err = filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// Defense-in-depth: skip symlinks (zip extraction shouldn't create them,
		// but protect against compromised temp directories)
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(skillDir, path)
		rel = filepath.ToSlash(rel)

		if rel == "SKILL.md" || rel == "_meta.json" {
			return nil
		}

		// Enforce per-file size limit
		if info.Size() > maxInstallFileSize {
			slog.Warn("skipping oversized file during install", "file", rel, "size", info.Size())
			return nil
		}

		// Enforce aggregate size limit
		totalSize += info.Size()
		if totalSize > maxInstallTotalSize {
			return fmt.Errorf("total file size exceeds %d bytes limit", maxInstallTotalSize)
		}

		// Enforce file count limit
		if filesSaved >= maxInstallFiles {
			return fmt.Errorf("file count exceeds %d limit", maxInstallFiles)
		}

		content, err := skillRoot.ReadFile(rel)
		if err != nil {
			return err
		}

		dir := filepath.Dir(rel)
		if dir == "." {
			dir = ""
		}
		fname := filepath.Base(rel)

		sf := &store.SkillFile{
			Path:         dir,
			Filename:     fname,
			Content:      string(content),
			IsExecutable: info.Mode().Perm()&0111 != 0,
			SizeBytes:    info.Size(),
		}

		if err := targetStore.SaveFile(r.Context(), parsed.Name, sf); err != nil {
			slog.Warn("failed to save auxiliary file during server install", "file", rel, "error", err)
			return nil
		}
		filesSaved++
		return nil
	})
	if err != nil {
		slog.Warn("error walking skill files during server install", "error", err)
	}

	// Run validation — determines whether skill is usable at runtime.
	// Skill is saved regardless, but validation_status controls runtime access.
	var validation *skills.ValidationResult
	validationStatus := skills.ValidationStatusUnknown

	// Load saved auxiliary files once (used for both validation and hash computation)
	savedFiles, _ := targetStore.ListFiles(r.Context(), parsed.Name)
	filePaths := make([]string, 0, len(savedFiles))
	fileHashes := make([]string, 0, len(savedFiles))
	for _, f := range savedFiles {
		fileHashes = append(fileHashes, skillFileHash(f))
		if f.Path != "" {
			filePaths = append(filePaths, f.Path+"/"+f.Filename)
		} else {
			filePaths = append(filePaths, f.Filename)
		}
	}
	installHash := skills.CompositeContentHash(string(skillData), fileHashes)

	if llmProvider := getValidationLLMProvider(r); llmProvider != nil {
		body := extractSkillBody(string(skillData))
		valResult, err := skills.ValidateSkill(r.Context(), skills.ValidatorConfig{
			SkillName: parsed.Name,
			Content:   body,
			Files:     filePaths,
			LLM:       llmProvider,
		})
		if err != nil {
			slog.Warn("skill validation failed during install", "skill", parsed.Name, "error", err)
		} else {
			validation = valResult
			validationStatus = skills.DetermineValidationStatus(valResult, nil, installHash)
		}
	}

	// Persist validation status
	valMeta := skills.ValidationMeta{
		LastValidatedAt: time.Now().UTC().Format(time.RFC3339),
		ContentHash:     installHash,
	}
	valMetaJSON, _ := json.Marshal(valMeta)
	if err := targetStore.UpdateValidationStatus(r.Context(), parsed.Name, validationStatus, string(valMetaJSON)); err != nil {
		slog.Warn("failed to update validation status after install", "skill", parsed.Name, "error", err)
	}

	installStatus := "ok"
	if !skills.IsUsableStatus(validationStatus) {
		installStatus = "installed_blocked"
	}

	resp := InstallSkillResponse{
		Status:           installStatus,
		Name:             parsed.Name,
		Scope:            scope,
		FilesSaved:       filesSaved,
		Version:          result.Version,
		Description:      parsed.Description,
		ValidationStatus: validationStatus,
		Validation:       validation,
	}
	respondJSON(w, http.StatusOK, resp)
}

// deleteSkillPlatform deletes a skill from the given store.
func deleteSkillPlatform(w http.ResponseWriter, r *http.Request, targetStore store.SkillStore, name, scope string) {
	ctx := r.Context()
	// Verify it exists in this store
	skill, err := targetStore.Get(ctx, name)
	if err != nil || skill == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found in %s store", name, scope))
		return
	}

	if err := targetStore.Delete(ctx, name); err != nil {
		slog.Error("failed to delete skill", "skill", name, "scope", scope, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to delete skill")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Shared helpers ---

func storeSkillToListItem(s *store.Skill) SkillListItem {
	// Re-parse the raw content to check eligibility
	parsed, err := skills.ParseSkillFile("store:"+s.Name, []byte(s.Content))
	if err != nil {
		return SkillListItem{
			Name:             s.Name,
			Description:      s.Description,
			Source:           "custom",
			Eligible:         true,
			Editable:         true,
			ValidationStatus: s.ValidationStatus,
		}
	}
	return SkillListItem{
		Name:             parsed.Name,
		Description:      parsed.Description,
		Source:           "custom",
		Eligible:         parsed.IsEligible(),
		Editable:         true,
		Missing:          parsed.MissingRequirements(),
		RequireBins:      parsed.RequireBins,
		RequireEnv:       parsed.RequireEnv,
		OS:               parsed.OS,
		ValidationStatus: s.ValidationStatus,
	}
}

// extractBody returns the markdown body after frontmatter from a raw SKILL.md.
func extractBody(rawFile string) string {
	parsed, err := skills.ParseSkillFile("extract", []byte(rawFile))
	if err != nil {
		return rawFile
	}
	return parsed.Content
}

func sortListItems(items []SkillListItem) {
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[i].Name > items[j].Name {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
}

// --- Skill Validation ---

// ValidateSkillResponse is the response for POST /api/skills/{name}/validate
type ValidateSkillResponse struct {
	Status           string                   `json:"status"`
	ValidationStatus string                   `json:"validation_status,omitempty"`
	Validation       *skills.ValidationResult `json:"validation"`
}

// ValidateSkillHandler handles POST /api/skills/{name}/validate
// Runs AI-powered validation on the skill content and auxiliary files.
// Persists the resulting validation_status to the database, which controls
// whether the skill can be used at runtime.
func ValidateSkillHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	svc := store.FromRequest(r)
	if svc == nil || (svc.PlatformSkills == nil && svc.Skills == nil && svc.TeamSkills == nil) {
		respondError(w, http.StatusNotFound, "no skill store available")
		return
	}

	// Validation updates persisted status and consumes LLM tokens, so it follows
	// the same scope-aware authorization as other skill mutations.
	skillStore := resolveSkillStoreForWrite(w, r, svc, scope)
	if skillStore == nil {
		return
	}

	// Rate limiting runs only after authorization so callers cannot learn whether
	// a skill in another scope was recently validated.
	if !canValidateSkill(effectiveTeamSlug(r), scope, name) {
		respondError(w, http.StatusTooManyRequests, "skill was validated recently — please wait before retrying")
		return
	}

	// Load skill content
	skill, err := skillStore.Get(r.Context(), name)
	if err != nil || skill == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found", name))
		return
	}

	// Load auxiliary files manifest
	files, _ := skillStore.ListFiles(r.Context(), name)
	filePaths := make([]string, 0, len(files))
	for _, f := range files {
		if f.Path != "" {
			filePaths = append(filePaths, f.Path+"/"+f.Filename)
		} else {
			filePaths = append(filePaths, f.Filename)
		}
	}

	// Get LLM provider for validation
	llmProvider := skillValidationLLMProvider(r)
	if llmProvider == nil {
		respondError(w, http.StatusServiceUnavailable, "skill validation unavailable: no AI provider configured")
		return
	}

	// Extract body content (strip frontmatter for analysis)
	body := extractSkillBody(skill.Content)

	// Run validation
	result, err := skills.ValidateSkill(r.Context(), skills.ValidatorConfig{
		SkillName: name,
		Content:   body,
		Files:     filePaths,
		LLM:       llmProvider,
	})
	if err != nil {
		slog.Warn("skill validation failed", "skill", name, "error", err)
		respondError(w, http.StatusBadGateway, "skill validation failed")
		return
	}

	// Determine new validation status.
	// Use composite hash that includes auxiliary file contents — modifying
	// an auxiliary file invalidates existing acknowledgments.
	fileHashes := make([]string, 0, len(files))
	for _, f := range files {
		fileHashes = append(fileHashes, skillFileHash(f))
	}
	contentHash := skills.CompositeContentHash(skill.Content, fileHashes)

	// Load existing acknowledged risks from validation_meta (if any)
	var existingMeta skills.ValidationMeta
	if skill.ValidationMeta != "" {
		_ = json.Unmarshal([]byte(skill.ValidationMeta), &existingMeta)
	}

	newStatus := skills.DetermineValidationStatus(result, existingMeta.AcknowledgedRisks, contentHash)

	// Persist validation status and issues to DB
	valMeta := skills.ValidationMeta{
		AcknowledgedRisks: existingMeta.AcknowledgedRisks,
		Issues:            result.Issues,
		LastValidatedAt:   time.Now().UTC().Format(time.RFC3339),
		ContentHash:       contentHash,
	}
	valMetaJSON, _ := json.Marshal(valMeta)
	if err := skillStore.UpdateValidationStatus(r.Context(), name, newStatus, string(valMetaJSON)); err != nil {
		slog.Error("failed to persist validation status", "skill", name, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to persist skill validation")
		return
	}

	recordSkillValidation(effectiveTeamSlug(r), scope, name)
	respondJSON(w, http.StatusOK, ValidateSkillResponse{
		Status:           "ok",
		ValidationStatus: newStatus,
		Validation:       result,
	})
}

// AcknowledgeSkillRequest is the request for POST /api/skills/{name}/acknowledge.
type AcknowledgeSkillRequest struct {
	Message string `json:"message"` // The issue message being acknowledged
	Type    string `json:"type"`    // The issue type (e.g. "security")
}

// AcknowledgeSkillResponse is the response for POST /api/skills/{name}/acknowledge.
type AcknowledgeSkillResponse struct {
	Status            string                   `json:"status"`
	ValidationStatus  string                   `json:"validation_status"`
	RemainingCritical int                      `json:"remaining_critical"`
	Acknowledgment    *skills.AcknowledgedRisk `json:"acknowledgment,omitempty"` // The ack that was just created
}

// AcknowledgeSkillHandler handles POST /api/skills/{name}/acknowledge
// Records a user's acknowledgment of a specific critical validation issue.
// After acknowledgment, checks if all critical issues are now acknowledged —
// if yes, transitions the skill to "acknowledged" (usable at runtime).
// Requires team/org admin authorization.
func AcknowledgeSkillHandler(w http.ResponseWriter, r *http.Request) {
	// Authorization: only team/org admins can acknowledge critical security risks
	if !IsTeamAdmin(r) {
		respondError(w, http.StatusForbidden, "only team or org admins can acknowledge skill risks")
		return
	}

	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	r.Body = http.MaxBytesReader(w, r.Body, 16*1024) // 16 KiB — type + message
	var req AcknowledgeSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Message == "" || req.Type == "" {
		respondError(w, http.StatusBadRequest, "message and type are required")
		return
	}

	userID := ""
	userEmail := ""
	if pu := GetPlatformUser(r); pu != nil {
		userID = pu.ID
		userEmail = pu.Email
	}

	svc := store.FromRequest(r)
	if svc == nil || (svc.Skills == nil && svc.TeamSkills == nil) {
		respondError(w, http.StatusNotFound, "No skill store available")
		return
	}

	// Resolve store
	var skillStore store.SkillStore
	if scope == "team" && svc.TeamSkills != nil {
		skillStore = svc.TeamSkills
	} else if scope == "org" && svc.Skills != nil {
		skillStore = svc.Skills
	} else if svc.TeamSkills != nil {
		skillStore = svc.TeamSkills
	} else {
		skillStore = svc.Skills
	}

	if skillStore == nil {
		respondError(w, http.StatusNotFound, "No skill store available for scope")
		return
	}

	// Load skill
	skill, err := skillStore.Get(r.Context(), name)
	if err != nil || skill == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("Skill %q not found", name))
		return
	}

	// Load existing validation meta
	var meta skills.ValidationMeta
	if skill.ValidationMeta != "" {
		_ = json.Unmarshal([]byte(skill.ValidationMeta), &meta)
	}

	// Compute composite hash including auxiliary files
	auxFiles, _ := skillStore.ListFiles(r.Context(), name)
	fileHashes := make([]string, 0, len(auxFiles))
	for _, f := range auxFiles {
		fileHashes = append(fileHashes, skillFileHash(f))
	}
	contentHash := skills.CompositeContentHash(skill.Content, fileHashes)

	// Verify the acknowledgment is for the current content (hash matches)
	if meta.ContentHash != "" && meta.ContentHash != contentHash {
		respondError(w, http.StatusConflict, "Skill content has changed since validation — please re-validate first")
		return
	}

	// Cap: prevent unbounded growth of acknowledged risks
	const maxAcksPerSkill = 50
	if len(meta.AcknowledgedRisks) >= maxAcksPerSkill {
		respondError(w, http.StatusConflict, "acknowledgment limit reached (max 50 per skill)")
		return
	}

	// Deduplication: check if this exact acknowledgment already exists
	for _, existing := range meta.AcknowledgedRisks {
		if existing.Type == req.Type && existing.Message == req.Message && existing.ContentHash == contentHash {
			// Already acknowledged — return success idempotently
			respondJSON(w, http.StatusOK, AcknowledgeSkillResponse{
				Status:           "ok",
				ValidationStatus: skill.ValidationStatus,
				Acknowledgment:   &existing,
			})
			return
		}
	}

	// Add the acknowledgment
	ack := skills.AcknowledgedRisk{
		Message:             req.Message,
		Type:                req.Type,
		AcknowledgedBy:      userID,
		AcknowledgedByEmail: userEmail,
		AcknowledgedAt:      time.Now().UTC().Format(time.RFC3339),
		ContentHash:         contentHash,
	}
	meta.AcknowledgedRisks = append(meta.AcknowledgedRisks, ack)

	// Determine new status: are all critical issues now acknowledged?
	newStatus := skills.DetermineValidationStatus(
		&skills.ValidationResult{Issues: meta.Issues},
		meta.AcknowledgedRisks,
		contentHash,
	)

	// Persist updated meta + status
	valMetaJSON, _ := json.Marshal(meta)
	if err := skillStore.UpdateValidationStatus(r.Context(), name, newStatus, string(valMetaJSON)); err != nil {
		slog.Error("failed to update validation status", "skill", name, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to update validation status")
		return
	}

	// Count remaining unacknowledged critical issues
	remaining := 0
	if newStatus == skills.ValidationStatusBlocked {
		for _, issue := range meta.Issues {
			if issue.Severity == "critical" {
				key := issue.Type + ":" + issue.Message
				found := false
				for _, a := range meta.AcknowledgedRisks {
					if a.ContentHash == contentHash && a.Type+":"+a.Message == key {
						found = true
						break
					}
				}
				if !found {
					remaining++
				}
			}
		}
	}

	respondJSON(w, http.StatusOK, AcknowledgeSkillResponse{
		Status:            "ok",
		ValidationStatus:  newStatus,
		RemainingCritical: remaining,
		Acknowledgment:    &ack,
	})
}

// getValidationLLMProvider returns an LLMProvider for skill validation.
// Uses the ChatManager's LLM if already initialized, otherwise creates one
// directly from the effective platform config (provider cascade).
func getValidationLLMProvider(r *http.Request) skills.LLMProvider {
	// Fast path: use the already-initialized chat LLM.
	// NOTE: We snapshot comp under the lock. Even if another goroutine replaces
	// cm.components after we release the lock, our reference remains valid (GC
	// keeps it alive). The LLM pointer is safe to use after unlock.
	cm := GetChatManager()
	cm.mu.Lock()
	comp := cm.components
	cm.mu.Unlock()

	if llm := validationLLMFromComponents(comp); llm != nil {
		return llm
	}

	// Slow path: create LLM from effective config
	appCfg := effectiveAppConfig(r)
	if appCfg == nil {
		return nil
	}

	providerName := appCfg.General.DefaultProvider
	modelName := appCfg.General.DefaultModel
	if providerName == "" {
		return nil
	}

	llm, err := provider.GetProvider(r.Context(), providerName, modelName, appCfg)
	if err != nil {
		slog.Warn("failed to create LLM for skill validation", "provider", providerName, "error", err)
		return nil
	}

	return &validationLLMAdapter{llmFunc: makeLLMFuncFromModel(llm)}
}

// validationLLMFromComponents returns the chat LLM only when it is bound to a
// real provider. A pre-warmed Studio chat may hold the unconfigured placeholder;
// validation must fall back to the effective request provider in that case.
func validationLLMFromComponents(comp *StudioChatComponents) skills.LLMProvider {
	if comp == nil || comp.LLM == nil || comp.ProviderName == "" || comp.ModelName == "" {
		return nil
	}
	return &validationLLMAdapter{llmFunc: makeLLMFuncFromModel(comp.LLM)}
}

// validationLLMAdapter wraps a simple prompt→response function into skills.LLMProvider.
type validationLLMAdapter struct {
	llmFunc func(ctx context.Context, prompt string) (string, error)
}

func (a *validationLLMAdapter) EvaluateText(ctx context.Context, prompt string) (string, error) {
	return a.llmFunc(ctx, prompt)
}

// extractSkillBody strips YAML frontmatter from skill content, returning just the body.
func extractSkillBody(rawContent string) string {
	content := rawContent
	if strings.HasPrefix(content, "---\n") || strings.HasPrefix(content, "---\r\n") {
		// Find end of frontmatter
		idx := strings.Index(content[4:], "\n---")
		if idx >= 0 {
			content = strings.TrimSpace(content[4+idx+4:])
		}
	}
	return content
}

// ForceSkillStatusHandler handles POST /api/skills/{name}/force-status
// Allows an admin to manually set a skill's validation status. This is the
// escape hatch for platforms without an LLM configured — skills that cannot
// be automatically validated can be manually approved by an admin.
//
// Only accepts transitions to "acknowledged" (admin accepts responsibility).
// Cannot be used to set "clean" or "warnings" — those require actual validation.
func ForceSkillStatusHandler(w http.ResponseWriter, r *http.Request) {
	if !IsTeamAdmin(r) {
		respondError(w, http.StatusForbidden, "only team or org admins can force skill status")
		return
	}

	name := mux.Vars(r)["name"]
	scope := r.URL.Query().Get("scope")

	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Only allow forcing to "acknowledged" — admin explicitly accepts responsibility
	if req.Status != skills.ValidationStatusAcknowledged {
		respondError(w, http.StatusBadRequest, "can only force status to 'acknowledged'")
		return
	}
	if req.Reason == "" {
		respondError(w, http.StatusBadRequest, "reason is required when forcing status")
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || (svc.Skills == nil && svc.TeamSkills == nil) {
		respondError(w, http.StatusNotFound, "no skill store available")
		return
	}

	var skillStore store.SkillStore
	if scope == "team" && svc.TeamSkills != nil {
		skillStore = svc.TeamSkills
	} else if scope == "org" && svc.Skills != nil {
		skillStore = svc.Skills
	} else if svc.TeamSkills != nil {
		skillStore = svc.TeamSkills
	} else {
		skillStore = svc.Skills
	}

	if skillStore == nil {
		respondError(w, http.StatusNotFound, "no skill store available for scope")
		return
	}

	skill, err := skillStore.Get(r.Context(), name)
	if err != nil || skill == nil {
		respondError(w, http.StatusNotFound, fmt.Sprintf("skill %q not found", name))
		return
	}

	// Build validation meta — record who forced this and why
	userID := ""
	userEmail := ""
	if pu := GetPlatformUser(r); pu != nil {
		userID = pu.ID
		userEmail = pu.Email
	}

	var meta skills.ValidationMeta
	if skill.ValidationMeta != "" {
		_ = json.Unmarshal([]byte(skill.ValidationMeta), &meta)
	}

	// Compute composite hash
	auxFiles, _ := skillStore.ListFiles(r.Context(), name)
	fileHashes := make([]string, 0, len(auxFiles))
	for _, f := range auxFiles {
		fileHashes = append(fileHashes, skillFileHash(f))
	}
	contentHash := skills.CompositeContentHash(skill.Content, fileHashes)

	meta.ContentHash = contentHash
	meta.LastValidatedAt = time.Now().UTC().Format(time.RFC3339)
	// Add a synthetic acknowledgment recording the force
	meta.AcknowledgedRisks = append(meta.AcknowledgedRisks, skills.AcknowledgedRisk{
		Message:             "Admin forced status: " + req.Reason,
		Type:                "admin_override",
		AcknowledgedBy:      userID,
		AcknowledgedByEmail: userEmail,
		AcknowledgedAt:      time.Now().UTC().Format(time.RFC3339),
		ContentHash:         contentHash,
	})

	valMetaJSON, _ := json.Marshal(meta)
	if err := skillStore.UpdateValidationStatus(r.Context(), name, skills.ValidationStatusAcknowledged, string(valMetaJSON)); err != nil {
		slog.Error("failed to force validation status", "skill", name, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to update validation status")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"status":            "ok",
		"validation_status": skills.ValidationStatusAcknowledged,
	})
}

// skillFileHash computes a hash of a skill file that includes its path and filename.
// This ensures that renaming a file (without changing content) or swapping file
// contents between two paths both invalidate the composite hash.
func skillFileHash(f store.SkillFile) string {
	return skills.ContentHash(f.Path + "/" + f.Filename + "\x00" + f.Content)
}
