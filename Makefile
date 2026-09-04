# The workflows in .github/workflows/ call these same targets, so there is one
# definition of what "lint" means rather than two. CLAUDE.md's command table mirrors
# this file; if they drift, CI and local runs stop agreeing.

# Tool versions are pinned here rather than floating. `go run tool@version` keeps them
# out of go.mod (they are not build dependencies) while staying explicit and
# reproducible -- the same reasoning as pinning an action to a commit SHA.
# staticcheck's own dependency graph may need a newer Go than go.mod's directive;
# `go run` fetches that toolchain transparently for this one subprocess
# (GOTOOLCHAIN=auto). That's deterministic per pinned version and does not affect
# build/test, which run on the toolchain go.mod actually declares.
STATICCHECK_VERSION ?= v0.8.1
GOVULNCHECK_VERSION ?= v1.7.0

DATABASE_URL ?= postgres://cadenceos:cadenceos@localhost:5432/cadenceos_dev?sslmode=disable
export DATABASE_URL

.PHONY: help deps lint fmt test build run security migrate materialize sweep notify nightly db-up db-down clean

help: ## Show available targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

deps: ## Download module dependencies and verify them
	go mod download
	go mod verify

fmt: ## Format the code
	gofmt -w .

lint: ## gofmt, go vet, staticcheck
	@echo "==> gofmt"
	@unformatted="$$(gofmt -l . 2>/dev/null)"; \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt-clean:"; echo "$$unformatted"; echo "Run: make fmt"; exit 1; \
	fi
	@echo "==> go vet"
	go vet ./...
	@echo "==> staticcheck"
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

test: ## Run the test suite (requires a running database)
	go test -race -shuffle=on ./...

build: ## Build all binaries
	go build ./...

run: ## Run the service locally on :8080
	go run ./cmd/cadenceos

security: ## Dependency vulnerability audit
	@echo "==> govulncheck"
	@# Needs network access to vuln.go.dev. GitHub-hosted runners reach it; some
	@# sandboxed environments deny it at the proxy, where this target cannot run.
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

migrate: ## Apply pending database migrations
	go run ./cmd/migrate

materialize: ## Top up the planned-occurrence horizon for every active schedule
	go run ./cmd/materialize

sweep: ## End timed pauses that have come due
	go run ./cmd/sweep

notify: ## Send outstanding Phase 4 transactional emails
	go run ./cmd/notify

nightly: ## Run the nightly passes (sweep, then materialize) as the scheduler does
	./scripts/nightly.sh

db-up: ## Start the local Postgres 16 container
	docker compose up -d db
	@echo "waiting for postgres..."
	@until docker compose exec -T db pg_isready -U cadenceos >/dev/null 2>&1; do sleep 1; done
	@echo "ready"

db-down: ## Stop the local database
	docker compose down

clean: ## Remove build output
	rm -rf bin/ coverage.out coverage.html
	go clean -testcache
