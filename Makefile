# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.PHONY: help all build build-cmd build-examples fmt fmt-check lint lint-ci vet vuln test coverage cover fuzz check clean
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
# All packages except /examples (for lint/vet)
PKGS := $(shell go list ./... | grep -v '/examples$$' | sed 's,^github.com/otfabric/go-modbus,.,')
# Core library + subpackages: tests and coverage (exclude cmd and examples)
TEST_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/cmd' | sed 's,^github.com/otfabric/go-modbus,.,')


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

lint: ## Run staticcheck
	@echo "Running staticcheck"
	@staticcheck $(PKGS)

lint-ci: ## Run golangci-lint
	@echo "Running golangci-lint"
	@golangci-lint run $(PKGS)

vet: ## Run go vet on project packages
	@echo "Running go vet on packages: $(PKGS)"
	@go vet $(PKGS)

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

cover: coverage ## Open coverage report in browser
	@echo "Opening coverage report"
	@go tool cover -html=coverage.out

check: fmt fmt-check lint lint-ci vet vuln test coverage ## Run format + lint + vet + vuln + test

clean: ## Remove generated binaries
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR)
