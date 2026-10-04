package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/SAP/astonish/pkg/store/pgutil"
)

// PlatformInitRequest is the request body for POST /api/platform/init.
type PlatformInitRequest struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	SSLMode  string `json:"ssl_mode"`
	OrgName  string `json:"org_name"`
	OrgSlug  string `json:"org_slug"`
}

// PlatformInitResponse is the response for POST /api/platform/init.
type PlatformInitResponse struct {
	Success         bool   `json:"success"`
	Message         string `json:"message"`
	RestartRequired bool   `json:"restart_required"`
	Error           string `json:"error,omitempty"`
}

// PlatformInitHandler handles POST /api/platform/init.
//
// It accepts PostgreSQL connection parameters, creates the platform database,
// runs migrations, generates a JWT secret, and saves the config. This endpoint
// only works when the system is NOT already running in platform mode.
func PlatformInitHandler(w http.ResponseWriter, r *http.Request) {
	// Guard: refuse if already in platform mode.
	cfg, err := config.LoadAppConfig()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to load config: " + err.Error(),
		})
		return
	}
	if cfg.Storage.IsPostgres() {
		respondJSON(w, http.StatusConflict, PlatformInitResponse{
			Error: "Platform mode is already configured. To reconfigure, edit config.yaml directly.",
		})
		return
	}

	var req PlatformInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
			Error: "Invalid request body: " + err.Error(),
		})
		return
	}

	// Validate required fields.
	if req.Host == "" {
		req.Host = "localhost"
	}
	if req.Port == 0 {
		req.Port = 5432
	}
	if req.User == "" {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
			Error: "PostgreSQL username is required",
		})
		return
	}
	if req.Password == "" {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
			Error: "PostgreSQL password is required",
		})
		return
	}
	if req.SSLMode == "" {
		req.SSLMode = "prefer"
	}
	if req.OrgName == "" {
		req.OrgName = "My Organization"
	}
	if req.OrgSlug == "" {
		req.OrgSlug = "default"
	}

	// Validate slug format.
	for _, ch := range req.OrgSlug {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
				Error: "Organization slug must be lowercase alphanumeric with hyphens/underscores",
			})
			return
		}
	}

	// Generate a unique instance suffix for this deployment.
	// This namespaces all databases so multiple Astonish instances can share a PG host.
	suffix := config.GenerateInstanceSuffix()

	// Build a temporary DSN and open a single *sql.DB connection for the entire
	// admin phase. Using *sql.DB keeps the TCP socket pooled, which is critical
	// for kubectl port-forward tunnels.
	tempDSN := pgutil.BuildDSN(req.Host, req.Port, req.User, req.Password, "postgres", req.SSLMode)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	adminDB, connErr := sql.Open("pgx", tempDSN)
	if connErr != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to connect to PostgreSQL: " + cleanPGError(connErr.Error()),
		})
		return
	}
	adminDB.SetMaxOpenConns(1)
	adminDB.SetMaxIdleConns(1)
	if connErr = adminDB.PingContext(ctx); connErr != nil {
		adminDB.Close()
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to connect to PostgreSQL: " + cleanPGError(connErr.Error()),
		})
		return
	}

	for attempts := 0; attempts < 5; attempts++ {
		exists, checkErr := pgutil.PlatformDBExistsDB(ctx, adminDB, suffix)
		if checkErr != nil {
			adminDB.Close()
			respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
				Error: "Failed to check database existence: " + cleanPGError(checkErr.Error()),
			})
			return
		}
		if !exists {
			break
		}
		suffix = config.GenerateInstanceSuffix()
	}

	// Build the platform DSN with the actual platform DB name.
	platformDBName := config.PlatformDBName(suffix)
	platformDSN := pgutil.BuildDSN(req.Host, req.Port, req.User, req.Password, platformDBName, req.SSLMode)

	// Bootstrap: create DB and run migrations via entstore.
	// Pass adminDB so bootstrap reuses our existing connection.
	entCfg := entstore.Config{
		DSN:            platformDSN,
		InstanceSuffix: suffix,
	}
	if err := entstore.BootstrapPlatform(ctx, entCfg, adminDB); err != nil {
		adminDB.Close()
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to initialize platform database: " + cleanPGError(err.Error()),
		})
		return
	}
	adminDB.Close()

	// Generate JWT secret and save config.
	jwtSecret := config.GenerateJWTSecret()

	cfg.Storage.Backend = "postgres"
	cfg.Storage.Postgres.PlatformDSN = platformDSN
	cfg.Storage.Postgres.InstanceSuffix = suffix
	cfg.Storage.Auth.Mode = "builtin"
	cfg.Storage.Auth.JWTSecret = jwtSecret
	cfg.Storage.Auth.DefaultOrgName = req.OrgName
	cfg.Storage.Auth.DefaultOrgSlug = req.OrgSlug

	if err := config.SaveAppConfig(cfg); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Platform DB initialized but failed to save config: " + err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, PlatformInitResponse{
		Success:         true,
		Message:         "Platform initialized successfully. Please restart Astonish to activate platform mode.",
		RestartRequired: true,
	})
}

// cleanPGError removes sensitive connection details from PostgreSQL error messages.
func cleanPGError(msg string) string {
	// Remove anything that looks like a connection string
	if idx := strings.Index(msg, "postgres://"); idx >= 0 {
		end := strings.IndexAny(msg[idx:], " \n\t")
		if end < 0 {
			msg = msg[:idx] + "[redacted]"
		} else {
			msg = msg[:idx] + "[redacted]" + msg[idx+end:]
		}
	}
	return msg
}

// SQLitePlatformInitRequest is the request body for POST /api/platform/init/sqlite.
type SQLitePlatformInitRequest struct {
	DataDir       string `json:"data_dir"`
	OrgName       string `json:"org_name"`
	OrgSlug       string `json:"org_slug"`
	AuthMode      string `json:"auth_mode,omitempty"`
	AdminEmail    string `json:"admin_email"`
	AdminName     string `json:"admin_name"`
	AdminPassword string `json:"admin_password"`
}

// SQLitePlatformInitHandler handles POST /api/platform/init/sqlite.
//
// It creates the SQLite platform database, bootstraps the first user/org/team,
// generates a JWT secret and master encryption key, and saves the config.
// This endpoint only works when the system is NOT already running in platform mode.
func SQLitePlatformInitHandler(w http.ResponseWriter, r *http.Request) {
	// Guard: refuse if already in platform mode.
	cfg, err := config.LoadAppConfig()
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to load config: " + err.Error(),
		})
		return
	}
	if cfg.Storage.IsPostgres() || cfg.Storage.IsSQLite() {
		respondJSON(w, http.StatusConflict, PlatformInitResponse{
			Error: "Platform mode is already configured. To reconfigure, edit config.yaml directly.",
		})
		return
	}

	var req SQLitePlatformInitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
			Error: "Invalid request body: " + err.Error(),
		})
		return
	}

	if req.AuthMode == "" {
		req.AuthMode = config.AuthModeNone
	}
	if err := config.ValidateAuthMode("sqlite", req.AuthMode); err != nil {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{Error: err.Error()})
		return
	}
	if req.AuthMode == config.AuthModeBuiltin && req.AdminEmail == "" {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
			Error: "Admin email is required",
		})
		return
	}
	if req.AuthMode == config.AuthModeBuiltin && (req.AdminPassword == "" || len(req.AdminPassword) < 8) {
		respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
			Error: "Admin password must be at least 8 characters",
		})
		return
	}
	if req.OrgName == "" {
		req.OrgName = "My Organization"
	}
	if req.OrgSlug == "" {
		req.OrgSlug = "default"
	}

	// Validate slug format.
	for _, ch := range req.OrgSlug {
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			respondJSON(w, http.StatusBadRequest, PlatformInitResponse{
				Error: "Organization slug must be lowercase alphanumeric with hyphens/underscores",
			})
			return
		}
	}

	// Determine data directory.
	dataDir := req.DataDir
	if dataDir == "" {
		dataDir = cfg.Storage.SQLite.GetDataDir()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	// Bootstrap: create SQLite platform database and run schema migrations.
	entCfg := entstore.Config{
		DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
		DataDir: dataDir,
	}
	if err := entstore.BootstrapPlatform(ctx, entCfg, nil); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to initialize SQLite database: " + err.Error(),
		})
		return
	}

	// Open the store to seed admin user, org, and team.
	entStore, err := entstore.New(ctx, entCfg)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to open SQLite database after bootstrap: " + err.Error(),
		})
		return
	}
	defer entStore.Close()

	if req.AuthMode == config.AuthModeNone {
		if err := store.ProvisionSingleUser(ctx, entStore, req.OrgName, req.OrgSlug); err != nil {
			respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{Error: "Failed to create local workspace: " + err.Error()})
			return
		}
		cfg.Storage.Backend = "sqlite"
		cfg.Storage.SQLite.DataDir = dataDir
		cfg.Storage.Auth.Mode = config.AuthModeNone
		cfg.Storage.Auth.DefaultOrgName = req.OrgName
		cfg.Storage.Auth.DefaultOrgSlug = req.OrgSlug
		if err := config.SaveAppConfig(cfg); err != nil {
			respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{Error: "Platform initialized but failed to save config: " + err.Error()})
			return
		}
		respondJSON(w, http.StatusOK, PlatformInitResponse{
			Success:         true,
			Message:         "SQLite single-user workspace initialized successfully. Please restart Astonish to activate it.",
			RestartRequired: true,
		})
		return
	}

	// Seed admin user.
	now := time.Now()
	adminDisplayName := req.AdminName
	if adminDisplayName == "" {
		adminDisplayName = req.AdminEmail
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to hash admin password: " + err.Error(),
		})
		return
	}
	userID := uuid.New().String()
	adminUser := &store.User{
		ID:           userID,
		Email:        req.AdminEmail,
		DisplayName:  adminDisplayName,
		PasswordHash: string(hash),
		PlatformRole: "superadmin",
		Status:       "active",
		CreatedAt:    now,
	}
	if err := entStore.Users().Create(ctx, adminUser); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to create admin user: " + err.Error(),
		})
		return
	}

	// Seed organization.
	orgID := uuid.New().String()
	org := &store.Organization{
		ID:        orgID,
		Name:      req.OrgName,
		Slug:      req.OrgSlug,
		CreatedAt: now,
	}
	if err := entStore.Organizations().Create(ctx, org); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to create organization: " + err.Error(),
		})
		return
	}

	// Add admin as org owner.
	if err := entStore.Organizations().AddMember(ctx, userID, orgID, "owner"); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to add org membership: " + err.Error(),
		})
		return
	}

	// Provision org.
	if err := entStore.ProvisionOrg(ctx, orgID, req.OrgSlug); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: fmt.Sprintf("Failed to provision org: %v", err),
		})
		return
	}

	// Create default team.
	orgStore, err := entStore.ForOrg(req.OrgSlug)
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to open org store: " + err.Error(),
		})
		return
	}
	team := &store.Team{
		ID:         uuid.New().String(),
		Name:       "General",
		Slug:       "general",
		SchemaName: entstore.TeamSchemaName("general"),
		CreatedAt:  now,
	}
	if err := orgStore.Teams().CreateTeam(ctx, team); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Failed to create team: " + err.Error(),
		})
		return
	}

	// Provision team and personal schema.
	if err := orgStore.ProvisionTeam(ctx, "general"); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: fmt.Sprintf("Failed to provision team: %v", err),
		})
		return
	}
	if err := orgStore.ProvisionPersonalSchema(ctx, userID); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: fmt.Sprintf("Failed to provision personal schema: %v", err),
		})
		return
	}

	// Generate master encryption key and save to .store_key file.
	masterKey := make([]byte, 32)
	if _, err := rand.Read(masterKey); err == nil {
		masterKeyHex := hex.EncodeToString(masterKey)
		if configDir, dirErr := config.GetConfigDir(); dirErr == nil {
			keyPath := filepath.Join(configDir, ".store_key")
			_ = os.WriteFile(keyPath, []byte(masterKeyHex), 0600)
		}
	}

	// Generate JWT secret and save config.
	jwtSecret := config.GenerateJWTSecret()

	cfg.Storage.Backend = "sqlite"
	cfg.Storage.SQLite.DataDir = dataDir
	cfg.Storage.Auth.Mode = "builtin"
	cfg.Storage.Auth.JWTSecret = jwtSecret
	cfg.Storage.Auth.DefaultOrgName = req.OrgName
	cfg.Storage.Auth.DefaultOrgSlug = req.OrgSlug

	if err := config.SaveAppConfig(cfg); err != nil {
		respondJSON(w, http.StatusInternalServerError, PlatformInitResponse{
			Error: "Platform initialized but failed to save config: " + err.Error(),
		})
		return
	}

	respondJSON(w, http.StatusOK, PlatformInitResponse{
		Success:         true,
		Message:         "SQLite platform initialized successfully. Please restart Astonish to activate platform mode.",
		RestartRequired: true,
	})
}

// DeploymentModeHandler handles GET /api/platform/mode.
// Returns the current deployment mode so the frontend can adapt.
func DeploymentModeHandler(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"mode": "personal",
		})
		return
	}

	mode := "personal"
	if cfg.Storage.IsPostgres() {
		mode = "platform"
	} else if cfg.Storage.IsSQLite() {
		mode = "sqlite"
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"mode":       mode,
		"configured": cfg.Storage.Backend != "",
	})
}

// PlatformInitStatusHandler handles GET /api/platform/init/status.
// Returns whether platform mode is already configured and initialized.
func PlatformInitStatusHandler(w http.ResponseWriter, _ *http.Request) {
	cfg, err := config.LoadAppConfig()
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{
			"configured":  false,
			"initialized": false,
		})
		return
	}

	configured := cfg.Storage.IsPostgres() || cfg.Storage.IsSQLite()
	initialized := false

	if cfg.Storage.IsPostgres() {
		// Try connecting to verify the database is actually accessible.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		entCfg := entstore.Config{
			DSN:            cfg.Storage.Postgres.GetPlatformDSN(),
			InstanceSuffix: cfg.Storage.Postgres.InstanceSuffix,
		}
		_, entS, pgErr := entstore.NewPlatformServices(ctx, entCfg)
		if pgErr == nil {
			initialized = true
			entS.Close()
		}
	} else if cfg.Storage.IsSQLite() {
		// Check that the platform.db exists and is accessible.
		dataDir := cfg.Storage.SQLite.GetDataDir()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		entCfg := entstore.Config{
			DSN:     "file:" + filepath.Join(dataDir, "platform.db"),
			DataDir: dataDir,
		}
		s, sqlErr := entstore.New(ctx, entCfg)
		if sqlErr == nil {
			initialized = true
			s.Close()
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"configured":  configured,
		"initialized": initialized,
	})
}
