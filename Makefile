SHELL := /bin/sh

GO ?= go
GOFMT ?= gofmt
PKGS ?= ./...
GOFILES := $(filter-out $(shell git ls-files --deleted -- '*.go'),$(shell git ls-files -- '*.go'))
GOVULNCHECK_VERSION ?= v1.6.0
COMPOSITION_EXAMPLE_DIR := examples/kit-series-composition

# Keep build cache inside the repo so local runs are reproducible and do not
# depend on a writable global cache path.
export GOCACHE ?= $(CURDIR)/.cache/go-build

.DEFAULT_GOAL := help

.PHONY: \
	help \
	build-examples \
	dependency-boundary \
	fmt \
	fmt-check \
	vet \
	test \
	test-race \
	coverage \
	tidy \
	tidy-check \
	govulncheck \
	verify \
	clean

help: ## Show available targets.
	@printf "Available targets:\n"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build-examples: ## Compile the runnable example programs.
	@echo "==> build examples"
	@$(GO) build ./examples/...
	@mkdir -p .bin
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) build -o $(CURDIR)/.bin/kit-series-composition .

dependency-boundary: ## Keep example-only dependencies out of the root package.
	@echo "==> checking dependency boundary"
	@imports="$$($(GO) list -deps -f '{{.ImportPath}}' .)"; \
	forbidden="$$(printf '%s\n' "$$imports" | grep -E '^(github\.com/jaredjakacky/(configkit|dependkit|workerkit)|go\.opentelemetry\.io/otel/(exporters|sdk))(/|$$)' || true)"; \
	if [ -n "$$forbidden" ]; then \
		echo "The Servekit root package must not import example-only packages:"; \
		echo "$$forbidden"; \
		exit 1; \
	fi
	@modules="$$($(GO) list -m -f '{{.Path}}' all)"; \
	forbidden="$$(printf '%s\n' "$$modules" | grep -E '^github\.com/jaredjakacky/(configkit|dependkit|workerkit)$$' || true)"; \
	if [ -n "$$forbidden" ]; then \
		echo "The Servekit module graph must not include sibling kits:"; \
		echo "$$forbidden"; \
		exit 1; \
	fi

fmt: ## Format tracked Go source files.
	@echo "==> formatting"
	@$(GOFMT) -w $(GOFILES)

fmt-check: ## Verify tracked Go source files are formatted.
	@echo "==> checking formatting"
	@out="$$($(GOFMT) -l $(GOFILES))"; \
	if [ -n "$$out" ]; then \
		echo "The following files are not formatted:"; \
		echo "$$out"; \
		exit 1; \
	fi

vet: ## Run go vet on all packages.
	@echo "==> vet"
	@$(GO) vet $(PKGS)
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) vet ./...

test: ## Run tests for all packages.
	@echo "==> test"
	@$(GO) test $(PKGS)
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) test ./...

test-race: ## Run tests with the race detector enabled.
	@echo "==> test (race)"
	@$(GO) test -race $(PKGS)
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) test -race ./...

coverage: ## Run tests with coverage output written to coverage.out.
	@echo "==> coverage"
	@$(GO) test -coverprofile=coverage.out $(PKGS)

tidy: ## Synchronize go.mod and go.sum with the source tree.
	@echo "==> tidy"
	@$(GO) mod tidy
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) mod tidy

tidy-check: ## Verify go.mod/go.sum are already tidy.
	@echo "==> checking tidy"
	@$(GO) mod tidy -diff
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) mod tidy -diff

govulncheck: ## Run the pinned govulncheck tool against the main module packages.
	@echo "==> govulncheck"
	@$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(PKGS)
	@$(GO) -C $(COMPOSITION_EXAMPLE_DIR) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

verify: fmt-check dependency-boundary vet test build-examples tidy-check ## Run the local verification suite.
	@echo "==> verification passed"

clean: ## Remove local build outputs and caches.
	@echo "==> clean"
	@rm -rf .cache coverage.out .bin
