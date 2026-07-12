VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GO_VERSION ?= 1.26.4
NODE_VERSION ?= 24.16.0
NPM_VERSION ?= 11.17.0
GOCACHE ?= /tmp/opsi-go-cache
GOTOOLCHAIN ?= local
UI_NPM ?= npm
RUN :=
PROXY :=
DEV_CONTROL_PLANE_COMPOSE := docker compose --env-file deploy/dev-control-plane/.env -f deploy/dev-control-plane/compose.yaml

.PHONY: check-toolchain verify test verify-postgres build verify-dr verify-dr-full verify-e2e-k3s-preflight verify-e2e-k3s verify-e2e-k3s-selfcheck verify-e2e-node-lifecycle-preflight verify-e2e-node-lifecycle verify-e2e-node-lifecycle-selfcheck ui-build ui-lint lint source-hygiene package-source check-source-package verify-source-package-policy clean e2e-dry-run release smoke-release dev-control-plane-validate dev-control-plane-build dev-control-plane-up dev-control-plane-down

check-toolchain:
	@go version | grep -q "go$(GO_VERSION)" || { echo "Go $(GO_VERSION) required"; go version; exit 1; }
	@node --version | grep -qx "v$(NODE_VERSION)" || { echo "Node $(NODE_VERSION) required"; node --version; exit 1; }
	@$(UI_NPM) --version | grep -qx "$(NPM_VERSION)" || { echo "npm $(NPM_VERSION) required"; $(UI_NPM) --version; exit 1; }

verify: check-toolchain source-hygiene lint test ui-build ui-lint

test:
	cd contracts/go && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./cmd/... ./internal/...
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./...

verify-postgres:
	@test -n "$$OPSI_TEST_DATABASE_URL" || { echo "OPSI_TEST_DATABASE_URL required for Postgres tests"; exit 1; }
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresBootstrapLeaseIsAtomicAcrossWorkers$$' | grep -qx 'TestPostgresBootstrapLeaseIsAtomicAcrossWorkers'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart$$' | grep -qx 'TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency$$' | grep -qx 'TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/adminbootstrap -list '^TestPostgresBootstrapOwnerIsIdempotentAcrossRestart$$' | grep -qx 'TestPostgresBootstrapOwnerIsIdempotentAcrossRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/webhookrelay -list '^TestPostgresQueuePersistsSanitizedJobsWhenDatabaseAvailable$$' | grep -qx 'TestPostgresQueuePersistsSanitizedJobsWhenDatabaseAvailable'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/webhookrelay -list '^TestPostgresRelayRetryScheduleSurvivesRestart$$' | grep -qx 'TestPostgresRelayRetryScheduleSurvivesRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresBootstrapLeaseIsAtomicAcrossWorkers$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresBootstrapLeaseHeartbeatRetryDeadLetterSurvivesRestart$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/adminbootstrap -run '^TestPostgresBootstrapOwnerIsIdempotentAcrossRestart$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/webhookrelay -run '^TestPostgres(QueuePersistsSanitizedJobsWhenDatabaseAvailable|RelayRetryScheduleSurvivesRestart)$$' -count=1

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

build:
	$(RUN) mkdir -p bin
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-agent ./cmd/opsi-agent
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi ./cmd/opsi
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-cloud ./cmd/opsi-cloud
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-bootstrap-worker ./cmd/opsi-bootstrap-worker

ui-build:
	cd cli/ui && $(RUN) $(UI_NPM) ci
	cd cli/ui && $(RUN) $(UI_NPM) run build

ui-lint:
	cd cli/ui && $(RUN) $(UI_NPM) run lint

lint:
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./...
	cd cli && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./cmd/... ./internal/...
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./...
	cd contracts/go && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go vet ./...

source-hygiene:
	$(RUN) ./scripts/source-package.sh check-tree
	@legacy_action='rate_limit_'ingress; legacy_annotation='nginx.ingress.kubernetes.io/'limit-rps; if rg -n "$$legacy_action|$$legacy_annotation" . --glob '!docs/archive/**' --glob '!docs/opsi-roadmap-v3/**' --glob '!docs/opsi_roadmap_v3/**' --glob '!.git/**'; then echo "legacy ingress remediation reference found"; exit 1; fi
	@if rg -n 'IngressEnabled|Traefik-safe graceful shutdown defaults|sleep 10' agent cli cloud contracts --glob '!**/*_test.go'; then echo "removed ingress deployment capability found in production code"; exit 1; fi
	@if rg -n 'bool ingress_enabled|json:"ingress_enabled|yaml:"ingress_enabled|^[[:space:]]*ingress_enabled:' agent cli cloud contracts --glob '!**/*_test.go'; then echo "removed ingress deployment config or contract found"; exit 1; fi
	@if rg -n '"ingress"' cli/internal/commands --glob '!**/*_test.go'; then echo "removed --ingress CLI flag found"; exit 1; fi

package-source:
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

dev-control-plane-validate:
	@command -v docker >/dev/null 2>&1 || { echo "Docker is required"; exit 1; }
	@docker compose version >/dev/null 2>&1 || { echo "Docker Compose plugin is required"; exit 1; }
	@for file in deploy/dev-control-plane/.env deploy/dev-control-plane/config/cloud.json deploy/dev-control-plane/config/bootstrap-worker.json; do \
		test -f "$$file" || { echo "missing runtime file: $$file"; exit 1; }; \
	done
	@for file in deploy/dev-control-plane/.env deploy/dev-control-plane/config/cloud.json deploy/dev-control-plane/config/bootstrap-worker.json; do \
		if rg -q 'REPLACE_WITH_|EXAMPLE_SECRET|CHANGE_ME' "$$file"; then echo "placeholder remains in $$file"; exit 1; fi; \
	done
	@$(DEV_CONTROL_PLANE_COMPOSE) config --quiet

dev-control-plane-build: dev-control-plane-validate
	$(DEV_CONTROL_PLANE_COMPOSE) build

dev-control-plane-up: dev-control-plane-validate
	$(DEV_CONTROL_PLANE_COMPOSE) up -d

dev-control-plane-down:
	$(DEV_CONTROL_PLANE_COMPOSE) down
