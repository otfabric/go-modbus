# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.PHONY: help all build build-cmd build-examples lint vet test check clean
help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

BIN_DIR := bin # Output directory for generated binaries
# All packages except /examples (for lint/vet)
PKGS := $(shell go list ./... | grep -v '/examples$$' | sed 's,^github.com/otfabric/modbus,.,')
# Core library only: tests and coverage run on . only (exclude cmd and examples)
TEST_PKGS := .

all: build ## Default target: build cmd + examples apps

build: build-cmd build-examples ## Build all app entrypoints

build-cmd: ## Build binaries from cmd/*.go
	@echo "Building command line interface"
	@mkdir -p $(BIN_DIR)
	@for src in cmd/*.go; do \
		name="$$(basename "$$src" .go)"; \
		go build -o "$(BIN_DIR)/$$name" "$$src"; \
	done

build-examples: ## Build binaries from examples/*.go
	@echo "Building examples"
	@mkdir -p $(BIN_DIR)
	@for src in examples/**/*.go; do \
		name="$$(basename "$$src" .go)"; \
		go build -o "$(BIN_DIR)/example-$$name" "$$src"; \
	done

fmt: ## Format Go code with gofmt
	@echo "Running gofmt"
	@gofmt -w .

lint: ## Run staticcheck
	@echo "Running staticcheck"
	@staticcheck $(PKGS)

lint-ci: ## Run golangci-lint
	@echo "Running golangci-lint"
	@golangci-lint run $(PKGS)

vet: ## Run go vet on project packages
	@echo "Running go vet on packages: $(PKGS)"
	@go vet $(PKGS)

test: ## Run tests on core library only
	@echo "Running tests on packages: $(TEST_PKGS)"
	@go test $(TEST_PKGS)

coverage: ## Run tests with coverage on core library only (writes coverage.out)
	@echo "Running coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic $(TEST_PKGS)

cover: coverage ## Open coverage report in browser
	@echo "Opening coverage report"
	@go tool cover -html=coverage.out

check: fmt lint lint-ci vet test coverage ## Run lint + vet + test

clean: ## Remove generated binaries
	@echo "Cleaning up"
	@rm -rf $(BIN_DIR)
