VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || printf '%040d' 0)
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

.PHONY: check-toolchain verify test verify-postgres build agent-release verify-agent-release verify-dr verify-dr-full verify-e2e-k3s-preflight verify-e2e-k3s verify-e2e-k3s-selfcheck verify-e2e-node-lifecycle-preflight verify-e2e-node-lifecycle verify-e2e-node-lifecycle-selfcheck verify-dev-control-plane-preflight verify-dev-control-plane-clean-vm verify-r5-005-github-app-preflight ui-build ui-test ui-lint lint source-hygiene package-source check-source-package verify-source-package-policy clean e2e-dry-run release smoke-release dev-control-plane-validate-source dev-control-plane-validate dev-control-plane-build dev-control-plane-up dev-control-plane-down verify-staging-control-plane-policy verify-staging-control-plane-caddy-smoke staging-control-plane-validate-source staging-control-plane-validate staging-control-plane-up staging-control-plane-down

check-toolchain:
	@go version | grep -q "go$(GO_VERSION)" || { echo "Go $(GO_VERSION) required"; go version; exit 1; }
	@node --version | grep -qx "v$(NODE_VERSION)" || { echo "Node $(NODE_VERSION) required"; node --version; exit 1; }
	@$(UI_NPM) --version | grep -qx "$(NPM_VERSION)" || { echo "npm $(NPM_VERSION) required"; $(UI_NPM) --version; exit 1; }

verify-r5-005-github-app-preflight:
	@PYTHONDONTWRITEBYTECODE=1 python3 scripts/verify_r5_005_github_app_preflight_test.py

verify: check-toolchain source-hygiene lint test ui-test ui-build ui-lint

test:
	cd contracts/go && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./cmd/... ./internal/...
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...

verify-postgres:
	@test -n "$$OPSI_TEST_DATABASE_URL" || { echo "OPSI_TEST_DATABASE_URL required for Postgres tests"; exit 1; }
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresBootstrapLeaseIsAtomicAcrossWorkers$$' | grep -qx 'TestPostgresBootstrapLeaseIsAtomicAcrossWorkers'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart$$' | grep -qx 'TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresImmutableDeploymentSnapshotAndEventsSurviveRestart$$' | grep -qx 'TestPostgresImmutableDeploymentSnapshotAndEventsSurviveRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresLegacyDeploymentIsRetiredWithoutBlockingCanonicalLease$$' | grep -qx 'TestPostgresLegacyDeploymentIsRetiredWithoutBlockingCanonicalLease'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresExposureRolloutSurvivesRestartAndSerializesConcurrentApply$$' | grep -qx 'TestPostgresExposureRolloutSurvivesRestartAndSerializesConcurrentApply'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/adminbootstrap -list '^TestPostgresBootstrapOwnerIsIdempotentAcrossRestart$$' | grep -qx 'TestPostgresBootstrapOwnerIsIdempotentAcrossRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresImmutableDeploymentSnapshotAndEventsSurviveRestart$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresLegacyDeploymentIsRetiredWithoutBlockingCanonicalLease$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresExposureRolloutSurvivesRestartAndSerializesConcurrentApply$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresBootstrapLeaseIsAtomicAcrossWorkers$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/adminbootstrap -run '^TestPostgresBootstrapOwnerIsIdempotentAcrossRestart$$' -count=1

verify-dr:
	$(RUN) ./scripts/verify-dr.sh

verify-dr-full: verify-dr

verify-e2e-k3s-preflight:
	$(RUN) ./scripts/e2e/verify-k3s.sh --preflight

verify-e2e-k3s:
	$(RUN) ./scripts/e2e/verify-k3s.sh

verify-e2e-k3s-selfcheck:
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
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi ./cmd/opsi
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-cloud ./cmd/opsi-cloud
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-bootstrap-worker ./cmd/opsi-bootstrap-worker

agent-release:
	$(RUN) env AGENT_VERSION="$(AGENT_VERSION)" AGENT_COMMIT="$(AGENT_COMMIT)" OUT_DIR="$(AGENT_RELEASE_DIR)" GOCACHE="$(GOCACHE)" GOTOOLCHAIN="$(GOTOOLCHAIN)" ./scripts/build-agent-release.sh

verify-agent-release:
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

source-hygiene: verify-source-package-policy
	$(RUN) ./scripts/source-package.sh check-tree
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
	@if rg -ni 'password|sshpass|SSHPASS|accept-new|StrictHostKeyChecking=accept-new|auth_method.?[=:].?password|ssh_password' scripts/e2e/verify-k3s.sh; then echo "retired E2E SSH transport found"; exit 1; fi
	@if rg -n 'OPSI_E2E_SERVICE_REPO|OPSI_E2E_SERVICE_SHA|OPSI_E2E_BAD_SERVICE_SHA' scripts/e2e/verify-k3s.sh README.md agent/README.md docs/architecture.md docs/security_story.md docs/architecture_decisions/ADR-004-trusted-artifact-cd.md docs/architecture_decisions/ADR-006-immutable-manual-deployment.md docs/runbooks/clean_vps_k3s_e2e.md docs/current_state.md docs/status_matrix.md docs/opsi_roadmap_v5_production.md .agents/current.md; then echo "retired E2E source input found"; exit 1; fi
	@if rg -ni 'Agent currently (clones|builds)|current Agent.*(clone|build).*Git|Git deployment and user-provided manifest application exist|user manifests may contain their own resources|generic GitHub (push )?relay remains (active|current)|generic GitHub webhook relay is (active|current)' README.md agent/README.md docs/architecture.md docs/security_story.md docs/architecture_decisions/ADR-004-trusted-artifact-cd.md docs/architecture_decisions/ADR-006-immutable-manual-deployment.md docs/runbooks/clean_vps_k3s_e2e.md docs/current_state.md docs/status_matrix.md docs/opsi_roadmap_v5_production.md .agents/current.md; then echo "stale active delivery claim found"; exit 1; fi
	@if rg -ni 'BuildRecord.*(direct|directly).*(Engine\.Deploy|ProductionAdapter\.Deploy)|BuildRecord.*directly reaches Engine\.Deploy' README.md agent/README.md docs/architecture.md docs/security_story.md docs/architecture_decisions/ADR-004-trusted-artifact-cd.md docs/architecture_decisions/ADR-006-immutable-manual-deployment.md docs/runbooks/clean_vps_k3s_e2e.md docs/current_state.md docs/status_matrix.md docs/opsi_roadmap_v5_production.md .agents/current.md; then echo "stale direct BuildRecord-to-Engine claim found"; exit 1; fi
	@for token in rolled_back desired_digest 'current_digest' 'previous_digest' 'healthy A.*broken B.*restored A'; do rg -n "$$token" scripts/e2e/verify-k3s.sh >/dev/null || { echo "E2E rollback restoration gate missing: $$token"; exit 1; }; done
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

staging-control-plane-validate-source: verify-staging-control-plane-policy
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
