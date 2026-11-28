//go:build e2e

package e2eboot

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func checkRetainedHomeCluster(ctx context.Context, in RetainedHomeKubernetesInputs, client kubernetes.Interface) error {
	namespaces := make([]*corev1.Namespace, 0, 2)
	claims := make([]*corev1.PersistentVolumeClaim, 0, 3)
	for _, name := range []string{in.Kubernetes.Namespace, in.Kubernetes.ControlPlaneNamespace} {
		ns, err := client.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read dedicated namespace: %w", err)
		}
		namespaces = append(namespaces, ns)
	}
	for _, name := range []string{in.Kubernetes.LayersPVCName, in.Kubernetes.UppersPVCName, in.Kubernetes.HomesPVCName} {
		pvc, err := client.CoreV1().PersistentVolumeClaims(in.Kubernetes.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read dedicated claim: %w", err)
		}
		claims = append(claims, pvc)
	}
	return validateRetainedHomeClaims(in, namespaces, claims)
}

// bootstrapRetainedHomeNoModel preserves the real SQL services, HTTP auth and
// scoped core clients without resolving, configuring or constructing a provider.
// There is no startup sweep. DB cleanup is restricted to a successful CREATE
// receipt and matching persistent ownership comment on the exact random name.
func bootstrapRetainedHomeNoModel(t *testing.T, in RetainedHomeKubernetesInputs) *Harness {
	t.Helper()
	ctx := context.Background()
	suffix := "e2ehome" + uuid.NewString()[:8]
	platformDSN := buildDSN(t, in.DSN, suffix)
	dbName := config.PlatformDBName(suffix)
	receipt := "astonish-retained-home:" + in.Owner + ":" + uuid.NewString()
	admin, err := pgx.Connect(ctx, in.DSN)
	if err != nil {
		t.Fatal("BLOCKED: connect to dedicated PostgreSQL instance")
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		t.Fatalf("BLOCKED: create fresh owned platform: %v", err)
	}
	if _, err := admin.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{dbName}.Sanitize()+" IS '"+receipt+"'"); err != nil {
		t.Fatal("BLOCKED: ownership receipt failed; retain database for operator review")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanup, in.DSN)
		if err != nil {
			t.Error("owned DB cleanup connection failed")
			return
		}
		defer conn.Close(cleanup)
		var name, owner string
		var currentOwner bool
		err = conn.QueryRow(cleanup, "SELECT datname, COALESCE(shobj_description(oid, 'pg_database'), ''), datdba = (SELECT oid FROM pg_roles WHERE rolname = current_user) FROM pg_database WHERE datname=$1", dbName).Scan(&name, &owner, &currentOwner)
		if err != nil || validateRetainedHomeDatabaseReceipt(name, dbName, owner, receipt, currentOwner) != nil {
			t.Error("owned DB cleanup refused: ownership absent or changed")
			return
		}
		// Do not terminate unrelated clients or force a drop. An occupied DB blocks.
		if _, err = conn.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
			t.Errorf("owned DB cleanup blocked: %v", err)
		}
	})
	if err := entstore.BootstrapPlatform(ctx, entstore.Config{DSN: platformDSN, InstanceSuffix: suffix}, nil); err != nil {
		t.Fatalf("BLOCKED: platform bootstrap: %v", err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "astonish")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := config.AppConfig{}
	cfg.Storage = config.StorageConfig{Backend: "postgres", Postgres: config.PostgresConfig{PlatformDSN: platformDSN, InstanceSuffix: suffix}, Auth: config.PlatformAuthConfig{JWTSecret: defaultJWTSecret}}
	// Sandbox runtime is passed explicitly by the retained-home opener. Do not
	// let Studio construct a separate ambient backend or eager runtime.
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("SUDO_USER", "")
	t.Setenv("ASTONISH_MASTER_KEY", e2eFixedMasterKeyHex)
	svc, es, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: platformDSN, InstanceSuffix: suffix, MaxOpenConns: 10, MaxIdleConns: 5})
	if err != nil {
		t.Fatalf("BLOCKED: SQL services: %v", err)
	}
	t.Cleanup(func() { es.Close() })
	auth := api.NewPlatformAuth(cfg.Storage.Auth, es, cfg.Storage)
	studio, err := launcher.NewStudioServer(0, launcher.WithServices(svc), launcher.WithPlatformAuth(auth, es), launcher.WithTenantMiddleware(entstore.TenantMiddleware(es)))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := studio.Serve(); err != nil && err != http.ErrServerClosed {
			t.Errorf("retained-home Studio: %v", err)
		}
	}()
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = studio.Shutdown(stop)
	})
	time.Sleep(500 * time.Millisecond)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", studio.Port())
	h := &Harness{BaseURL: baseURL, Store: es, PlatformDSN: platformDSN, Suffix: suffix, BaseDSN: in.DSN}
	owner := seedRetainedHomeOwner(t, h)
	h.Token = owner.Token
	return h
}

func retainedHomeDedicatedClient(in RetainedHomeKubernetesInputs) (kubernetes.Interface, error) {
	// Loading only this explicit file prevents ambient merging and in-cluster
	// fallback. Pin both client-go and the production backend to the same context.
	raw, err := clientcmd.LoadFromFile(in.Kubernetes.KubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("load dedicated kubeconfig: %w", err)
	}
	if raw.CurrentContext != in.Kubernetes.Context {
		return nil, fmt.Errorf("dedicated kubeconfig current-context must match explicitly approved context (kubectl callers must not use ambient context)")
	}
	if raw.Contexts[in.Kubernetes.Context] == nil {
		return nil, fmt.Errorf("approved context absent from dedicated kubeconfig")
	}
	cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, in.Kubernetes.Context, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// seedRetainedHomeOwner uses existing scope provisioning, not Seed's provider,
// MCP, credential or embedding fixtures. Both DB creation and decommission are
// fenced by exact persistent receipts; no prior database is ever adopted.
func seedRetainedHomeOwner(t *testing.T, h *Harness) *SeededUser {
	t.Helper()
	ctx := context.Background()
	orgSlug := "home-" + uuid.NewString()[:8]
	orgID := uuid.NewString()
	dbName := config.OrgDBName(h.Suffix, orgSlug)
	receipt := "astonish-retained-org:" + uuid.NewString()
	conn, err := pgx.Connect(ctx, h.BaseDSN)
	if err != nil {
		t.Fatal("BLOCKED: dedicated org database connection")
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbName}.Sanitize()); err != nil {
		conn.Close(ctx)
		t.Fatal("BLOCKED: cannot create fresh dedicated org DB")
	}
	if _, err := conn.Exec(ctx, "COMMENT ON DATABASE "+pgx.Identifier{dbName}.Sanitize()+" IS '"+receipt+"'"); err != nil {
		conn.Close(ctx)
		t.Fatal("BLOCKED: org ownership receipt failed; retain DB")
	}
	conn.Close(ctx)
	orgRecord := &store.Organization{ID: orgID, Name: "Dedicated retained home", Slug: orgSlug, DBName: dbName, Status: "active", CreatedAt: time.Now()}
	if err := h.PlatformBackend().Organizations().Create(ctx, orgRecord); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanup, h.BaseDSN)
		if err != nil {
			t.Error("org cleanup connection blocked")
			return
		}
		defer conn.Close(cleanup)
		var name, owner string
		var currentOwner bool
		err = conn.QueryRow(cleanup, "SELECT datname, COALESCE(shobj_description(oid, 'pg_database'), ''), datdba = (SELECT oid FROM pg_roles WHERE rolname = current_user) FROM pg_database WHERE datname=", dbName).Scan(&name, &owner, &currentOwner)
		record, lookupErr := h.PlatformBackend().Organizations().GetBySlug(cleanup, orgSlug)
		if err != nil || lookupErr != nil || record == nil || record.ID != orgID || record.DBName != dbName || validateRetainedHomeDatabaseReceipt(name, dbName, owner, receipt, currentOwner) != nil {
			t.Error("org cleanup refused: exact ownership absent or changed")
			return
		}
		if err := h.PlatformBackend().DecommissionOrg(cleanup, orgSlug); err != nil {
			t.Errorf("owned org cleanup blocked: %v", err)
		}
	})
	if err := h.PlatformBackend().ProvisionOrg(ctx, orgID, orgSlug); err != nil {
		t.Fatal(err)
	}
	email := "home-" + uuid.NewString() + "@test.local"
	userID := createUser(t, ctx, h, email, "Retained home owner")
	addOrgMember(t, ctx, h, userID, orgID, "owner")
	org := getOrgDataStore(t, h, orgSlug)
	teamSlug := "home-team"
	teamID := provisionTeam(t, ctx, org, teamSlug, "Retained home team")
	addTeamMember(t, ctx, org, userID, teamID, "admin")
	provisionPersonalSchema(t, ctx, org, userID)
	result := &SeedResult{Users: map[string]*SeededUser{}}
	registerSeededUser(result, h.BaseURL, api.NewJWTIssuer(defaultJWTSecret, 15*time.Minute, 90*24*time.Hour), userID, email, email, "Retained home owner", orgSlug, teamSlug, "owner", "admin")
	return result.Users[email]
}
