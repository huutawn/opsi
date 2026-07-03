VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
GOCACHE ?= /tmp/opsi-go-cache

.PHONY: test build ui-build lint e2e-dry-run release smoke-release

test:
	cd contracts/go && rtk env GOCACHE=$(GOCACHE) go test ./...
	cd agent && rtk env GOCACHE=$(GOCACHE) go test ./...
	cd cli && rtk env GOCACHE=$(GOCACHE) go test ./...
	cd cloud && rtk env GOCACHE=$(GOCACHE) go test ./...

build:
	rtk mkdir -p bin
	cd agent && rtk env GOCACHE=$(GOCACHE) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-agent ./cmd/opsi-agent
	cd cli && rtk env GOCACHE=$(GOCACHE) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi ./cmd/opsi
	cd cloud && rtk env GOCACHE=$(GOCACHE) go build -ldflags "$(LDFLAGS)" -o ../bin/opsi-cloud ./cmd/opsi-cloud

ui-build:
	cd cli/ui && rtk npm run build

lint:
	cd agent && rtk env GOCACHE=$(GOCACHE) go vet ./...
	cd cli && rtk env GOCACHE=$(GOCACHE) go vet ./...
	cd cloud && rtk env GOCACHE=$(GOCACHE) go vet ./...
	cd contracts/go && rtk env GOCACHE=$(GOCACHE) go vet ./...

e2e-dry-run:
	cd agent && rtk env GOCACHE=$(GOCACHE) go test ./internal/cloudrunner

release: build
	rtk mkdir -p release/config.examples release/docs
	rtk cp bin/opsi release/opsi
	rtk cp bin/opsi-agent release/opsi-agent
	rtk cp bin/opsi-cloud release/opsi-cloud
	rtk cp docs/demo_runbook.md release/docs/demo_runbook.md
	cd release && rtk sha256sum opsi opsi-agent opsi-cloud > checksums.txt

smoke-release:
	rtk proxy ./release/opsi version
	rtk proxy ./release/opsi-agent --version
	rtk proxy ./release/opsi-cloud --version
