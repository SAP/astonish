# Auto-load .env files if present (credentials, DSN, provider config).
# .env for shared defaults, .env.local for per-developer secrets.
# Both are gitignored. Existing shell env vars take precedence.
-include .env
-include .env.local
export

# Use bash for recipe execution — we rely on `set -o pipefail`, `[[`, etc.
SHELL := /bin/bash

# Variables
BINARY_NAME = astonish
WEB_DIR = web
VERSION ?= dev
DOCKER_REGISTRY ?= ghcr.io/sap
DEV_TAG ?= dev
# Host arch mapped to Docker/Go arch (x86_64→amd64, aarch64/arm64→arm64).
DEV_ARCH ?= $(shell uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/')

# Default target
all: build-all

# Help
help:
	@echo "Usage:"
	@echo "  make build           - Build the Go binary only"
	@echo "  make build-treesitter-lib - Build native tree-sitter shared library"
	@echo "  make build-ui        - Build the React UI (web/dist)"
	@echo "  make build-all       - Build UI first, then Go binary"
	@echo "  make run             - Run the Go application"
	@echo "  make studio          - Run Astonish Studio (dev mode)"
	@echo "  make studio-dev      - Run Studio with live UI reload"
	@echo "  make test            - Run all unit tests (Go + frontend)"
	@echo "  make test-unit       - Same as 'make test'"
	@echo "  make test-integration - Run integration tests (needs ASTONISH_TEST_DSN)"
	@echo "  make test-e2e        - Run E2E tests (needs live LLM + DB + e2e k8s infra)"
	@echo "  make test-e2e-sqlite - Run E2E tests with SQLite backend (no DB needed, needs k8s)"
	@echo "  make test-e2e-inspect - Run E2E tests in shared mode (browse sessions in UI after run)"
	@echo "  make test-e2e-inspect-stop - Stop the inspector started by 'test-e2e-inspect'"
	@echo "  make e2e-k8s-up      - Provision isolated k8s sandbox infra for e2e tests"
	@echo "  make e2e-k8s-down    - Tear down e2e k8s sandbox infra"
	@echo "  make install         - Install the binary to ~/bin"
	@echo "  make clean           - Clean up build artifacts"
	@echo "  make update-mcp-stars - Update MCP store GitHub star counts"
	@echo ""
	@echo "Docker Test Environment:"
	@echo "  make e2e-env-up      - Start isolated test environment"
	@echo "  make e2e-env-down    - Stop test environment"
	@echo "  make e2e-env-rebuild - Rebuild and restart test environment"
	@echo ""
	@echo "Kubernetes Platform Init:"
	@echo "  make platform-init PLATFORM_HOST=<host> PLATFORM_USER=<user> PLATFORM_PASSWORD=<pass>"
	@echo "                       - Run 'astonish platform init' inside the cluster"
	@echo "  make create-secrets K8S_NAMESPACE=<ns> PLATFORM_DSN=<dsn>"
	@echo "                       - Create astonish-secrets (master-key, jwt-secret, platform-dsn)"
	@echo ""
	@echo "Docker Production (Persistent Data):"
	@echo "  make docker-up       - Start persistent container (maps ./.astonish-data)"
	@echo "  make docker-down     - Stop persistent container"
	@echo "  make docker-rebuild  - Rebuild and restart persistent container"
	@echo ""
	@echo "Sandbox (Docker+Incus for macOS/Windows):"
	@echo "  make build-linux       - Cross-compile Linux amd64 binary"
	@echo "  make build-linux-arm64 - Cross-compile Linux arm64 binary"
	@echo "  make docker-incus      - Build+LOAD local Incus image (native arch; tags :$(VERSION) and :latest when VERSION=dev)"
	@echo ""
	@echo "OpenShell Sandbox:"
	@echo "  make docker-sandbox-openshell - Build the OpenShell sandbox image"
	@echo "  make proto-gen                - Regenerate gRPC stubs from vendored protos"
	@echo "  make helm-deps                - Update Helm subchart dependencies"
	@echo ""
	@echo "Registry Push (multi-arch, requires docker login + buildx):"
	@echo "  make push-dev                      - Build+push astonish:dev (multi-arch)"
	@echo "  make push-incus-dev                - Build+push astonish-incus:dev (multi-arch)"
	@echo "  make push-sandbox-base-dev         - Build+push astonish-sandbox-base:dev (multi-arch)"
	@echo "  make push-sandbox-openshell-dev    - Build+push astonish-sandbox-openshell:dev (multi-arch)"
	@echo "  make push-all-dev                  - Push all dev images"
	@echo ""
	@echo "Registry Push (fast single-arch, dev iteration):"
	@echo "  make push-dev-fast                      - Build+push astonish:dev (native arch only)"
	@echo "  make push-sandbox-base-dev-fast         - Build+push sandbox-base:dev (native arch only)"
	@echo "  make push-incus-dev-fast               - Build+push incus:dev (native arch only)"
	@echo "  make push-sandbox-openshell-dev-fast    - Build+push sandbox-openshell:dev (native arch only)"
	@echo "  make push-all-dev-fast                  - Push all dev images (native arch only)"

# Build the Go binary only
build:
	@echo "Building Go binary..."
	go build -o $(BINARY_NAME) .
	@echo "Go binary built successfully: $(BINARY_NAME)"

build-treesitter-lib:
	$(MAKE) -C pkg/codeintel/native

# Build the React UI
build-ui:
	@echo "Building React UI..."
	@rm -rf $(WEB_DIR)/dist
	cd $(WEB_DIR) && npm install && npm run build
	@touch $(WEB_DIR)/embed.go
	@echo "React UI built successfully: $(WEB_DIR)/dist"

# Build everything: UI first, then Go binary
build-all: setup-hooks ent-generate build-ui build
	@echo "Full build complete!"

# Setup git hooks (runs automatically on first build). Uses core.hooksPath so
# updates to .githooks/pre-commit apply without re-copying into .git/hooks.
setup-hooks:
	@chmod +x .githooks/pre-commit
	@git config core.hooksPath .githooks
	@echo "✓ Git hooks path set to .githooks (golangci-lint required on commit)"

# Match CI: verify config schema, then lint (version from .golangci-version).
lint:
	@required=$$(tr -d '[:space:]' < .golangci-version | sed 's/^v//'); \
	if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint not found — install v$$required (see CONTRIBUTING.md)"; exit 1; \
	fi; \
	installed=$$(golangci-lint version 2>/dev/null | sed -n 's/.*version \([0-9][0-9.]*\).*/\1/p' | head -1); \
	req_mm=$$(echo "$$required" | cut -d. -f1,2); \
	inst_mm=$$(echo "$$installed" | cut -d. -f1,2); \
	if [ "$$inst_mm" != "$$req_mm" ]; then \
		echo "golangci-lint v$$installed != required v$$required (CI)"; exit 1; \
	fi; \
	golangci-lint config verify; \
	golangci-lint run --timeout=5m

# Run the application
run:
	@echo "Running Go application..."
	go run .

# Run Astonish Studio (production mode - serves built UI)
studio: build-ui
	@echo "Starting Astonish Studio..."
	go run . studio

# Run Studio in dev mode (Go backend + Vite dev server)
studio-dev:
	@echo "Starting Astonish Studio (dev mode)..."
	@echo "  Backend: http://localhost:9393"
	@echo "  Frontend: http://localhost:5173"
	@echo ""
	@echo "Run 'cd web && npm run dev' in another terminal for live UI reload"
	go run . studio

# Run tests — unit tests (Go + frontend, no external deps)
test: test-unit

test-unit:
	@echo "Running Go unit tests..."
	go test ./...
	@echo "Running frontend tests..."
	cd $(WEB_DIR) && npm test -- --run
	@echo "All unit tests passed!"

# Integration tests — need Postgres (ASTONISH_TEST_DSN)
test-integration:
	@if [ -z "$$ASTONISH_TEST_DSN" ]; then \
		echo "ERROR: ASTONISH_TEST_DSN required. Example: export ASTONISH_TEST_DSN=postgres://user:pass@localhost:5432/testdb"; exit 1; fi
	@echo "Running integration tests..."
	go test -tags=integration -count=1 -timeout=10m ./...
	@echo "Integration tests passed!"

# E2E tests — need Postgres + live LLM provider + kubectl (for sandbox tests)
# Each test bootstraps a full platform (fresh DB, real server, real auth).
# -p 1 serializes packages to avoid Postgres role creation races.
test-e2e:
	@if [ -z "$$ASTONISH_TEST_DSN" ]; then echo "ERROR: ASTONISH_TEST_DSN required"; exit 1; fi
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH (required for e2e sandbox preflight)"; exit 1; }
	@CP_NS="$${ASTONISH_E2E_CONTROL_PLANE_NAMESPACE:-astonish}"; \
	SB_NS="$${ASTONISH_E2E_SANDBOX_NAMESPACE:-astonish-sandbox}"; \
	echo "Preflight: verifying e2e k8s infra ($$CP_NS + $$SB_NS)..."; \
	for NS in "$$CP_NS" "$$SB_NS"; do \
		if ! kubectl get ns "$$NS" >/dev/null 2>&1; then \
			echo ""; \
			echo "ERROR: e2e k8s infra not found — namespace \"$$NS\" missing."; \
			echo ""; \
			echo "  Provision it with:    make e2e-k8s-up"; \
			echo "  Tear it down with:    make e2e-k8s-down"; \
			echo ""; \
			echo "  Override namespaces by setting ASTONISH_E2E_CONTROL_PLANE_NAMESPACE"; \
			echo "  and ASTONISH_E2E_SANDBOX_NAMESPACE in .env (defaults: astonish, astonish-sandbox)."; \
			echo ""; \
			exit 1; \
		fi; \
	done; \
	for PVC in astonish-layers astonish-uppers; do \
		if ! kubectl get pvc -n "$$SB_NS" "$$PVC" >/dev/null 2>&1; then \
			echo ""; \
			echo "ERROR: e2e k8s infra incomplete — PVC \"$$PVC\" missing in namespace \"$$SB_NS\"."; \
			echo "  Re-provision with:    make e2e-k8s-up"; \
			echo ""; \
			exit 1; \
		fi; \
	done; \
	echo "Preflight OK."
	@echo ""
	@echo "Running E2E tests (~26 tests, typically 5-15 min)..."
	@set -o pipefail; go test -tags=e2e -count=1 -p 1 -timeout=15m \
		$(if $(RUN),-run $(RUN)) $(if $(VERBOSE),-v) -json ./tests/e2e/... \
		| node tests/scenarios/stream.mjs /tmp/e2e-results.json; \
	RESULT=$$?; \
	node tests/scenarios/parse-run.mjs /tmp/e2e-results.json; \
	exit $$RESULT

# E2E with SQLite backend — same tests, no PostgreSQL required.
# Still requires K8s for sandbox-dependent tests.
test-e2e-sqlite:
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH (required for e2e sandbox preflight)"; exit 1; }
	@CP_NS="$${ASTONISH_E2E_CONTROL_PLANE_NAMESPACE:-astonish}"; \
	SB_NS="$${ASTONISH_E2E_SANDBOX_NAMESPACE:-astonish-sandbox}"; \
	echo "Preflight: verifying e2e k8s infra ($$CP_NS + $$SB_NS)..."; \
	for NS in "$$CP_NS" "$$SB_NS"; do \
		if ! kubectl get ns "$$NS" >/dev/null 2>&1; then \
			echo ""; \
			echo "ERROR: e2e k8s infra not found — namespace \"$$NS\" missing."; \
			echo "  Provision it with:    make e2e-k8s-up"; \
			echo ""; \
			exit 1; \
		fi; \
	done; \
	for PVC in astonish-layers astonish-uppers; do \
		if ! kubectl get pvc -n "$$SB_NS" "$$PVC" >/dev/null 2>&1; then \
			echo ""; \
			echo "ERROR: e2e k8s infra incomplete — PVC \"$$PVC\" missing in namespace \"$$SB_NS\"."; \
			echo "  Re-provision with:    make e2e-k8s-up"; \
			echo ""; \
			exit 1; \
		fi; \
	done; \
	echo "Preflight OK."
	@echo ""
	@echo "Running E2E tests with SQLite backend (~26 tests, typically 5-15 min)..."
	@set -o pipefail; ASTONISH_E2E_BACKEND=sqlite go test -tags=e2e -count=1 -p 1 -timeout=15m \
		$(if $(RUN),-run $(RUN)) $(if $(VERBOSE),-v) -json ./tests/e2e/... \
		| node tests/scenarios/stream.mjs /tmp/e2e-results-sqlite.json; \
	RESULT=$$?; \
	node tests/scenarios/parse-run.mjs /tmp/e2e-results-sqlite.json; \
	exit $$RESULT

# E2E k8s sandbox infrastructure — provision/destroy the isolated namespaces,
# PVCs, RBAC, and seeded @base layer used by sandbox-aware E2E tests.
#
# Namespace layout (DEDICATED to e2e — never reuse a live install):
#   control plane: astonishe2e
#   sandbox:       astonishe2e-sandbox
#
# Tests run on the host using your kubeconfig; the chart's api/worker pods
# are scaled to 0 (we only need the sandbox slice). See
# deploy/helm/astonish/values-e2e.yaml for details.
E2E_K8S_RELEASE := astonishe2e
E2E_K8S_NS := astonishe2e
E2E_K8S_SANDBOX_NS := astonishe2e-sandbox

e2e-k8s-up:
	@which helm >/dev/null 2>&1 || { echo "ERROR: helm not found in PATH"; exit 1; }
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH"; exit 1; }
	@echo "Provisioning isolated e2e k8s infra ($(E2E_K8S_NS) + $(E2E_K8S_SANDBOX_NS))..."
	helm upgrade --install $(E2E_K8S_RELEASE) deploy/helm/astonish \
		-n $(E2E_K8S_NS) --create-namespace \
		-f deploy/helm/astonish/values-e2e.yaml \
		--history-max=3 \
		--wait --timeout=10m
	@echo "Patching NFS server (if present) to tolerate disk-pressure (prevents cleanup deadlock)..."
	-kubectl -n nfs-system patch deployment nfs-server --type=json -p='[{"op":"add","path":"/spec/template/spec/tolerations/-","value":{"key":"node.kubernetes.io/disk-pressure","effect":"NoSchedule","operator":"Exists"}}]' 2>/dev/null || true
	@echo "Verifying seeded @base layer..."
	@E2E_K8S_SANDBOX_NS=$(E2E_K8S_SANDBOX_NS) scripts/verify-e2e-seed.sh
	@echo "E2E k8s infra ready."

e2e-k8s-down:
	@which helm >/dev/null 2>&1 || { echo "ERROR: helm not found in PATH"; exit 1; }
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH"; exit 1; }
	@echo "Tearing down e2e k8s infra..."
	-helm uninstall $(E2E_K8S_RELEASE) -n $(E2E_K8S_NS) 2>/dev/null
	-kubectl delete pods --all -n $(E2E_K8S_SANDBOX_NS) --force --grace-period=0 --ignore-not-found 2>/dev/null
	-kubectl delete ns $(E2E_K8S_NS) --ignore-not-found
	-kubectl delete ns $(E2E_K8S_SANDBOX_NS) --ignore-not-found
	@echo "Pruning unused containerd images on nodes (best effort)..."
	-for node in $$(kubectl get nodes -o name 2>/dev/null); do \
		kubectl debug $$node -it --image=alpine -- chroot /host sh -c 'crictl rmi --prune 2>/dev/null || true' 2>/dev/null || true; \
	done
	@echo "E2E k8s infra removed."

# ===========================================================================
# E2E OpenShell sandbox infrastructure — provision/destroy the OpenShell
# gateway and sandbox namespace for E2E tests against the OpenShell backend.
#
# Namespace layout (DEDICATED to e2e — never reuse a live install):
#   control plane: astonishe2eos        (OpenShell gateway pod lives here)
#   sandbox:       astonishe2eos-sandbox (sandbox pods spawned by gateway)
#
# The chart's api/worker pods are scaled to 0 — tests run in-process on the
# host (same pattern as K8s e2e). Only the OpenShell gateway is deployed.
# ===========================================================================
E2E_OS_RELEASE := astonishe2eos
E2E_OS_NS := astonishe2eos
E2E_OS_SANDBOX_NS := astonishe2eos-sandbox

e2e-openshell-up:
	@which helm >/dev/null 2>&1 || { echo "ERROR: helm not found in PATH"; exit 1; }
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH"; exit 1; }
	@echo "Pulling OpenShell subchart dependency..."
	helm dependency update deploy/helm/astonish
	@echo "Deploying OpenShell e2e infra ($(E2E_OS_NS) + $(E2E_OS_SANDBOX_NS))..."
	helm upgrade --install $(E2E_OS_RELEASE) deploy/helm/astonish \
		-n $(E2E_OS_NS) --create-namespace \
		-f deploy/helm/astonish/values-e2e-openshell.yaml \
		--history-max=3 \
		--wait --timeout=10m
	@echo "Verifying OpenShell gateway is Running..."
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do \
		PHASE=$$(kubectl get pod -n $(E2E_OS_NS) -l app.kubernetes.io/name=openshell \
			-o jsonpath='{.items[0].status.phase}' 2>/dev/null); \
		if [ "$$PHASE" = "Running" ]; then break; fi; \
		if [ $$i -eq 15 ]; then \
			echo "ERROR: OpenShell gateway not Running after 45s."; \
			kubectl get pods -n $(E2E_OS_NS); \
			exit 1; \
		fi; \
		sleep 3; \
	done
	@echo "OpenShell e2e infra ready."
	@echo "  Gateway: $(E2E_OS_RELEASE)-openshell.$(E2E_OS_NS).svc.cluster.local:8080"

e2e-openshell-down:
	@which helm >/dev/null 2>&1 || { echo "ERROR: helm not found in PATH"; exit 1; }
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH"; exit 1; }
	@echo "Tearing down OpenShell e2e infra..."
	-helm uninstall $(E2E_OS_RELEASE) -n $(E2E_OS_NS) 2>/dev/null
	-kubectl delete pods --all -n $(E2E_OS_SANDBOX_NS) --force --grace-period=0 --ignore-not-found 2>/dev/null
	-kubectl delete ns $(E2E_OS_NS) --ignore-not-found
	-kubectl delete ns $(E2E_OS_SANDBOX_NS) --ignore-not-found
	@echo "OpenShell e2e infra removed."

# Run E2E tests against the OpenShell backend.
# Requires: ASTONISH_TEST_DSN, provider API key, OpenShell gateway Running.
#
# The tests run on the host, so we port-forward the in-cluster gateway to
# localhost:18080 for the duration of the run. This avoids requiring the host
# to resolve cluster-internal DNS names.
E2E_OS_LOCAL_PORT := 18080

test-e2e-openshell:
	@if [ -z "$$ASTONISH_TEST_DSN" ]; then echo "ERROR: ASTONISH_TEST_DSN required"; exit 1; fi
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH"; exit 1; }
	@echo "Preflight: verifying OpenShell gateway is Running..."
	@PHASE=$$(kubectl get pod -n $(E2E_OS_NS) -l app.kubernetes.io/name=openshell \
		-o jsonpath='{.items[0].status.phase}' 2>/dev/null); \
	if [ "$$PHASE" != "Running" ]; then \
		echo ""; \
		echo "ERROR: OpenShell gateway not Running (phase=$$PHASE)."; \
		echo "  Provision it with:    make e2e-openshell-up"; \
		echo "  Tear it down with:    make e2e-openshell-down"; \
		echo ""; \
		exit 1; \
	fi
	@echo "Preflight OK — OpenShell gateway Running."
	@echo ""
	@echo "Starting port-forward to OpenShell gateway (localhost:$(E2E_OS_LOCAL_PORT) → gateway:8080)..."
	@kubectl port-forward -n $(E2E_OS_NS) \
		svc/$(E2E_OS_RELEASE)-openshell $(E2E_OS_LOCAL_PORT):8080 \
		>/dev/null 2>&1 & PF_PID=$$!; \
	sleep 2; \
	if ! kill -0 $$PF_PID 2>/dev/null; then \
		echo "ERROR: port-forward failed to start."; \
		exit 1; \
	fi; \
	echo "Port-forward running (PID $$PF_PID)."; \
	echo ""; \
	echo "Running E2E tests against OpenShell backend (~44 tests, typically 10-20 min)..."; \
	set -o pipefail; \
	ASTONISH_E2E_SANDBOX_BACKEND=openshell \
	ASTONISH_E2E_SANDBOX_NAMESPACE=$(E2E_OS_SANDBOX_NS) \
	ASTONISH_E2E_CONTROL_PLANE_NAMESPACE=$(E2E_OS_NS) \
	ASTONISH_E2E_OPENSHELL_GATEWAY=localhost:$(E2E_OS_LOCAL_PORT) \
	go test -tags=e2e -count=1 -p 1 -timeout=20m \
		$(if $(RUN),-run $(RUN)) $(if $(VERBOSE),-v) -json ./tests/e2e/... \
		| node tests/scenarios/stream.mjs /tmp/e2e-openshell-results.json; \
	RESULT=$$?; \
	kill $$PF_PID 2>/dev/null || true; \
	node tests/scenarios/parse-run.mjs /tmp/e2e-openshell-results.json; \
	exit $$RESULT

# E2E inspector mode — long-lived single-instance for post-run UI inspection.
#
# Boots a long-lived in-process StudioServer (port 9394) and runs the entire
# e2e suite against it. After the run, the server keeps running so you can
# log into the UI and browse every chat session created during the run.
#
# Usage:
#   make test-e2e-inspect       # boot inspector + run suite + leave running
#   make test-e2e-inspect-stop  # gracefully stop the inspector
#
# The platform DB (suffix=e2einspect) is dropped & re-created on every
# `test-e2e-inspect` invocation — each run starts fresh.
E2E_INSPECT_BIN := bin/e2e-inspector
E2E_INSPECT_LOG := /tmp/astonish-e2e-inspect.log
E2E_INSPECT_PORT := 9394

# Inspector links the full StudioServer (pkg/...), so any change to backend
# code (chat runner, handlers, agent) MUST trigger a rebuild — otherwise
# tests run against a stale binary and silently mask backend regressions.
# See: 2026-05-23 CHAT-067 stale-inspector incident.
E2E_INSPECT_DEPS := $(shell find tools/e2e-inspector tests/e2eboot pkg cmd -name '*.go' -not -name '*_test.go' 2>/dev/null)

$(E2E_INSPECT_BIN): $(E2E_INSPECT_DEPS)
	@mkdir -p $(@D)
	go build -o $@ ./tools/e2e-inspector

test-e2e-inspect: $(E2E_INSPECT_BIN)
	@if [ -z "$$ASTONISH_TEST_DSN" ]; then echo "ERROR: ASTONISH_TEST_DSN required"; exit 1; fi
	@which kubectl >/dev/null 2>&1 || { echo "ERROR: kubectl not found in PATH (required for e2e sandbox preflight)"; exit 1; }
	@CP_NS="$${ASTONISH_E2E_CONTROL_PLANE_NAMESPACE:-astonish}"; \
	SB_NS="$${ASTONISH_E2E_SANDBOX_NAMESPACE:-astonish-sandbox}"; \
	echo "Preflight: verifying e2e k8s infra ($$CP_NS + $$SB_NS)..."; \
	for NS in "$$CP_NS" "$$SB_NS"; do \
		kubectl get ns "$$NS" >/dev/null 2>&1 || { echo "ERROR: namespace $$NS missing — run 'make e2e-k8s-up' first"; exit 1; }; \
	done; \
	for PVC in astonish-layers astonish-uppers; do \
		kubectl get pvc -n "$$SB_NS" "$$PVC" >/dev/null 2>&1 || { echo "ERROR: PVC $$PVC missing in $$SB_NS"; exit 1; }; \
	done; \
	echo "Preflight OK."
	@if [ -f /tmp/astonish-e2e-inspect.json ] && kill -0 $$(node -e "console.log(JSON.parse(require('fs').readFileSync('/tmp/astonish-e2e-inspect.json')).pid)" 2>/dev/null) 2>/dev/null; then \
		echo "Inspector already running. Stop it first with: make test-e2e-inspect-stop"; \
		exit 1; \
	fi
	@rm -f /tmp/astonish-e2e-inspect.json /tmp/astonish-e2e-inspect.secret
	@echo ""
	@echo "Draining any sandbox pods left over from previous runs..."
	@$(E2E_INSPECT_BIN) --stop-pods 2>&1 | sed 's/^/  /'
	@echo ""
	@echo "Starting e2e inspector on port $(E2E_INSPECT_PORT) (log: $(E2E_INSPECT_LOG))..."
	@nohup $(E2E_INSPECT_BIN) > $(E2E_INSPECT_LOG) 2>&1 & echo $$! > /tmp/astonish-e2e-inspect.pid
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do \
		if [ -f /tmp/astonish-e2e-inspect.json ]; then break; fi; \
		sleep 1; \
	done
	@if [ ! -f /tmp/astonish-e2e-inspect.json ]; then \
		echo "ERROR: inspector failed to start within 15s. Log:"; \
		cat $(E2E_INSPECT_LOG); \
		exit 1; \
	fi
	@echo "Inspector started. Tail of startup log:"
	@tail -10 $(E2E_INSPECT_LOG) | sed 's/^/  /'
	@echo ""
	@echo "Running E2E tests against shared inspector instance..."
	@set -o pipefail; ASTONISH_E2E_KEEP_ALIVE=1 \
		go test -tags=e2e -count=1 -p 1 -timeout=15m -json ./tests/e2e/... \
		| node tests/scenarios/stream.mjs /tmp/e2e-results.json; \
	RESULT=$$?; \
	node tests/scenarios/parse-run.mjs /tmp/e2e-results.json; \
	echo ""; \
	if [ $$RESULT -ne 0 ]; then \
		echo "Some tests failed — inspector is still running so you can investigate."; \
	fi; \
	echo ""; \
	echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"; \
	echo "Inspector still running with all tests' chat sessions captured."; \
	echo ""; \
	echo "Open in browser:"; \
	HOST=$$(hostname -f 2>/dev/null || hostname); \
	echo "  http://localhost:$(E2E_INSPECT_PORT)        (from this host)"; \
	if [ "$$HOST" != "localhost" ] && [ -n "$$HOST" ]; then \
		echo "  http://$$HOST:$(E2E_INSPECT_PORT)  (from any host that can reach this one)"; \
	fi; \
	echo ""; \
	echo "Or tunnel from your laptop:"; \
	echo "  ssh -L $(E2E_INSPECT_PORT):localhost:$(E2E_INSPECT_PORT) $${USER}@$$HOST"; \
	echo "  Then open: http://localhost:$(E2E_INSPECT_PORT)"; \
	echo ""; \
	$(E2E_INSPECT_BIN) --info; \
	echo ""; \
	echo "Stop inspector:           make test-e2e-inspect-stop"; \
	echo "Drain sandbox pods only:  $(E2E_INSPECT_BIN) --stop-pods"; \
	echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Stop inspector + drop its databases + clean tmp files. Always idempotent:
# safe to run when nothing is running, when only DBs are orphaned, or when
# only state files are stale. Order: kill first (using PID file or pgrep
# fallback), then call `e2e-inspector --cleanup` which drops the DBs and
# removes /tmp/astonish-e2e-inspect-* dirs and state files.
test-e2e-inspect-stop: $(E2E_INSPECT_BIN)
	@if [ -z "$$ASTONISH_TEST_DSN" ]; then echo "ERROR: ASTONISH_TEST_DSN required (used to drop databases)"; exit 1; fi
	@PIDS=""; \
	if [ -f /tmp/astonish-e2e-inspect.pid ]; then \
		FILE_PID=$$(cat /tmp/astonish-e2e-inspect.pid 2>/dev/null); \
		if [ -n "$$FILE_PID" ] && kill -0 $$FILE_PID 2>/dev/null; then \
			PIDS="$$FILE_PID"; \
		fi; \
	fi; \
	PGREP_PIDS=$$(pgrep -x e2e-inspector 2>/dev/null || true); \
	for P in $$PGREP_PIDS; do \
		case " $$PIDS " in *" $$P "*) ;; *) PIDS="$$PIDS $$P" ;; esac; \
	done; \
	if [ -n "$$PIDS" ]; then \
		for PID in $$PIDS; do \
			echo "Stopping inspector (PID $$PID)..."; \
			kill -TERM $$PID 2>/dev/null || true; \
		done; \
		for i in 1 2 3 4 5 6 7 8 9 10; do \
			ALIVE=""; \
			for PID in $$PIDS; do \
				if kill -0 $$PID 2>/dev/null; then ALIVE="$$ALIVE $$PID"; fi; \
			done; \
			[ -z "$$ALIVE" ] && break; \
			sleep 1; \
		done; \
		for PID in $$PIDS; do \
			if kill -0 $$PID 2>/dev/null; then \
				echo "PID $$PID did not exit gracefully, sending SIGKILL"; \
				kill -KILL $$PID 2>/dev/null || true; \
			fi; \
		done; \
	else \
		echo "No inspector process running."; \
	fi
	@rm -f /tmp/astonish-e2e-inspect.pid
	@$(E2E_INSPECT_BIN) --cleanup

# Scenario coverage report — reads YAML catalogs under tests/scenarios/
scenario-coverage:
	@node tests/scenarios/report.mjs

# Install to ~/bin
install: build-all
	@echo "Installing $(BINARY_NAME) to ~/bin..."
	@mkdir -p ~/bin
	cp $(BINARY_NAME) ~/bin/
	@echo "Installed successfully!"

# Clean up build artifacts
clean:
	@echo "Cleaning up build artifacts..."
	rm -rf $(BINARY_NAME)
	rm -f astonish-linux-amd64 astonish-linux-arm64
	rm -rf $(WEB_DIR)/dist
	rm -rf $(WEB_DIR)/node_modules
	@echo "Cleanup complete!"

# Update MCP store star counts from GitHub
update-mcp-stars:
	@echo "Updating MCP server star counts..."
	GITHUB_TOKEN=$$(gh auth token) python3 scripts/update-mcp-stars.py
	@echo "Star counts updated!"

.PHONY: all help build build-ui build-all run studio studio-dev test test-unit test-integration test-e2e test-e2e-sqlite test-e2e-inspect test-e2e-inspect-stop e2e-k8s-up e2e-k8s-down install clean update-mcp-stars setup-hooks platform-init create-secrets e2e-env-up e2e-env-down e2e-env-rebuild docker-up docker-down docker-rebuild build-linux build-linux-arm64 docker-incus docker-sandbox-openshell ensure-builder push-dev push-incus-dev push-sandbox-base-dev push-sandbox-openshell-dev push-all-dev push-dev-fast push-sandbox-base-dev-fast push-incus-dev-fast push-sandbox-openshell-dev-fast push-all-dev-fast ent-generate proto-gen

# Docker Test Environment - isolated environment for running integration/E2E tests
e2e-env-up:
	@echo "Starting isolated test environment..."
	docker compose -f docker-compose.e2e.yml up -d --build
	@echo "Astonish running at http://localhost:9393"

e2e-env-down:
	@echo "Stopping test environment..."
	docker compose -f docker-compose.e2e.yml down
	@echo "Test environment stopped."

e2e-env-rebuild:
	@echo "Rebuilding test environment..."
	docker compose -f docker-compose.e2e.yml down
	docker compose -f docker-compose.e2e.yml up -d --build
	@echo "Astonish running at http://localhost:9393"

# Docker Production - Persistent environment
docker-up: build-linux build-linux-arm64
	@echo "Starting persistent container..."
	docker compose up -d --build
	@echo "Astonish running at http://localhost:9393"
	@echo "Data stored in ./.astonish-data"

docker-down:
	@echo "Stopping persistent container..."
	docker compose down
	@echo "Container stopped."

docker-rebuild: build-linux build-linux-arm64
	@echo "Rebuilding persistent container..."
	docker compose down
	docker compose up -d --build
	@echo "Astonish running at http://localhost:9393"

# Platform init via Kubernetes (run inside cluster)
platform-init:
ifndef PLATFORM_HOST
	$(error PLATFORM_HOST is required. Usage: make platform-init PLATFORM_HOST=... PLATFORM_USER=... PLATFORM_PASSWORD=...)
endif
ifndef PLATFORM_USER
	$(error PLATFORM_USER is required. Usage: make platform-init PLATFORM_HOST=... PLATFORM_USER=... PLATFORM_PASSWORD=...)
endif
ifndef PLATFORM_PASSWORD
	$(error PLATFORM_PASSWORD is required. Usage: make platform-init PLATFORM_HOST=... PLATFORM_USER=... PLATFORM_PASSWORD=...)
endif
	kubectl run astonish-platform-init --rm -it --restart=Never \
	  -n astonish --image=$(DOCKER_REGISTRY)/astonish:$(DEV_TAG) --image-pull-policy=Always \
	  --command -- astonish platform init \
	  --host $(PLATFORM_HOST) --user $(PLATFORM_USER) --password $(PLATFORM_PASSWORD)

# Create Kubernetes secrets for Astonish
create-secrets:
ifndef K8S_NAMESPACE
	$(error K8S_NAMESPACE is required. Usage: make create-secrets K8S_NAMESPACE=... PLATFORM_DSN=...)
endif
ifndef PLATFORM_DSN
	$(error PLATFORM_DSN is required. Usage: make create-secrets K8S_NAMESPACE=... PLATFORM_DSN=...)
endif
	kubectl -n $(K8S_NAMESPACE) create secret generic astonish-secrets \
	  --from-literal=master-key="$$(openssl rand -hex 32)" \
	  --from-literal=jwt-secret="$$(openssl rand -hex 32)" \
	  --from-literal=platform-dsn="$(PLATFORM_DSN)"

# Cross-compile Linux binary (for macOS/Windows dev pushing into sandbox containers)
build-linux:
	@echo "Cross-compiling Linux amd64 binary..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/SAP/astonish/cmd/astonish.Version=$(VERSION)" -o astonish-linux-amd64 .
	@echo "Linux binary built: astonish-linux-amd64"

build-linux-arm64:
	@echo "Cross-compiling Linux arm64 binary..."
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/SAP/astonish/cmd/astonish.Version=$(VERSION)" -o astonish-linux-arm64 .
	@echo "Linux binary built: astonish-linux-arm64"

# Generate Go gRPC stubs from vendored NVIDIA OpenShell proto files
proto-gen:
	@echo "Generating Go gRPC stubs from proto/openshell/v1/..."
	buf generate
	@echo "Generated: pkg/sandbox/openshell/gen/openshellv1/"

# Update Helm chart dependencies (pull OpenShell subchart archive)
helm-deps:
	helm dependency update deploy/helm/astonish
	@echo "Helm dependencies updated (Chart.lock + charts/ archive)"

# Build the Incus Docker image for local use (macOS/Windows Docker+Incus).
#
# Pitfalls this target avoids:
#   1. docker-container buildx driver discards the result unless --load is set
#      (plain `docker build` can leave the image only in build cache).
#   2. Dockerfile COPYs astonish-linux-${TARGETARCH}; on Apple Silicon that is
#      arm64, so we must build that binary — not only amd64.
#   3. Runtime DockerImageTag() uses :latest when host version is "dev", not :dev.
#      When VERSION=dev we dual-tag so EnsureIncusDockerContainer can find it.
#
# After this: docker rm -f astonish-incus && astonish sandbox refresh --force
# and start a new chat session (existing session containers keep the old binary).
docker-incus:
	@if [ "$(DEV_ARCH)" = "arm64" ]; then \
		$(MAKE) build-linux-arm64; \
	else \
		$(MAKE) build-linux; \
	fi
	@echo "Building Incus Docker image (linux/$(DEV_ARCH)) and loading into local Docker..."
	# Single -t with --load: multi-tag --load is unreliable on docker-container
	# buildx (only the first tag may stick). Retag explicitly afterward.
	docker buildx build \
		--platform linux/$(DEV_ARCH) \
		-f docker/incus/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-incus:$(VERSION) \
		--load \
		.
	@echo "Image loaded: $(DOCKER_REGISTRY)/astonish-incus:$(VERSION)"
	@if [ "$(VERSION)" = "dev" ]; then \
		docker tag $(DOCKER_REGISTRY)/astonish-incus:dev $(DOCKER_REGISTRY)/astonish-incus:latest; \
		echo "Also tagged: $(DOCKER_REGISTRY)/astonish-incus:latest (runtime looks for this when host version is dev)"; \
	fi
	@echo "Next: docker rm -f astonish-incus && ./astonish sandbox refresh --force  (then new chat session)"

# Build the OpenShell sandbox image (NVIDIA sandbox base + Astonish agent tools)
docker-sandbox-openshell: build-linux
	@echo "Building OpenShell sandbox image..."
	docker build -f docker/sandbox-openshell/Dockerfile -t $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(VERSION) .
	@echo "Image built: $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(VERSION)"

# --- Registry Push (multi-arch via buildx) ---

# Ensure a buildx builder with multi-platform support exists
ensure-builder:
	@docker buildx inspect astonish-builder >/dev/null 2>&1 || \
		docker buildx create --name astonish-builder --use --driver docker-container --bootstrap
	@docker buildx use astonish-builder

# Build and push the main Astonish image (multi-arch)
# Cross-compiles Go on the host to avoid QEMU segfaults with Go 1.26.
push-dev: ensure-builder build-linux build-linux-arm64
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish:$(DEV_TAG) (linux/amd64,linux/arm64)..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f docker/astonish/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish:$(DEV_TAG)"

# Build and push the Incus image (multi-arch)
# Requires local cross-compiled binaries for both architectures
push-incus-dev: ensure-builder build-linux build-linux-arm64
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish-incus:$(DEV_TAG) (linux/amd64,linux/arm64)..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f docker/incus/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-incus:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish-incus:$(DEV_TAG)"

# Build and push the sandbox base image (multi-arch).
# Cross-compiles Go inside the Dockerfile on $BUILDPLATFORM (avoids QEMU Go 1.26 panics).
# Requires web/dist/ for go:embed (run `make build-ui` first if missing).
push-sandbox-base-dev: ensure-builder
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish-sandbox-base:$(DEV_TAG) (linux/amd64,linux/arm64)..."
	docker buildx build --platform linux/amd64,linux/arm64 \
		-f docker/sandbox-base/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-sandbox-base:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish-sandbox-base:$(DEV_TAG)"

# Build and push the OpenShell sandbox image (amd64 only — KasmVNC + CloakBrowser are amd64)
push-sandbox-openshell-dev: ensure-builder build-linux
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(DEV_TAG) (linux/amd64)..."
	docker buildx build --platform linux/amd64 \
		-f docker/sandbox-openshell/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(DEV_TAG)"

# Push all dev images
push-all-dev: push-dev push-incus-dev push-sandbox-base-dev push-sandbox-openshell-dev
	@echo "All dev images pushed successfully!"

# --- Fast single-arch dev builds (native architecture only) ---
# These skip multi-arch cross-compilation for faster iteration during development.
# DEV_ARCH is defined near the top of this Makefile.

# Fast: build and push main Astonish API image (single arch)
push-dev-fast: ensure-builder build-linux build-linux-arm64
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish:$(DEV_TAG) (linux/$(DEV_ARCH) only)..."
	docker buildx build --platform linux/$(DEV_ARCH) \
		-f docker/astonish/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish:$(DEV_TAG)"

# Fast: build and push sandbox base image (single arch)
push-sandbox-base-dev-fast: ensure-builder
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish-sandbox-base:$(DEV_TAG) (linux/$(DEV_ARCH) only)..."
	docker buildx build --platform linux/$(DEV_ARCH) \
		-f docker/sandbox-base/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-sandbox-base:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish-sandbox-base:$(DEV_TAG)"

# Fast: build and push Incus image (single arch). Build the binary for DEV_ARCH
# (arm64 on Apple Silicon), not only amd64.
push-incus-dev-fast: ensure-builder
	@if [ "$(DEV_ARCH)" = "arm64" ]; then \
		$(MAKE) build-linux-arm64; \
	else \
		$(MAKE) build-linux; \
	fi
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish-incus:$(DEV_TAG) (linux/$(DEV_ARCH) only)..."
	docker buildx build --platform linux/$(DEV_ARCH) \
		-f docker/incus/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-incus:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish-incus:$(DEV_TAG)"

# Fast: build and push OpenShell sandbox image (amd64 only — KasmVNC + CloakBrowser are amd64)
push-sandbox-openshell-dev-fast: ensure-builder build-linux
	@echo "Building and pushing $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(DEV_TAG) (linux/amd64)..."
	docker buildx build --platform linux/amd64 \
		-f docker/sandbox-openshell/Dockerfile \
		-t $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(DEV_TAG) \
		--push .
	@echo "Pushed: $(DOCKER_REGISTRY)/astonish-sandbox-openshell:$(DEV_TAG)"

# Fast: push all dev images (single arch)
push-all-dev-fast: push-dev-fast push-sandbox-base-dev-fast push-incus-dev-fast push-sandbox-openshell-dev-fast
	@echo "All dev images pushed ($(DEV_ARCH) only)!"

# Regenerate Ent client code for all scopes.
ent-generate:
	@echo "Generating Ent clients..."
	@cd ent/platform && go run generate.go
	@cd ent/org && go run generate.go
	@cd ent/team && go run generate.go
	@cd ent/personal && go run generate.go
	@echo "Done."
