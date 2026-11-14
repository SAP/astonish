package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/docker"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type taskContinuityConfig struct {
	Dir, Owner, Scope, Bot, Task, Address, Image string
	MasterKey                                    string
	ProductionPort                               int
	Home                                         sandbox.BotHomeBinding
}

func taskContinuityOpen(t *testing.T, q taskContinuityConfig) (*entstore.Store, store.BotStore, context.Context, *docker.DockerBackend) {
	t.Helper()
	ctx := context.Background()
	_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(q.Dir, "platform.db"), DataDir: q.Dir})
	if err != nil {
		t.Fatal(err)
	}
	org, err := db.ForOrg("continuity")
	if err != nil {
		t.Fatal(err)
	}
	repo := org.ForTeam("ops").Bots()
	if q.Scope == "personal" {
		repo = org.ForUser(q.Owner).Bots()
	}
	auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: q.Owner, OrgSlug: "continuity", TeamSlug: "ops", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := docker.New(docker.Config{Sessions: sandbox.NewSessionRegistryFromStore(nil), LayersDir: filepath.Join(q.Dir, "layers"), UppersDir: filepath.Join(q.Dir, "uppers"), SandboxImage: q.Image})
	if err != nil {
		t.Fatal(err)
	}
	return db, repo, auth, backend
}

// A deliberately owned subprocess daemon harness: production HTTP handlers,
// scoped Ent router and physical Docker Home; no paid model or normal daemon.
// The parent SIGKILLs this process and starts a new process on the SAME config.
func TestBotTaskContinuityDaemonProcess(t *testing.T) {
	path := os.Getenv("ASTONISH_TEST_TASK_CONTINUITY_CHILD")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var q taskContinuityConfig
	if err = json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(q.Address)
	if err != nil || host != "127.0.0.1" || port == "9393" {
		t.Fatal("owned loopback non-production port required")
	}
	if q.ProductionPort < 1 || q.ProductionPort == 9393 {
		t.Fatal("owned production daemon port required")
	}
	go func() {
		if e := Run(RunConfig{Port: q.ProductionPort}); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(2)
		}
	}()
	// Wait for production startup migration before opening the inspection
	// connection; racing two migration clients is not restart behavior.
	deadline := time.Now().Add(120 * time.Second)
	probeClient := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var lastProbe string
	for {
		resp, e := probeClient.Get("http://127.0.0.1:" + strconv.Itoa(q.ProductionPort) + "/api/auth/setup-status")
		if e == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			resp.Body.Close()
			lastProbe = fmt.Sprintf("HTTP %d %s", resp.StatusCode, body)
			if resp.StatusCode == http.StatusOK {
				break
			}
		} else {
			lastProbe = e.Error()
		}
		if time.Now().After(deadline) {
			if log, e := os.ReadFile(filepath.Join(q.Dir, "logs", "daemon.log")); e == nil {
				fmt.Fprintf(os.Stderr, "owned production startup log:\n%s\n", log)
			}
			t.Fatalf("production daemon startup timed out: %s", lastProbe)
		}
		time.Sleep(100 * time.Millisecond)
	}
	db, repo, auth, backend := taskContinuityOpen(t, q)
	defer db.Close()
	workspace := &launcher.BotTaskWorkspace{Bots: repo, Tasks: repo.(store.BotTaskStore), Backend: backend}
	if err = workspace.Install(); err != nil {
		t.Fatal(err)
	}
	svc := &store.Services{Bots: repo, PersonalBots: repo}
	auth = store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: q.Owner, OrgSlug: "continuity", TeamSlug: "ops"}), svc)
	router := mux.NewRouter()
	router.HandleFunc("/bots/{botID}", api.BotGetHandler)
	router.HandleFunc("/continuity", func(w http.ResponseWriter, r *http.Request) {
		task, e := repo.(store.BotTaskStore).GetTask(auth, uuid.MustParse(q.Bot), uuid.MustParse(q.Task))
		if e != nil || task.Head == nil {
			http.Error(w, fmt.Sprint(e), 500)
			return
		}
		revision, e := workspace.Load(auth, task.BotID, *task.Head)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		state, e := workspace.Read(auth, task.BotID, task.HomePath+"/state/observations.sqlite", 1<<20)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		physical, e := backend.ReadBotHomeWorkspace(auth, &q.Home, task.HomePath+"/TASK.md", 8192)
		if e != nil || !bytes.Equal(physical, revision.Bundle["TASK.md"]) {
			http.Error(w, "physical/registry mismatch", 500)
			return
		}
		json.NewEncoder(w).Encode(struct {
			Task     *store.BotTask
			Revision *store.BotTaskRevision
			State    []byte
		}{task, revision, state})
	})
	server := &http.Server{Addr: q.Address, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r.WithContext(auth)) }), ReadHeaderTimeout: 5 * time.Second}
	if err = server.ListenAndServe(); err != nil {
		t.Fatal(err)
	}
}

func TestBotTaskContinuityHTTPAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_TASK_CONTINUITY") != "1" {
		t.Skip("explicit owned continuity driver required")
	}
	// Use a fresh test-owned encryption root for seeding and both child
	// processes; never load or copy the normal installation's store key.
	masterKey := strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")
	t.Setenv("ASTONISH_MASTER_KEY", masterKey)
	// Continuity exercises storage, physical Home bytes and process restart,
	// not semantic retrieval quality. Keep production embedding initialization
	// wired through its supported HTTP provider, but supply a test-owned
	// loopback fixture rather than downloading a fresh HuggingFace model into
	// each isolated HOME. Never read the normal installation's model cache.
	embeddings := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/embeddings" {
			http.Error(w, "unexpected embedding fixture request", http.StatusBadRequest)
			return
		}
		var input struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil || input.Model != "continuity-fixture" || input.Input == "" {
			http.Error(w, "invalid embedding fixture input", http.StatusBadRequest)
			return
		}
		vector := make([]float32, 384)
		vector[0] = 1 // Valid normalized fixture; no semantic-quality claim.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"embedding": vector}}})
	}))
	t.Cleanup(embeddings.Close)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			owner := uuid.New()
			orgID := uuid.NewString()
			q := taskContinuityConfig{Dir: dir, Owner: owner.String(), Scope: string(scope), MasterKey: masterKey, Image: os.Getenv("ASTONISH_SANDBOX_IMAGE")}
			_, seed, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			if err = seed.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = seed.Organizations().Create(ctx, &store.Organization{ID: orgID, Slug: "continuity", Name: "Continuity", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = seed.Organizations().AddMember(ctx, owner.String(), orgID, "owner"); err != nil {
				t.Fatal(err)
			}
			if err = seed.ProvisionOrg(ctx, orgID, "continuity"); err != nil {
				t.Fatal(err)
			}
			org, err := seed.ForOrg("continuity")
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "ops", Name: "Ops", SchemaName: "team_ops"}
			if err = org.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			if err = org.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "admin"}); err != nil {
				t.Fatal(err)
			}
			if err = org.ProvisionTeam(ctx, "ops"); err != nil {
				t.Fatal(err)
			}
			if err = org.ProvisionPersonalSchema(ctx, owner.String()); err != nil {
				t.Fatal(err)
			}
			seed.Close()
			db, repo, auth, backend := taskContinuityOpen(t, q)
			v := store.DefaultBotVersion()
			v.Charter = "Retain continuity files"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Continuity", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			q.Bot = b.ID.String()
			home, err := backend.EnsureBotHome(auth, sandbox.BotHomeScope{OrgID: "continuity", TeamID: "ops", UserID: owner.String(), BotID: q.Bot})
			if err != nil {
				t.Fatal(err)
			}
			q.Home = *home
			t.Cleanup(func() {
				c, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				if e := backend.DeleteBotHome(c, home); e != nil {
					t.Error(e)
				}
			})
			service, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindService, ClientID: "owned-continuity", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: "continuity", TeamSlug: "ops", Authenticated: true, Scopes: []string{entstore.BotHomeRegistrationScope}})
			if err != nil {
				t.Fatal(err)
			}
			if err = entstore.RegisterBotHomeBinding(service, repo, entstore.BotHomeRegistration{BotID: b.ID, ExpectedRevision: b.Revision, Home: store.BotHomeBinding{ID: uuid.New(), BotID: b.ID, Scope: scope, Backend: string(home.Backend), StorageKey: home.Ref, Revision: 1}}, func(context.Context, entstore.BotHomeRegistration) error { return nil }); err != nil {
				t.Fatal(err)
			}
			b, err = repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			orgForPin, e := db.ForOrg("continuity")
			if e != nil {
				t.Fatal(e)
			}
			if scope == store.BotScopePersonal {
				e = orgForPin.ForUser(owner.String()).SetSessionPin(auth, b.HomeSessionID.String(), "controlled", "model")
			} else {
				e = orgForPin.ForTeam("ops").SetSessionPin(auth, b.HomeSessionID.String(), "controlled", "model")
			}
			if e != nil {
				t.Fatal(e)
			}
			// Owner file retention is independent of execution readiness; this
			// fixture does not activate a paid-model-capable Bot.
			workspace := &launcher.BotTaskWorkspace{Bots: repo, Tasks: repo.(store.BotTaskStore), Backend: backend}
			if err = workspace.Install(); err != nil {
				t.Fatal(err)
			}
			id := uuid.New()
			q.Task = id.String()
			files := []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("# Retained procedure\nInspect persisted observations.\n")}, {Path: "scripts/inspect.py", Executable: true, Data: []byte("import sqlite3\nprint('retained helper')\n")}}
			revision := store.BotTaskRevision{SchemaVersion: 1, ID: uuid.New(), BotID: b.ID, Ref: store.BotTaskRef{TaskID: id, Revision: 1}, Manifest: store.BotTaskManifest{SchemaVersion: 1, BotID: b.ID, TaskID: id, Title: "Retained procedure", Assignment: "Inspect persisted observations"}, Bundle: map[string][]byte{}}
			for _, f := range files {
				kind := store.BotTaskAssetDefinition
				if f.Executable {
					kind = store.BotTaskAssetScript
				}
				revision.Manifest.Assets = append(revision.Manifest.Assets, store.BotTaskAsset{Path: f.Path, Kind: kind, Digest: f.Digest(), SizeBytes: int64(len(f.Data)), Executable: f.Executable})
				revision.Bundle[f.Path] = f.Data
			}
			revision.Ref.Digest = revision.ContentDigest()
			task, err := workspace.Publish(auth, store.BotTaskPrepareRequest{Mutation: store.BotTaskMutation{BotID: b.ID, ActorID: owner, IdempotencyKey: "publish", AuthorityConstraintRef: "owner"}, Task: store.BotTask{ID: id, BotID: b.ID, Key: id.String(), HomePath: "tasks/" + id.String(), Title: "Retained procedure", Status: store.BotTaskReady}, Candidate: revision})
			if err != nil {
				t.Fatal(err)
			}
			// Generate a real, closed SQLite state file, not bytes with a .sqlite suffix.
			statePath := filepath.Join(dir, "observations.sqlite")
			cmd := exec.Command("python3", "-c", "import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); c.execute('create table observations(value text)'); c.execute(\"insert into observations values ('before-restart')\"); c.commit(); c.close()", statePath)
			if out, e := cmd.CombinedOutput(); e != nil {
				t.Fatalf("SQLite fixture %s: %v", out, e)
			}
			state, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if err = workspace.Write(auth, store.BotTaskMutation{BotID: b.ID, ActorID: owner, IdempotencyKey: "state"}, sandbox.BotHomeWorkspaceFile{Path: task.HomePath + "/state/observations.sqlite", Data: state}); err != nil {
				t.Fatal(err)
			}
			db.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			if port == 9393 {
				t.Fatal("refuse normal daemon port")
			}
			q.Address = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			productionListener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			q.ProductionPort = productionListener.Addr().(*net.TCPAddr).Port
			productionListener.Close()
			if q.ProductionPort == 9393 {
				t.Fatal("refuse normal daemon port")
			}
			configDir := filepath.Join(dir, "config", "astonish")
			if e = os.MkdirAll(configDir, 0700); e != nil {
				t.Fatal(e)
			}
			// This is an existing workspace, so fresh-install auto-provisioning does
			// not create local@astonish.local. Select the seeded owner explicitly.
			configYAML := fmt.Sprintf("storage:\n  backend: sqlite\n  sqlite:\n    data_dir: %q\n  auth:\n    mode: none\n    single_user_email: %q\n    default_org_slug: continuity\ndaemon:\n  port: %d\n  log_dir: %q\nsandbox:\n  enabled: false\nmemory:\n  embedding:\n    provider: openai-compat\n    base_url: %q\n    model: continuity-fixture\n", dir, owner.String()+"@test.invalid", q.ProductionPort, filepath.Join(dir, "logs"), embeddings.URL)
			if e = os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0600); e != nil {
				t.Fatal(e)
			}
			configPath := filepath.Join(dir, "owned-daemon.json")
			raw, _ := json.Marshal(q)
			if err = os.WriteFile(configPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var childLogs []string
			start := func() (*exec.Cmd, func()) {
				t.Helper()
				binary, e := os.Executable()
				if e != nil {
					t.Fatal(e)
				}
				child := exec.Command(binary, "-test.run=^TestBotTaskContinuityDaemonProcess$", "-test.timeout=3m")
				dockerEndpoint, e := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
				if e != nil {
					t.Fatal(e)
				}
				child.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + strings.TrimSpace(string(dockerEndpoint)), "HOME=" + dir, "XDG_CONFIG_HOME=" + filepath.Join(dir, "config"), "XDG_DATA_HOME=" + filepath.Join(dir, "owned-data"), "ASTONISH_MASTER_KEY=" + q.MasterKey, "ASTONISH_TEST_TASK_CONTINUITY_CHILD=" + configPath}
				log, e := os.Create(filepath.Join(dir, fmt.Sprintf("daemon-%d.log", time.Now().UnixNano())))
				if e != nil {
					t.Fatal(e)
				}
				childLogs = append(childLogs, log.Name())
				child.Stdout = log
				child.Stderr = log
				if e = child.Start(); e != nil {
					t.Fatal(e)
				}
				stopped := false
				stop := func() {
					if !stopped {
						stopped = true
						_ = child.Process.Kill()
						_ = child.Wait()
						log.Close()
					}
				}
				t.Cleanup(stop)
				return child, stop
			}
			read := func(t *testing.T) []byte {
				t.Helper()
				client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				deadline := time.Now().Add(125 * time.Second)
				nextProgress := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if time.Now().After(nextProgress) {
						if log, e := os.ReadFile(filepath.Join(dir, "logs", "daemon.log")); e == nil {
							if len(log) > 4096 {
								log = log[len(log)-4096:]
							}
							t.Logf("owned production readiness progress:\n%s", log)
						}
						nextProgress = time.Now().Add(5 * time.Second)
					}
					probe, e := client.Get("http://127.0.0.1:" + strconv.Itoa(q.ProductionPort) + "/api/auth/setup-status")
					if e != nil {
						time.Sleep(100 * time.Millisecond)
						continue
					}
					probe.Body.Close()
					if probe.StatusCode != http.StatusOK {
						// Startup can expose the listener before platform initialization
						// finishes. Require a successful production response within the
						// bounded readiness window, not the first transient response.
						time.Sleep(100 * time.Millisecond)
						continue
					}
					resp, e := client.Get("http://" + q.Address + "/continuity")
					if e == nil {
						body, _ := io.ReadAll(resp.Body)
						resp.Body.Close()
						if resp.StatusCode == 200 {
							return body
						}
						t.Fatalf("continuity HTTP %d %s", resp.StatusCode, body)
					}
					time.Sleep(100 * time.Millisecond)
				}
				for _, path := range childLogs {
					if log, err := os.ReadFile(path); err == nil {
						if len(log) > 16384 {
							log = log[len(log)-16384:]
						}
						t.Logf("owned daemon log %s:\n%s", path, log)
					}
				}
				t.Fatal("owned daemon did not become ready")
				return nil
			}
			var before []byte
			first, stop := start()
			if !t.Run("before-restart", func(t *testing.T) {
				before = read(t)
				var observed struct {
					Task     *store.BotTask
					Revision *store.BotTaskRevision
					State    []byte
				}
				if e := json.Unmarshal(before, &observed); e != nil {
					t.Fatal(e)
				}
				if observed.Task == nil || observed.Task.ID != id || observed.Revision == nil || observed.Revision.Ref != revision.Ref || !bytes.Equal(observed.State, state) || !bytes.Equal(observed.Revision.Bundle["scripts/inspect.py"], revision.Bundle["scripts/inspect.py"]) {
					t.Fatal("daemon did not resolve original Task pin/helper/real SQLite bytes")
				}
			}) {
				stop()
				return // No restart comparison is meaningful without baseline evidence.
			}
			stop()
			second, stop2 := start()
			defer stop2()
			t.Run("process-restart-files-state", func(t *testing.T) {
				if first.Process.Pid == second.Process.Pid {
					t.Fatal("process not replaced")
				}
				after := read(t)
				if !bytes.Equal(before, after) {
					t.Fatal("Task revision/helper/state changed across SIGKILL/restart")
				}
			})
		})
	}
}
