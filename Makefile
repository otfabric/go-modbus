# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.PHONY: help all build build-cmd build-examples fmt fmt-check lint lint-ci vet vuln test coverage cover fuzz soak check clean interop test-interop
help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Parent otfabric/go.work does not list this module; isolate toolchain commands.
export GOWORK := off

# CI formats and lints with the Go version from go.mod, not with the toolchain installed here.
# gofmt's output changes between Go releases (comment alignment, for one), so formatting is
# done with that version, and `fmt-check` also requires the local gofmt to agree: a file that
# two versions format differently fails CI on one side or the other.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

# Output directory for generated binaries
BIN_DIR := bin
# Build tag of the interop package (excluded from default ./... builds).
INTEROP_TAGS := interop
# All packages except /examples (for lint/vet), ./interop included
PKGS := $(shell go list -tags=$(INTEROP_TAGS) ./... | grep -v '/examples$$' | sed 's,^github.com/otfabric/go-modbus,.,')
# Core library + subpackages: tests and coverage (exclude cmd, examples and the interop suite)
TEST_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/cmd' | grep -v '/interop' | sed 's,^github.com/otfabric/go-modbus,.,')

# Reference implementations for 'make interop': otfabric/modbus-interop v0.1.0
# (libmodbus v3.2.0, PyModbus 3.15.0, digitalpetri/modbus 2.1.6, NModbus 3.0.83,
# tokio-modbus 0.17.0).
# The images are pinned by the multi-arch index digests of its release manifest.
# Keep in step with interop/harness.go and .github/workflows/interop.yml.
# Override to test other builds, e.g.
#   make interop MODBUS_INTEROP_PYMODBUS_IMAGE=ghcr.io/otfabric/modbus-interop-pymodbus:dev
# MODBUS_INTEROP_ADAPTERS=libmodbus,pymodbus restricts the run to a subset.
MODBUS_INTEROP_LIBMODBUS_IMAGE    ?= ghcr.io/otfabric/modbus-interop-libmodbus@sha256:d51da912841f7afc78095fb043193601e13c463b847384d5316d1ed37d7d0b6d
MODBUS_INTEROP_PYMODBUS_IMAGE     ?= ghcr.io/otfabric/modbus-interop-pymodbus@sha256:bae6b6cffd82108ac017e157d8a21d36ef2cbec960d938990eb07a52e765a738
MODBUS_INTEROP_DIGITALPETRI_IMAGE ?= ghcr.io/otfabric/modbus-interop-digitalpetri@sha256:7d78a87dc88cdca4b09b639aa4ed25c604620984233b157df8ab57fc98c79da6
MODBUS_INTEROP_NMODBUS_IMAGE      ?= ghcr.io/otfabric/modbus-interop-nmodbus@sha256:6da5066a5274ece1788d545ee7ca9153e8366767a5d6599f916ca69b16e408fb
MODBUS_INTEROP_TOKIOMODBUS_IMAGE  ?= ghcr.io/otfabric/modbus-interop-tokiomodbus@sha256:23f93e71653ef8fdc62de05826a4f015de5141770402b3c398fabd129b7122e7


all: build ## Default target: build cmd + examples apps

build: build-cmd build-examples ## Build all app entrypoints

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TAG      ?= $(shell git describe --tags --exact-match 2>/dev/null || echo none)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.tag=$(TAG) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

build-cmd: ## Build CLI binary from cmd/modbus-cli/ package
	@echo "Building command line interface ($(VERSION))"
	@mkdir -p $(BIN_DIR)
	@go build -ldflags "$(LDFLAGS)" -o "$(BIN_DIR)/modbus-cli" ./cmd/modbus-cli/

build-examples: ## Build binaries from examples/*.go
	@echo "Building examples"
	@mkdir -p $(BIN_DIR)
	@for src in examples/**/*.go; do \
		name="$$(basename "$$src" .go)"; \
		go build -o "$(BIN_DIR)/example-$$name" "$$src"; \
	done

fmt: ## Format Go code with the gofmt of the Go version in go.mod (what CI uses)
	@echo "Running gofmt (go$(GO_MOD_VERSION))"
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if the go.mod-version gofmt or the local gofmt would change any file
	@echo "Checking formatting (gofmt go$(GO_MOD_VERSION) and local $$(go env GOVERSION))"
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then \
		[ -z "$$ci" ] || { echo "Not formatted for gofmt go$(GO_MOD_VERSION) (CI):"; echo "$$ci"; }; \
		[ -z "$$loc" ] || { echo "Not formatted for the local gofmt:"; echo "$$loc"; }; \
		echo "Files must be stable under both. Run 'make fmt'; if the two versions still disagree,"; \
		echo "restructure the code they format differently (e.g. move trailing comments onto their own lines)."; \
		exit 1; \
	fi

lint: ## Run staticcheck (includes -tags=interop for ./interop)
	@echo "Running staticcheck"
	@staticcheck -tags=$(INTEROP_TAGS) $(PKGS)

lint-ci: ## Run golangci-lint (build-tags include interop; see .golangci.yml)
	@echo "Running golangci-lint"
	@golangci-lint run $(PKGS)

vet: ## Run go vet on project packages (includes -tags=interop compile of ./interop)
	@echo "Running go vet on packages: $(PKGS)"
	@go vet -tags=$(INTEROP_TAGS) $(PKGS)

vuln: ## Run govulncheck
	@echo "Running govulncheck"
	@govulncheck ./...

test: ## Run tests on core library only
	@echo "Running tests on packages: $(TEST_PKGS)"
	@go test $(TEST_PKGS)

coverage: ## Run tests with coverage on core library only (writes coverage.out)
	@echo "Running coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic $(TEST_PKGS)

# Duration each fuzz target runs. Override with FUZZTIME=... (e.g. FUZZTIME=5m).
FUZZTIME ?= 30s
fuzz: ## Run back-to-back fuzz targets for FUZZTIME each (default 30s)
	@echo "Fuzzing (FUZZTIME=$(FUZZTIME))"
	@go test -run='^$$' -fuzz='^FuzzServerRequest$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzClientResponseParse$$' -fuzztime=$(FUZZTIME) .

# Duration the chaos and the stress tests each keep looping in soak mode. Override with
# SOAKTIME=... (e.g. SOAKTIME=30m). A failing chaos run logs its seed; replay it with
# MODBUS_CHAOS_SEED=<seed>.
SOAKTIME ?= 2m
soak: ## Run the chaos and stress tests under the race detector for SOAKTIME each (default 2m)
	@echo "Soaking chaos and stress tests (SOAKTIME=$(SOAKTIME))"
	@MODBUS_CHAOS_DURATION=$(SOAKTIME) MODBUS_STRESS_DURATION=$(SOAKTIME) \
		go test -race -count=1 -timeout=0 -run 'Chaos|Stress' .

test-interop: ## Run the interop suite against the libmodbus, PyModbus, digitalpetri, NModbus and tokio-modbus reference images (needs Docker)
	@echo "Running interop tests against $(MODBUS_INTEROP_LIBMODBUS_IMAGE), $(MODBUS_INTEROP_PYMODBUS_IMAGE), $(MODBUS_INTEROP_DIGITALPETRI_IMAGE), $(MODBUS_INTEROP_NMODBUS_IMAGE) and $(MODBUS_INTEROP_TOKIOMODBUS_IMAGE)"
	@MODBUS_INTEROP_LIBMODBUS_IMAGE="$(MODBUS_INTEROP_LIBMODBUS_IMAGE)" \
		MODBUS_INTEROP_PYMODBUS_IMAGE="$(MODBUS_INTEROP_PYMODBUS_IMAGE)" \
		MODBUS_INTEROP_DIGITALPETRI_IMAGE="$(MODBUS_INTEROP_DIGITALPETRI_IMAGE)" \
		MODBUS_INTEROP_NMODBUS_IMAGE="$(MODBUS_INTEROP_NMODBUS_IMAGE)" \
		MODBUS_INTEROP_TOKIOMODBUS_IMAGE="$(MODBUS_INTEROP_TOKIOMODBUS_IMAGE)" \
		go test -tags=$(INTEROP_TAGS) -count=1 -timeout=30m ./interop/...

interop: test-interop ## Alias for test-interop

cover: coverage ## Open coverage report in browser
	@echo "Opening coverage report"
	@go tool cover -html=coverage.out

check: fmt fmt-check lint lint-ci vet vuln test coverage ## Run format + lint + vet + vuln + test

clean: ## Remove generated binaries
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR)
