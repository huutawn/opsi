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

.PHONY: check-toolchain verify test verify-postgres build verify-dr verify-e2e-k3s-preflight verify-e2e-k3s verify-e2e-k3s-selfcheck verify-e2e-node-lifecycle-preflight verify-e2e-node-lifecycle verify-e2e-node-lifecycle-selfcheck ui-build ui-lint lint source-hygiene package-source check-source-package clean e2e-dry-run release smoke-release

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
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/registry -list '^TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency$$' | grep -qx 'TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/webhookrelay -list '^TestPostgresQueuePersistsSanitizedJobsWhenDatabaseAvailable$$' | grep -qx 'TestPostgresQueuePersistsSanitizedJobsWhenDatabaseAvailable'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/webhookrelay -list '^TestPostgresRelayRetryScheduleSurvivesRestart$$' | grep -qx 'TestPostgresRelayRetryScheduleSurvivesRestart'
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/registry -run '^TestPostgresDeploymentJobRestartRetryDeadLetterAndIdempotency$$' -count=1
	cd cloud && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) OPSI_REQUIRE_POSTGRES_TESTS=1 go test ./internal/webhookrelay -run '^TestPostgres(QueuePersistsSanitizedJobsWhenDatabaseAvailable|RelayRetryScheduleSurvivesRestart)$$' -count=1

verify-dr:
	$(RUN) ./scripts/verify-dr.sh

verify-e2e-k3s-preflight:
	$(RUN) ./scripts/e2e/verify-k3s.sh --preflight

verify-e2e-k3s:
	$(RUN) ./scripts/e2e/verify-k3s.sh

verify-e2e-k3s-selfcheck:
	$(RUN) ./scripts/e2e/verify-k3s.sh --self-test

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
	@if $(PROXY) find . -type f \( -path './bin/*' -o -path './release/*' -o -path './cli/ui/out/*' -o -path './cli/ui/.next/*' -o -name 'opsi-agent' -o -name 'opsi-cloud' -o -name 'opsi-bootstrap-worker' -o -name 'opsi' -o -name '*.db' -o -name '*.sqlite' -o -name '*.sqlite-*' -o -name '*.sqlite3' -o -name '*.tsbuildinfo' \) -print | grep -n "."; then echo "source hygiene failed: run 'make clean' and do not commit generated artifacts"; exit 1; fi

package-source:
	$(RUN) mkdir -p dist
	$(RUN) tar --exclude-vcs --exclude='./bin' --exclude='./release' --exclude='./dist' --exclude='./agent/opsi-agent' --exclude='./cli/opsi' --exclude='./cloud/opsi-cloud' --exclude='./cloud/opsi-bootstrap-worker' --exclude='./cli/ui/out' --exclude='./cli/ui/.next' --exclude='./cli/ui/node_modules' --exclude='*.db' --exclude='*.sqlite' --exclude='*.sqlite-*' --exclude='*.sqlite3' --exclude='*.tsbuildinfo' --exclude='./coverage' --exclude='./.tmp' --exclude='./tmp' -czf dist/opsi-source.tar.gz --transform 's,^,opsi/,' .
	$(RUN) $(MAKE) check-source-package

check-source-package:
	@if $(RUN) tar -tzf dist/opsi-source.tar.gz | grep -En "(^opsi/bin/|^opsi/release/|opsi-agent$$|opsi-cloud$$|opsi-bootstrap-worker$$|/opsi$$|\\.db$$|\\.sqlite($$|-)|\\.sqlite3$$|tsconfig\\.tsbuildinfo|cli/ui/(out|\\.next|node_modules)/)"; then echo "source archive contains forbidden artifacts"; exit 1; fi

clean:
	$(RUN) rm -rf bin release dist agent/opsi-agent cli/opsi cloud/opsi-cloud cloud/opsi-bootstrap-worker cli/ui/out cli/ui/.next cli/ui/node_modules cli/ui/tsconfig.tsbuildinfo headroom_memory.db coverage .tmp tmp

e2e-dry-run:
	cd agent && $(RUN) env GOCACHE=$(GOCACHE) GOTOOLCHAIN=$(GOTOOLCHAIN) go test ./internal/cloudrunner

release: build
	$(RUN) mkdir -p release/config.examples release/docs
	$(RUN) cp bin/opsi release/opsi
	$(RUN) cp bin/opsi-agent release/opsi-agent
	$(RUN) cp bin/opsi-cloud release/opsi-cloud
	$(RUN) cp bin/opsi-bootstrap-worker release/opsi-bootstrap-worker
	$(RUN) cp docs/demo_runbook.md release/docs/demo_runbook.md
	cd release && $(RUN) sha256sum opsi opsi-agent opsi-cloud opsi-bootstrap-worker > checksums.txt

smoke-release:
	$(PROXY) ./release/opsi version
	$(PROXY) ./release/opsi-agent --version
	$(PROXY) ./release/opsi-cloud --version
