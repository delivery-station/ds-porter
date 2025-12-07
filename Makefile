.PHONY: build test lint clean install run help release-prepare release-build-all release-checksums release-oci-manifest release-oci-push

# Build variables
BINARY_NAME=ds-porter
VERSION?=dev
COMMIT?=$(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE?=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

# OCI Registry variables
REGISTRY?=ghcr.io/delivery-station/porter
IMAGE_NAME=$(REGISTRY):$(VERSION)
IMAGE_LATEST=$(REGISTRY):latest

# Go commands
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt
GOVET=$(GOCMD) vet

# Build directories
BUILD_DIR=bin
CMD_DIR=cmd/porter

# Target platforms
PLATFORMS=linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

help: ## Display this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

build: ## Build the binary
	@echo "Building $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./$(CMD_DIR)
	@echo "Binary built: $(BUILD_DIR)/$(BINARY_NAME)"

test: ## Run unit tests
	@echo "Running unit tests..."
	$(GOTEST) -v -race -short -coverprofile=coverage.out ./...

test-integration: ## Run integration tests
	@echo "Running integration tests..."
	$(GOTEST) -v -race -tags=integration ./test/integration/...

test-all: ## Run all tests (unit + integration)
	@echo "Running all tests..."
	$(GOTEST) -v -race -tags=integration -coverprofile=coverage.out ./...

test-coverage: test ## Run tests with coverage report
	@echo "Generating coverage report..."
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint: ## Run linters
	@echo "Running go fmt..."
	$(GOFMT) ./...
	@echo "Running go vet..."
	$(GOVET) ./...
	@echo "Checking go mod tidy..."
	$(GOMOD) tidy
	@git diff --exit-code go.mod go.sum || (echo "go.mod or go.sum needs updating" && exit 1)

clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@echo "Clean complete"

install: build ## Install binary to $GOPATH/bin
	@echo "Installing $(BINARY_NAME)..."
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/$(BINARY_NAME)
	@echo "Installed to $(GOPATH)/bin/$(BINARY_NAME)"

run: build ## Build and run the binary
	@$(BUILD_DIR)/$(BINARY_NAME)

deps: ## Download dependencies
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

all: deps lint test build ## Run all checks and build

release-prepare: ## Prepare for release
	@echo "Preparing release $(VERSION)"
	@mkdir -p $(BUILD_DIR)

release-build-all: release-prepare ## Build multi-platform binaries for release
	@echo "Building release artifacts for $(VERSION)"
	# Iterate through PLATFORMS to build per-platform artifacts into dedicated directories
	@for platform in $(PLATFORMS); do \
		GOOS=$${platform%/*}; \
		GOARCH=$${platform#*/}; \
		OUTPUT_DIR="$(BUILD_DIR)/$$platform"; \
		mkdir -p "$$OUTPUT_DIR"; \
		OUTPUT_FILE="$$OUTPUT_DIR/$(BINARY_NAME)"; \
		if [ "$$GOOS" = "windows" ]; then \
			OUTPUT_FILE="$$OUTPUT_FILE.exe"; \
		fi; \
		echo "  > $$platform"; \
		GOOS=$$GOOS GOARCH=$$GOARCH $(GOBUILD) $(LDFLAGS) -o "$$OUTPUT_FILE" ./$(CMD_DIR); \
	done
	@echo "✓ All platform binaries built successfully"

release-checksums: ## Create checksums for release artifacts
	@echo "Creating checksums..."
	cd $(BUILD_DIR) && sha256sum $(BINARY_NAME)-$(VERSION)-* > checksums-$(VERSION).txt && cd ..
	@echo "✓ Checksums created: $(BUILD_DIR)/checksums-$(VERSION).txt"

.DEFAULT_GOAL := help
