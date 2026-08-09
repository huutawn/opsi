VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || printf '%040d' 0)
LDFLAGS := -X main.version=$(VERSION) -X main.revision=$(GIT_COMMIT)
CLI_LDFLAGS := $(LDFLAGS) -X github.com/opsi-dev/opsi/cli/internal/commands.localUIVersion=$(VERSION) -X github.com/opsi-dev/opsi/cli/internal/commands.localUIRevision=$(GIT_COMMIT)
AGENT_VERSION ?= $(VERSION)
AGENT_COMMIT ?= $(GIT_COMMIT)
AGENT_RELEASE_DIR ?= dist/agent
AGENT_LDFLAGS := -X main.version=$(AGENT_VERSION) -X main.commit=$(AGENT_COMMIT)
GO_VERSION ?= 1.26.4
NODE_VERSION ?= 24.16.0
NPM_VERSION ?= 11.17.0
GOCACHE ?= /tmp/opsi-go-cache
GOTOOLCHAIN ?= local
UI_NPM ?= npm
RUN :=
PROXY :=
DEV_CONTROL_PLANE_COMPOSE := docker compose --env-file deploy/dev-control-plane/.env -f deploy/dev-control-plane/compose.yaml
DEV_CONTROL_PLANE_EXAMPLE_COMPOSE := docker compose --env-file deploy/dev-control-plane/.env.example -f deploy/dev-control-plane/compose.yaml
STAGING_CONTROL_PLANE_COMPOSE := docker compose --env-file deploy/staging-control-plane/.env -f deploy/staging-control-plane/compose.yaml
STAGING_CONTROL_PLANE_EXAMPLE_COMPOSE := docker compose --env-file deploy/staging-control-plane/.env.example -f deploy/staging-control-plane/compose.yaml

.PHONY: check-toolchain verify test verify-postgres build build-cli-release verify-cli-release verify-cli-installer verify-cli-clean-install agent-release verify-agent-release verify-dr verify-dr-full verify-e2e-k3s-preflight verify-e2e-k3s verify-e2e-k3s-selfcheck verify-e2e-node-lifecycle-preflight verify-e2e-node-lifecycle verify-e2e-node-lifecycle-selfcheck verify-dev-control-plane-preflight verify-dev-control-plane-clean-vm verify-r5-005-github-app-preflight verify-bootstrap-worker-release ui-build ui-test ui-lint lint source-hygiene package-source check-source-package verify-source-package-policy clean e2e-dry-run release smoke-release dev-control-plane-validate-source dev-control-plane-validate dev-control-plane-build dev-control-plane-up dev-control-plane-down verify-staging-control-plane-policy verify-staging-control-plane-caddy-smoke staging-control-plane-validate-source staging-control-plane-validate staging-control-plane-up staging-control-plane-down

check-toolchain:
	@go version | grep -q "go$(GO_VERSION)" || { echo "Go $(GO_VERSION) required"; go version; exit 1; }
	@node --version | grep -qx "v$(NODE_VERSION)" || { echo "Node $(NODE_VERSION) required"; node --version; exit 1; }
	@$(UI_NPM) --version | grep -qx "$(NPM_VERSION)" || { echo "npm $(NPM_VERSION) required"; $(UI_NPM) --version; exit 1; }

verify-r5-005-github-app-preflight:
	@PYTHONDONTWRITEBYTECODE=1 python3 scripts/verify_r5_005_github_app_preflight_test.py

verify-bootstrap-worker-release:
	@PYTHONDONTWRITEBYTECODE=1 python3 scripts/control-plane-release-test.py
	@PYTHONDONTWRITEBYTECODE=1 python3 scripts/bootstrap-worker-release-test.py
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOWORK=off GOTOOLCHAIN=$(GOTOOLCHAIN) go list -mod=readonly -deps \
	  ./cmd/opsi-cloud ./cmd/opsi-bootstrap-worker >/dev/null

verify: check-toolchain source-hygiene lint test ui-test ui-build ui-lint

test:
	cd contracts/go && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./cmd/... ./internal/...
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...

verify-postgres:
	@set -eu; \
	container="opsi-r5-016-postgres-$$PPID"; \
	dsn="$${OPSI_TEST_DATABASE_URL:-}"; cleanup() { test -z "$${started:-}" || docker rm -f "$$container" >/dev/null; }; trap cleanup EXIT INT TERM; \
	if test -z "$$dsn"; then \
		command -v docker >/dev/null 2>&1 || { echo "Docker is required when OPSI_TEST_DATABASE_URL is unset"; exit 1; }; \
		docker rm -f "$$container" >/dev/null 2>&1 || :; \
		docker run -d --rm --name "$$container" -e POSTGRES_USER=opsi -e POSTGRES_PASSWORD=opsi -e POSTGRES_DB=opsi -p 127.0.0.1::5432 postgres:16 >/dev/null; started=1; \
		for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do docker exec "$$container" pg_isready -U opsi -d opsi >/dev/null 2>&1 && break; test "$$attempt" -eq 12 || sleep 1; done; \
		port="$$(docker port "$$container" 5432/tcp | awk -F: '{print $$2}')"; dsn="postgres://opsi:opsi@127.0.0.1:$$port/opsi?sslmode=disable"; \
	fi; \
	cd cloud; OPSI_TEST_DATABASE_URL="$$dsn" OPSI_REQUIRE_POSTGRES_TESTS=1 GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test -tags postgresintegration -p 1 ./internal/postgres ./internal/actiondevice ./internal/registry ./internal/adminbootstrap -run 'Test(Postgres|R5012)' -count=1

verify-dr:
	$(RUN) ./scripts/verify-dr.sh

verify-dr-full: verify-dr

verify-e2e-k3s-preflight:
	$(RUN) ./scripts/e2e/verify-k3s.sh --preflight

verify-e2e-k3s:
	$(RUN) ./scripts/e2e/verify-k3s.sh

verify-e2e-k3s-selfcheck:
	@PYTHONDONTWRITEBYTECODE=1 python3 scripts/e2e/second_factor_handoff_test.py
	$(RUN) ./scripts/e2e/verify-k3s.sh --self-test
	@if rg -n 'OPSI_E2E_APPROVE_MITIGATION|incidents/.*/analyze|incidents/.*/actions/.*/approve|recommended_actions|action_hash' scripts/e2e/verify-k3s.sh; then echo "stale incident RCA/approval E2E dependency found"; exit 1; fi

verify-e2e-node-lifecycle-preflight:
	$(RUN) ./scripts/e2e/verify-node-lifecycle.sh --preflight

verify-e2e-node-lifecycle:
	$(RUN) ./scripts/e2e/verify-node-lifecycle.sh

verify-e2e-node-lifecycle-selfcheck:
	$(RUN) ./scripts/e2e/verify-node-lifecycle.sh --self-test

verify-dev-control-plane-preflight:
	./scripts/e2e/verify-dev-control-plane.sh --preflight

verify-dev-control-plane-clean-vm:
	./scripts/e2e/verify-dev-control-plane.sh \
	  --evidence docs/evidence/v3-013-clean-vm.md

build:
	$(RUN) mkdir -p bin
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(AGENT_LDFLAGS)" -o ../bin/opsi-agent ./cmd/opsi-agent
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(CLI_LDFLAGS)" -o ../bin/opsi ./cmd/opsi
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-cloud ./cmd/opsi-cloud
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-bootstrap-worker ./cmd/opsi-bootstrap-worker

build-cli-release:
	cd cli/ui && $(RUN) $(UI_NPM) ci
	cd cli/ui && $(RUN) $(UI_NPM) run build
	$(RUN) rm -rf dist/cli
	$(RUN) mkdir -p dist/cli
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		bundle=$$(mktemp -d); \
		GOOS=$$os GOARCH=$$arch env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -trimpath -ldflags "$(CLI_LDFLAGS)" -o "$$bundle/opsi" ./cli/cmd/opsi; \
		cp -R cli/ui/out "$$bundle/opsi-ui"; \
		tar -C "$$bundle" -czf "dist/cli/opsi-$(VERSION)-$$os-$$arch.tar.gz" opsi opsi-ui; \
		rm -rf "$$bundle"; \
	done
	@cd dist/cli && sha256sum opsi-*.tar.gz > checksums.txt

verify-cli-release: build-cli-release
	@set -eu; \
	os=$$(uname -s | tr '[:upper:]' '[:lower:]'); arch=$$(uname -m); \
	case "$$os/$$arch" in linux/x86_64) arch=amd64;; linux/aarch64|linux/arm64) arch=arm64;; darwin/x86_64) arch=amd64;; darwin/arm64) ;; *) echo "unsupported bundle smoke platform"; exit 1;; esac; \
	prefix=$$(mktemp -d); port=19780; pid=; \
	trap 'test -z "$$pid" || kill "$$pid" 2>/dev/null || :; rm -rf "$$prefix"' EXIT HUP INT TERM; \
	tar -C "$$prefix" -xzf "dist/cli/opsi-$(VERSION)-$$os-$$arch.tar.gz"; \
	version_json=$$(HOME="$$prefix/home" "$$prefix/opsi" version --json); \
	printf '%s\n' "$$version_json" | grep -q '"version":"$(VERSION)"'; \
	printf '%s\n' "$$version_json" | grep -q '"revision":"$(GIT_COMMIT)"'; \
	HOME="$$prefix/home" "$$prefix/opsi" start --addr "127.0.0.1:$$port" >"$$prefix/start.log" 2>&1 & pid=$$!; \
	for attempt in 1 2 3 4 5 6 7 8 9 10; do curl --fail --silent "http://127.0.0.1:$$port/health" >/dev/null && break; sleep 1; done; \
	curl --fail --silent "http://127.0.0.1:$$port/health" | grep -q '"status":"ok"'; \
	curl --fail --silent "http://127.0.0.1:$$port/" | grep -q '<title>Opsi'; \
	curl --fail --silent "http://127.0.0.1:$$port/api/local/settings" | grep -q '"cloud_authority":"https://opsidev.site"'; \
	curl --fail --silent "http://127.0.0.1:$$port/api/local/session" | grep -q '"agent_connected":"not connected"'; \
	test -f "$$prefix/opsi-ui/index.html"

verify-cli-installer:
	@OPSI_INSTALLER_SELF_TEST=1 OPSI_INSTALL_DIR=/tmp/opsi-installer-self-test ./scripts/install-cli.sh

verify-cli-clean-install: build-cli-release
	@./scripts/verify-clean-cli-install.sh "$(VERSION)" "$(GIT_COMMIT)" dist/cli

agent-release:
	$(RUN) env GOCACHE="$(GOCACHE)" GOTOOLCHAIN="$(GOTOOLCHAIN)" ./scripts/build-agent-release.sh "$(AGENT_COMMIT)" "$(AGENT_RELEASE_DIR)"

verify-agent-release:
	@PYTHONDONTWRITEBYTECODE=1 python3 scripts/agent-release-test.py
	$(RUN) env GOTOOLCHAIN="$(GOTOOLCHAIN)" ./scripts/verify-agent-release.sh

ui-build:
	cd cli/ui && $(RUN) $(UI_NPM) ci
	cd cli/ui && $(RUN) $(UI_NPM) run build

ui-test:
	cd cli/ui && $(RUN) $(UI_NPM) test

ui-lint:
	cd cli/ui && $(RUN) $(UI_NPM) run lint

lint:
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./...
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./cmd/... ./internal/...
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./...
	cd contracts/go && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./...

source-hygiene: verify-source-package-policy verify-bootstrap-worker-release verify-agent-release
	$(RUN) ./scripts/source-package.sh check-tree
	@if sed -n '/func (s \*Service) expireDeploymentLeasesLocked/,/^}/p' cloud/internal/registry/service.go | rg -n 'delete\(s\.deployLocks'; then echo "canonical lease exhaustion deletes service ownership"; exit 1; fi
	@if sed -n '/func expireDeploymentLeases(/,/^}/p' cloud/internal/registry/postgres.go | rg -n 'DELETE FROM service_deployment_locks'; then echo "Postgres canonical lease exhaustion deletes service ownership"; exit 1; fi
	@if sed -n '/func (s \*Service) acquireDeploymentLockLocked/,/^}/p' cloud/internal/registry/service.go | rg -n 'ExpiresAt\.After'; then echo "in-memory deployment lock takeover depends on TTL"; exit 1; fi
	@if sed -n '/func acquireDeploymentLock(/,/^}/p' cloud/internal/registry/postgres.go | rg -n 'service_deployment_locks\.expires_at[[:space:]]*<='; then echo "Postgres deployment lock takeover depends on TTL"; exit 1; fi
	@for file in cloud/internal/registry/service.go cloud/internal/registry/postgres.go; do body="$$(sed -n '/func .*CancelDeployment/,/^}/p' "$$file")"; printf '%s\n' "$$body" | rg -q 'job\.AttemptCount != 0' && printf '%s\n' "$$body" | rg -q 'job\.RolloutState' || { echo "cancel safety history guard missing in $$file"; exit 1; }; done
	@if ! sed -n '/func (s \*Service) acquireDeploymentLockLocked/,/^}/p' cloud/internal/registry/service.go | rg -q 'lock\.DeploymentID != deploymentID' || ! sed -n '/func (s \*Service) RetryDeployment/,/^}/p' cloud/internal/registry/service.go | rg -q 'acquireDeploymentLockLocked\(job\.ServiceID, job\.ID'; then echo "in-memory retry cannot renew its own ownership"; exit 1; fi
	@if ! sed -n '/func acquireDeploymentLock(/,/^}/p' cloud/internal/registry/postgres.go | rg -q 'id<>[$$]2' || ! sed -n '/func (s PostgresService) RetryDeployment/,/^}/p' cloud/internal/registry/postgres.go | rg -q 'acquireDeploymentLock\(ctx, tx, projectID, job\.ServiceID, job\.ID'; then echo "Postgres retry cannot renew its own ownership"; exit 1; fi
	@legacy_action='rate_limit_'ingress; legacy_annotation='nginx.ingress.kubernetes.io/'limit-rps; if rg -n "$$legacy_action|$$legacy_annotation" . --glob '!docs/archive/**' --glob '!docs/opsi-roadmap-v3/**' --glob '!docs/opsi_roadmap_v3/**' --glob '!.git/**'; then echo "legacy ingress remediation reference found"; exit 1; fi
	@if rg -n 'IngressEnabled|Traefik-safe graceful shutdown defaults|sleep 10' agent cli cloud contracts --glob '!**/*_test.go'; then echo "removed ingress deployment capability found in production code"; exit 1; fi
	@if rg -n 'bool ingress_enabled|json:"ingress_enabled|yaml:"ingress_enabled|^[[:space:]]*ingress_enabled:' agent cli cloud contracts --glob '!**/*_test.go'; then echo "removed ingress deployment config or contract found"; exit 1; fi
	@if rg -n '"ingress"' cli/internal/commands --glob '!**/*_test.go'; then echo "removed --ingress CLI flag found"; exit 1; fi
	@if rg -n 'runLegacyDevDeployment|RequestFromWebhook|PollWebhook|PollDeployment|ExecGitClient|ContainerdBuilder|KubectlAdapter|handleGitHubWebhook|NewPostgresQueue|EnableDebugUI|queued_webhooks' agent cli cloud contracts --glob '!**/*_test.go'; then echo "retired delivery implementation found"; exit 1; fi
	@if rg -n '"/v1/webhooks/github"|services/.*/deployments|routes\[\].*webhook_secret' agent cli cloud contracts deploy scripts --glob '!**/*_test.go'; then echo "retired delivery route found"; exit 1; fi
	@if rg -n 'git clone|buildx build|nerdctl.*build|renderManifestFile|/tmp/opsi-builds' agent --glob '!**/*_test.go'; then echo "Agent source-build path found"; exit 1; fi
	@if rg -n 'Mode:[[:space:]]*"immutable_image"|mode[[:space:]]*[:=][[:space:]]*"immutable_image"' cloud/internal/registry --glob '*.go' --glob '!**/*_test.go'; then echo "new active immutable_image job creation found"; exit 1; fi
	@if rg -n 'func \([^)]*\) Deploy\(|Engine\.Deploy|ProductionAdapter\.Deploy' agent/internal/cloudrunner agent/internal/deploy --glob '*.go' --glob '!**/*_test.go'; then echo "retired direct Engine.Deploy entry point found"; exit 1; fi
	@if ! rg -n 'LEGACY_DEPLOYMENT_RETIRED' agent/internal/cloudrunner/runner.go agent/internal/cloudrunner/runner_test.go >/dev/null; then echo "missing fail-closed legacy command guard"; exit 1; fi
	@for symbol in StartImmutableDeployment AgentCommand PollJob ProductionAdapter ReconcileRollout; do rg -n "$$symbol" agent cloud contracts >/dev/null || { echo "canonical symbol missing: $$symbol"; exit 1; }; done
	@if rg -n 'FailureCode[[:space:]]*!=[[:space:]]*deploymentv1\.RolloutCodeNoKnownGood' agent/internal/cloudrunner/result.go; then echo "failure code inequality must not infer pre-mutation"; exit 1; fi
	@if ! rg -n 'IsFactualTerminalRollout\(record\)' agent/internal/cloudrunner/result.go >/dev/null || ! rg -n 'FailurePhase == deploymentv1\.FailurePhasePreMutation' agent/internal/cloudrunner/result.go >/dev/null || ! rg -n 'if !terminal' agent/internal/cloudrunner/runner.go >/dev/null; then echo "Agent factual terminal and explicit failure phase guard is missing"; exit 1; fi
	@if ! rg -n 'result\.FailurePhase == deploymentv1\.FailurePhasePreMutation' cloud/internal/registry/rollout.go >/dev/null || ! rg -n 'RolloutMutationObserved\(job\.RolloutState\)' cloud/internal/registry/rollout.go >/dev/null; then echo "Cloud failure phase and observed progress validation is missing"; exit 1; fi
	@if rg -ni 'password|sshpass|SSHPASS|accept-new|StrictHostKeyChecking=accept-new|auth_method.?[=:].?password|ssh_password' scripts/e2e/verify-k3s.sh; then echo "retired E2E SSH transport found"; exit 1; fi
	@if rg -n 'OPSI_E2E_SERVICE_REPO|OPSI_E2E_SERVICE_SHA|OPSI_E2E_BAD_SERVICE_SHA' scripts/e2e/verify-k3s.sh README.md agent/README.md docs/architecture.md docs/security_story.md docs/architecture_decisions/ADR-004-trusted-artifact-cd.md docs/architecture_decisions/ADR-006-immutable-manual-deployment.md docs/runbooks/clean_vps_k3s_e2e.md docs/current_state.md docs/status_matrix.md docs/opsi_roadmap_v5_production.md .agents/current.md; then echo "retired E2E source input found"; exit 1; fi
	@if rg -ni 'Agent currently (clones|builds)|current Agent.*(clone|build).*Git|Git deployment and user-provided manifest application exist|user manifests may contain their own resources|generic GitHub (push )?relay remains (active|current)|generic GitHub webhook relay is (active|current)' README.md agent/README.md docs/architecture.md docs/security_story.md docs/architecture_decisions/ADR-004-trusted-artifact-cd.md docs/architecture_decisions/ADR-006-immutable-manual-deployment.md docs/runbooks/clean_vps_k3s_e2e.md docs/current_state.md docs/status_matrix.md docs/opsi_roadmap_v5_production.md .agents/current.md; then echo "stale active delivery claim found"; exit 1; fi
	@if rg -ni 'BuildRecord.*(direct|directly).*(Engine\.Deploy|ProductionAdapter\.Deploy)|BuildRecord.*directly reaches Engine\.Deploy' README.md agent/README.md docs/architecture.md docs/security_story.md docs/architecture_decisions/ADR-004-trusted-artifact-cd.md docs/architecture_decisions/ADR-006-immutable-manual-deployment.md docs/runbooks/clean_vps_k3s_e2e.md docs/current_state.md docs/status_matrix.md docs/opsi_roadmap_v5_production.md .agents/current.md; then echo "stale direct BuildRecord-to-Engine claim found"; exit 1; fi
	@for token in rolled_back desired_digest 'current_digest' 'previous_digest' 'healthy A.*broken B.*restored A'; do rg -n "$$token" scripts/e2e/verify-k3s.sh >/dev/null || { echo "E2E rollback restoration gate missing: $$token"; exit 1; }; done
	@if ! rg -n 'select_fresh_incident "\$$service_id" "\$$bad_deployment_started_at"' scripts/e2e/verify-k3s.sh >/dev/null || ! rg -n 'incident\.get\("created_at_unix", 0\)' scripts/e2e/verify-k3s.sh >/dev/null || ! rg -n 'created_at >= minimum_created_at' scripts/e2e/verify-k3s.sh >/dev/null; then echo "E2E incident selection is missing the freshness boundary"; exit 1; fi
	@test ! -e .github/workflows/e2e-k3s.yml || { echo "retired GitHub-hosted K3s workflow restored"; exit 1; }

package-source: verify-source-package-policy
	$(RUN) ./scripts/source-package.sh build dist/opsi-source.tar.gz

check-source-package:
	$(RUN) ./scripts/source-package.sh check dist/opsi-source.tar.gz

verify-source-package-policy:
	$(RUN) ./scripts/source-package.sh self-test

clean:
	$(RUN) rm -rf bin release dist agent/opsi-agent cli/opsi cloud/opsi-cloud cloud/opsi-bootstrap-worker cli/ui/out cli/ui/.next cli/ui/node_modules cli/ui/tsconfig.tsbuildinfo headroom_memory.db coverage .tmp tmp

e2e-dry-run:
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/cloudrunner

release: build
	$(RUN) rm -rf release
	$(RUN) mkdir -p release/config.examples release/docs
	$(RUN) cp bin/opsi release/opsi
	$(RUN) cp bin/opsi-agent release/opsi-agent
	$(RUN) cp bin/opsi-cloud release/opsi-cloud
	$(RUN) cp bin/opsi-bootstrap-worker release/opsi-bootstrap-worker
	$(RUN) cp agent/config.example.yaml release/config.examples/agent.config.example.yaml
	$(RUN) cp cloud/config.example.json release/config.examples/cloud.config.example.json
	$(RUN) cp docs/demo_runbook.md release/docs/demo_runbook.md
	cd release && $(RUN) sha256sum opsi opsi-agent opsi-cloud opsi-bootstrap-worker > checksums.txt
	$(RUN) ./scripts/source-package.sh check-release release

smoke-release:
	$(PROXY) ./release/opsi version
	$(PROXY) ./release/opsi-agent --version
	$(PROXY) ./release/opsi-cloud --version

dev-control-plane-validate-source:
	@command -v docker >/dev/null 2>&1 || { echo "Docker is required"; exit 1; }
	@docker compose version >/dev/null 2>&1 || { echo "Docker Compose plugin is required"; exit 1; }
	@./scripts/validate-dev-control-plane.py --source
	@$(DEV_CONTROL_PLANE_EXAMPLE_COMPOSE) config --quiet

dev-control-plane-validate:
	@command -v docker >/dev/null 2>&1 || { echo "Docker is required"; exit 1; }
	@docker compose version >/dev/null 2>&1 || { echo "Docker Compose plugin is required"; exit 1; }
	@./scripts/validate-dev-control-plane.py
	@$(DEV_CONTROL_PLANE_COMPOSE) config --quiet

dev-control-plane-build: dev-control-plane-validate
	$(DEV_CONTROL_PLANE_COMPOSE) build

dev-control-plane-up: dev-control-plane-validate
	$(DEV_CONTROL_PLANE_COMPOSE) up -d

dev-control-plane-down:
	$(DEV_CONTROL_PLANE_COMPOSE) down

verify-staging-control-plane-policy:
	@python3 scripts/validate-staging-control-plane-test.py

verify-staging-control-plane-caddy-smoke: staging-control-plane-validate
	@./scripts/e2e/verify-staging-control-plane-caddy.sh

staging-control-plane-validate-source: verify-staging-control-plane-policy verify-bootstrap-worker-release
	@python3 scripts/validate-staging-control-plane.py --source
	@command -v docker >/dev/null 2>&1 || { echo "Docker is required for Compose parsing"; exit 1; }
	@docker compose version >/dev/null 2>&1 || { echo "Docker Compose plugin is required"; exit 1; }
	@$(STAGING_CONTROL_PLANE_EXAMPLE_COMPOSE) config --quiet

staging-control-plane-validate: verify-staging-control-plane-policy
	@python3 scripts/validate-staging-control-plane.py --runtime
	@$(STAGING_CONTROL_PLANE_COMPOSE) config --quiet

staging-control-plane-up: staging-control-plane-validate
	$(STAGING_CONTROL_PLANE_COMPOSE) up -d

staging-control-plane-down:
	$(STAGING_CONTROL_PLANE_COMPOSE) down
