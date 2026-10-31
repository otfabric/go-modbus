# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.PHONY: help all build build-cmd build-examples fmt fmt-check lint lint-ci vet vuln test coverage cover fuzz bench soak check clean interop test-interop coverage-check cross e2e coverage-pkg-check
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
# The CLI is a nested module (ADR-0009): ./... of this module does not reach it.
CLI_DIR := cmd/modbus-cli
# Build tag of the interop package (excluded from default ./... builds).
INTEROP_TAGS := interop
# All packages except /examples (for lint/vet), ./interop included
PKGS := $(shell go list -tags=$(INTEROP_TAGS) ./... | grep -v '/examples$$' | sed 's,^github.com/otfabric/go-modbus,.,')
# Core library + subpackages: tests and coverage (exclude examples and the interop suite)
TEST_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/interop' | sed 's,^github.com/otfabric/go-modbus,.,')

# Reference implementations for 'make interop': otfabric/modbus-interop v0.2.0
# (libmodbus v3.2.0, PyModbus 3.16.1, digitalpetri/modbus 2.1.6, NModbus 3.0.83,
# tokio-modbus 0.17.0).
# The images are pinned by the multi-arch index digests of its release manifest.
# Keep in step with interop/harness.go and .github/workflows/interop.yml.
# Override to test other builds, e.g.
#   make interop MODBUS_INTEROP_PYMODBUS_IMAGE=ghcr.io/otfabric/modbus-interop-pymodbus:dev
# MODBUS_INTEROP_ADAPTERS=libmodbus,pymodbus restricts the run to a subset.
MODBUS_INTEROP_LIBMODBUS_IMAGE    ?= ghcr.io/otfabric/modbus-interop-libmodbus@sha256:b8b2d7e71908c225995037596e2d55b4700194b63a533ff57b8af074fb2dbf62
MODBUS_INTEROP_PYMODBUS_IMAGE     ?= ghcr.io/otfabric/modbus-interop-pymodbus@sha256:33b703491abc2f15c229ff433ffbb79eb00e86a8c56c30fb7799c8ed13832f5a
MODBUS_INTEROP_DIGITALPETRI_IMAGE ?= ghcr.io/otfabric/modbus-interop-digitalpetri@sha256:c9746131c1ed55c3beb8e507925aa01e466278fd2074a69926e6f4797959bf59
MODBUS_INTEROP_NMODBUS_IMAGE      ?= ghcr.io/otfabric/modbus-interop-nmodbus@sha256:761f5ce9f0175125ad4262d98e42460fd5235717f44bd8ab7973239f9ed86f80
MODBUS_INTEROP_TOKIOMODBUS_IMAGE  ?= ghcr.io/otfabric/modbus-interop-tokiomodbus@sha256:b317fc4c8e3c834a40fb1316582d5663885e4688aa17ba7eb69dd8a61495a2c8


all: build ## Default target: build cmd + examples apps

build: build-cmd build-examples ## Build all app entrypoints

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TAG      ?= $(shell git describe --tags --exact-match 2>/dev/null || echo none)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS  := -s -w -X main.version=$(VERSION) -X main.tag=$(TAG) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

build-cmd: ## Build the CLI binary from the nested module in cmd/modbus-cli/
	@echo "Building command line interface ($(VERSION))"
	@mkdir -p $(BIN_DIR)
	@cd $(CLI_DIR) && go build -ldflags "$(LDFLAGS)" -o "$(CURDIR)/$(BIN_DIR)/modbus-cli" .

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
	@cd $(CLI_DIR) && staticcheck ./...

lint-ci: ## Run golangci-lint (build-tags include interop; see .golangci.yml)
	@echo "Running golangci-lint"
	@golangci-lint run $(PKGS)
	@cd $(CLI_DIR) && golangci-lint run ./...

vet: ## Run go vet on project packages (includes -tags=interop compile of ./interop)
	@echo "Running go vet on packages: $(PKGS)"
	@go vet -tags=$(INTEROP_TAGS) $(PKGS)
	@cd $(CLI_DIR) && go vet ./...

vuln: ## Run govulncheck
	@echo "Running govulncheck"
	@govulncheck ./...
	@cd $(CLI_DIR) && govulncheck ./...

test: ## Run tests on core library only
	@echo "Running tests on packages: $(TEST_PKGS)"
	@go test $(TEST_PKGS)

coverage: ## Run tests with coverage on core library only (writes coverage.out)
	@echo "Running coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic $(TEST_PKGS)

# Duration each benchmark runs. Override with BENCHTIME=... (e.g. BENCHTIME=2s).
BENCHTIME ?= 1s
bench: ## Run the read-strategy benchmarks (requests/op, pdu-bytes/op, time, allocations)
	@echo "Benchmarking (BENCHTIME=$(BENCHTIME))"
	@go test -run='^$$' -bench=. -benchmem -benchtime=$(BENCHTIME) $(TEST_PKGS)

# Duration each fuzz target runs. Override with FUZZTIME=... (e.g. FUZZTIME=5m).
# Same minimum as coverage-min in ci.yml.
COVER_MIN := 95
# Per-package floor (TEST-012): what the weakest package measures now, rounded down.
COVER_PKG_MIN := 90
FUZZTIME ?= 30s
fuzz: ## Run back-to-back fuzz targets for FUZZTIME each (default 30s)
	@echo "Fuzzing (FUZZTIME=$(FUZZTIME))"
	@go test -run='^$$' -fuzz='^FuzzServerRequest$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzClientResponseParse$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzServerFunctions$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzCompile$$' -fuzztime=$(FUZZTIME) ./batch

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

coverage-check: coverage ## Fail when total coverage is below COVER_MIN
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { if (t+0 < m+0) { printf "coverage %.1f%% < %s%%\n", t, m; exit 1 } else printf "coverage %.1f%% >= %s%%\n", t, m }'

CROSS_PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/arm64 windows/amd64

cross: ## Cross-compile without cgo for the supported platforms
	@for p in $(CROSS_PLATFORMS); do \
		echo "Building for $$p"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build ./... || exit 1; \
		(cd $(CLI_DIR) && CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build -o /dev/null ./...) || exit 1; \
	done

e2e: ## End-to-end suite: public client against this module's server over loopback (TestE2E_*)
	@go test -count=1 -race -run '^TestE2E_' .

coverage-pkg-check: coverage ## Fail when a package is below COVER_PKG_MIN
	@awk -v m="$(COVER_PKG_MIN)" 'NR > 1 { n[$$1] = $$2; if ($$3 > 0) h[$$1] = 1 } \
	END { for (k in n) { p = k; sub(/\/[^\/]*$$/, "", p); s[p] += n[k]; if (k in h) c[p] += n[k] } \
	for (p in s) { v = 100 * c[p] / s[p]; if (v < m) { printf "%s: %.1f%% < %s%%\n", p, v, m; bad = 1 } } exit bad }' coverage.out

check: fmt fmt-check lint lint-ci vet vuln test coverage-pkg-check ## Run format + lint + vet + vuln + test

clean: ## Remove generated binaries
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR)
