# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.PHONY: help all build build-cmd build-examples fmt fmt-check lint lint-ci vet vuln test coverage cover fuzz bench soak stress-load check clean interop test-interop coverage-check cross e2e coverage-pkg-check docs-check
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

# docsync release of docs-check (DOC-008).
DOCSYNC_VERSION := v0.4.0

# Reference implementations for 'make interop': otfabric/modbus-interop v0.4.2
# (libmodbus v3.2.0, PyModbus 3.16.1, digitalpetri/modbus 2.1.6, NModbus 3.0.83,
# tokio-modbus 0.17.0, j2mod 3.4.0, jsmodbus 5.0.0, modbus-esp8266 4.1.1).
# modbus4j 3.1.0 is GPL and not published: build it in modbus-interop
# ("make image-modbus4j") and pass
#   make interop MODBUS_INTEROP_MODBUS4J_IMAGE=ghcr.io/otfabric/modbus-interop-modbus4j:dev
# The images are pinned by the multi-arch index digests of its release manifest.
# Keep in step with interop/harness.go and .github/workflows/interop.yml.
# Override to test other builds, e.g.
#   make interop MODBUS_INTEROP_PYMODBUS_IMAGE=ghcr.io/otfabric/modbus-interop-pymodbus:dev
# MODBUS_INTEROP_ADAPTERS=libmodbus,pymodbus restricts the run to a subset.
MODBUS_INTEROP_LIBMODBUS_IMAGE    ?= ghcr.io/otfabric/modbus-interop-libmodbus@sha256:da13c28adc0b00383d93d71b6cd70883d3673b12a171ffc260a7bb97aa75fd50
MODBUS_INTEROP_PYMODBUS_IMAGE     ?= ghcr.io/otfabric/modbus-interop-pymodbus@sha256:e780cc28e6aaba3f79117521f1f8dd9f29768d1d6dbcb39c6169dcf5062bac37
MODBUS_INTEROP_DIGITALPETRI_IMAGE ?= ghcr.io/otfabric/modbus-interop-digitalpetri@sha256:cc54df3a528f79cfe3bdb18f8e4d136dd829f566d1044610705d416e30f1c06b
MODBUS_INTEROP_NMODBUS_IMAGE      ?= ghcr.io/otfabric/modbus-interop-nmodbus@sha256:66af7261c1dff13a7e7f391049748e71cc0d46008a1004b7de12549e02c9760d
MODBUS_INTEROP_TOKIOMODBUS_IMAGE  ?= ghcr.io/otfabric/modbus-interop-tokiomodbus@sha256:34860cd1c7200e32a0111853564d640b62e4bfc89d7c833d39ab3f1e7aa3a3ce
MODBUS_INTEROP_J2MOD_IMAGE        ?= ghcr.io/otfabric/modbus-interop-j2mod@sha256:14cbe8953bc66fc1da77cc7673da00b9c2b99fef446558ee77914e5540394445
MODBUS_INTEROP_JSMODBUS_IMAGE     ?= ghcr.io/otfabric/modbus-interop-jsmodbus@sha256:0d02076d4a8a8707632f8e7ab23b144501ee4ff2d9d7d5592a2ca2a8da48332d
MODBUS_INTEROP_MODBUSESP_IMAGE    ?= ghcr.io/otfabric/modbus-interop-modbusesp@sha256:1f6b07b332e3108e0b56cc3d631bf96f89a8438b95b98c05445935182f2832a6
MODBUS_INTEROP_MODBUS4J_IMAGE     ?=


all: build ## Default target: build cmd + examples apps

build: build-cmd build-examples ## Build all app entrypoints

# Version and tag only when HEAD is exactly on a tag (version without the leading v);
# otherwise the binary reports dev. The full commit SHA, as the Release workflow injects.
VERSION  ?= $(patsubst v%,%,$(shell git describe --tags --exact-match 2>/dev/null))
TAG      ?= $(shell git describe --tags --exact-match 2>/dev/null)
# -dirty when the working tree has changes, as Go itself marks vcs.modified
# (git status --porcelain, untracked files included); empty outside git.
DIRTY := $(shell [ -z "$$(git status --porcelain 2>/dev/null)" ] || echo -dirty)
COMMIT   ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)$(DIRTY)
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
	@go test -run='^$$' -fuzz='^FuzzClientResponseDecode$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzServerFunctions$$' -fuzztime=$(FUZZTIME) .
	@go test -run='^$$' -fuzz='^FuzzCompile$$' -fuzztime=$(FUZZTIME) ./batch
	@go test -run='^$$' -fuzz='^FuzzHostileRuntimeDecode$$' -fuzztime=$(FUZZTIME) ./codec
	@go test -run='^$$' -fuzz='^FuzzHostileTCPResponse$$' -fuzztime=$(FUZZTIME) ./internal/transport
	@go test -run='^$$' -fuzz='^FuzzHostileRTUResponse$$' -fuzztime=$(FUZZTIME) ./internal/transport
	@go test -run='^$$' -fuzz='^FuzzHostileASCIIResponse$$' -fuzztime=$(FUZZTIME) ./internal/transport
	@go test -run='^$$' -fuzz='^FuzzHostileDiscover$$' -fuzztime=$(FUZZTIME) ./sunspec

# Duration the chaos and the stress tests each keep looping in soak mode. Override with
# SOAKTIME=... (e.g. SOAKTIME=30m). A failing chaos run logs its seed; replay it with
# MODBUS_CHAOS_SEED=<seed>.
SOAKTIME ?= 2m
soak: ## Run the chaos and stress tests under the race detector for SOAKTIME each (default 2m)
	@echo "Soaking chaos and stress tests (SOAKTIME=$(SOAKTIME))"
	@MODBUS_CHAOS_DURATION=$(SOAKTIME) MODBUS_STRESS_DURATION=$(SOAKTIME) \
		go test -race -count=1 -timeout=0 -run 'Chaos|Stress' .

stress-load: ## Repeat the chaos regression tests under CPU contention with the race detector (about 5 to 15 minutes)
	@scripts/stress-load.sh bin/stress-load

test-interop: ## Run the interop suite against the reference images of modbus-interop (needs Docker)
	@echo "Running interop tests against $(MODBUS_INTEROP_LIBMODBUS_IMAGE), $(MODBUS_INTEROP_PYMODBUS_IMAGE), $(MODBUS_INTEROP_DIGITALPETRI_IMAGE), $(MODBUS_INTEROP_NMODBUS_IMAGE), $(MODBUS_INTEROP_TOKIOMODBUS_IMAGE), $(MODBUS_INTEROP_J2MOD_IMAGE), $(MODBUS_INTEROP_JSMODBUS_IMAGE), $(MODBUS_INTEROP_MODBUSESP_IMAGE)$(if $(MODBUS_INTEROP_MODBUS4J_IMAGE), and $(MODBUS_INTEROP_MODBUS4J_IMAGE))"
	@MODBUS_INTEROP_LIBMODBUS_IMAGE="$(MODBUS_INTEROP_LIBMODBUS_IMAGE)" \
		MODBUS_INTEROP_PYMODBUS_IMAGE="$(MODBUS_INTEROP_PYMODBUS_IMAGE)" \
		MODBUS_INTEROP_DIGITALPETRI_IMAGE="$(MODBUS_INTEROP_DIGITALPETRI_IMAGE)" \
		MODBUS_INTEROP_NMODBUS_IMAGE="$(MODBUS_INTEROP_NMODBUS_IMAGE)" \
		MODBUS_INTEROP_TOKIOMODBUS_IMAGE="$(MODBUS_INTEROP_TOKIOMODBUS_IMAGE)" \
		MODBUS_INTEROP_J2MOD_IMAGE="$(MODBUS_INTEROP_J2MOD_IMAGE)" \
		MODBUS_INTEROP_JSMODBUS_IMAGE="$(MODBUS_INTEROP_JSMODBUS_IMAGE)" \
		MODBUS_INTEROP_MODBUSESP_IMAGE="$(MODBUS_INTEROP_MODBUSESP_IMAGE)" \
		MODBUS_INTEROP_MODBUS4J_IMAGE="$(MODBUS_INTEROP_MODBUS4J_IMAGE)" \
		go test -tags=$(INTEROP_TAGS) -count=1 -timeout=45m ./interop/...

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

check: fmt fmt-check lint lint-ci vet vuln test coverage-pkg-check docs-check ## Run format + lint + vet + vuln + test + docs-check

# The documents against the code (DOC-008): docsync, as docs-check.yml runs it.
docs-check: ## Check that the documents agree with the code (docsync), library and cmd/modbus-cli
	@go run github.com/otfabric/docsync@$(DOCSYNC_VERSION) check .
	@cd cmd/modbus-cli && go run github.com/otfabric/docsync@$(DOCSYNC_VERSION) check .

clean: ## Remove generated binaries
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR)
