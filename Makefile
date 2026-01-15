.PHONY: build test test-integration lint clean run install release release-dry-run release-snapshot

BINARY=bolt
BUILD_DIR=bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

LDFLAGS=-ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

build:
	@mkdir -p $(BUILD_DIR)
	go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY) ./cmd/bolt

build-all: build-linux build-darwin

build-linux:
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-amd64 ./cmd/bolt
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-linux-arm64 ./cmd/bolt

build-darwin:
	@mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-amd64 ./cmd/bolt
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY)-darwin-arm64 ./cmd/bolt

test:
	go test -v -short ./...

test-coverage:
	go test -short -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

test-integration:
	go test -v -timeout 5m ./tests/integration/...

lint:
	golangci-lint run

clean:
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

run:
	go run ./cmd/bolt

install: build
	cp $(BUILD_DIR)/$(BINARY) /usr/local/bin/

# Install dependencies
deps:
	go mod tidy

# Validate example playbooks
validate-examples:
	go run ./cmd/bolt validate examples/playbooks/*.yaml

# Run example playbook (dry-run)
example:
	go run ./cmd/bolt run examples/playbooks/setup-dev.yaml --dry-run --debug

# Create and push a release tag
# Usage: make release TAG=v1.0.0
release:
	@if [ -z "$(TAG)" ]; then \
		echo "Recent tags:"; \
		git tag --sort=-version:refname | head -3 || echo "  (no tags)"; \
		echo ""; \
		echo "Error: TAG is required. Usage: make release TAG=v1.0.0"; \
		exit 1; \
	fi
	@echo "Creating release $(TAG)..."
	git tag -a $(TAG) -m "Release $(TAG)"
	git push origin $(TAG)
	@echo "Release $(TAG) pushed. GitHub Actions will build and publish artifacts."

# GoReleaser: test release configuration without publishing
release-dry-run:
	goreleaser release --snapshot --clean --skip=publish

# GoReleaser: create snapshot release (for testing)
release-snapshot:
	goreleaser release --snapshot --clean

# GoReleaser: check configuration
release-check:
	goreleaser check
