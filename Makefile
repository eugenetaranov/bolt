.PHONY: build test test-integration test-docker test-docker-up test-docker-run test-docker-down lint clean run install release release-dry-run release-snapshot list

list: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | awk -F ':.*## ' '{printf "  %-24s %s\n", $$1, $$2}'

BINARY=tack
BUILD_DIR=bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

build: ## Build the binary
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/tack

build-all: build-linux build-darwin ## Build for all platforms

build-linux: ## Build for Linux (amd64, arm64)
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/tack
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-arm64 ./cmd/tack

build-darwin: ## Build for macOS (amd64, arm64)
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/tack
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/tack

test: ## Run unit tests
	go test -v -short ./...

test-coverage: ## Run tests with coverage report
	go test -short -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

test-integration: ## Run Go integration tests
	go test -v -timeout 20m ./tests/integration/...

test-docker: test-docker-up test-docker-run test-docker-down ## Run Docker SSH integration test (all-in-one)

test-docker-up: ## Start 3 SSH Docker containers
	docker compose -f tests/docker-compose.yaml up -d --build
	bash tests/setup-keys.sh

test-docker-run: build ## Run playbook against Docker containers
	./bin/tack run tests/docker-playbook.yaml \
		-c ssh://testuser@127.0.0.1:2201 \
		-c ssh://testuser@127.0.0.1:2202 \
		-c ssh://testuser@127.0.0.1:2203 \
		--ssh-key tests/.ssh/id_ed25519 \
		--ssh-insecure --auto-approve

test-docker-down: ## Tear down Docker containers and clean keys
	docker compose -f tests/docker-compose.yaml down -v
	rm -rf tests/.ssh

lint: ## Run linter
	golangci-lint run

clean: ## Remove build artifacts
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

run: ## Run tack directly via go run
	go run ./cmd/tack

install: build ## Build and install to /usr/local/bin
	cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/

deps: ## Install dependencies
	go mod tidy

validate-examples: ## Validate example playbooks
	go run ./cmd/tack validate examples/playbooks/*.yaml

example: ## Run example playbook (dry-run)
	go run ./cmd/tack run examples/playbooks/setup-dev.yaml --dry-run --debug

release: ## Create and push a release tag
	@if [ -z "$(TAG)" ]; then \
		LATEST=$$(git tag --sort=-version:refname 2>/dev/null | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$$' | head -1); \
		if [ -z "$$LATEST" ]; then \
			SUGGESTED="v0.1.0"; \
		else \
			MAJOR=$$(echo $$LATEST | sed 's/v\([0-9]*\)\.\([0-9]*\)\.\([0-9]*\)/\1/'); \
			MINOR=$$(echo $$LATEST | sed 's/v\([0-9]*\)\.\([0-9]*\)\.\([0-9]*\)/\2/'); \
			PATCH=$$(echo $$LATEST | sed 's/v\([0-9]*\)\.\([0-9]*\)\.\([0-9]*\)/\3/'); \
			PATCH=$$((PATCH + 1)); \
			SUGGESTED="v$$MAJOR.$$MINOR.$$PATCH"; \
		fi; \
		echo "Latest tag: $${LATEST:-none}"; \
		printf "Enter tag [$$SUGGESTED]: "; \
		read INPUT_TAG; \
		TAG=$${INPUT_TAG:-$$SUGGESTED}; \
		echo "Creating release $$TAG..."; \
		git tag -a $$TAG -m "Release $$TAG" && \
		git push origin $$TAG && \
		echo "Release $$TAG pushed. GitHub Actions will build and publish artifacts."; \
	else \
		echo "Creating release $(TAG)..."; \
		git tag -a $(TAG) -m "Release $(TAG)" && \
		git push origin $(TAG) && \
		echo "Release $(TAG) pushed. GitHub Actions will build and publish artifacts."; \
	fi

release-dry-run: ## Test release without publishing
	goreleaser release --snapshot --clean --skip=publish

release-snapshot: ## Create snapshot release
	goreleaser release --snapshot --clean

release-check: ## Check GoReleaser configuration
	goreleaser check
