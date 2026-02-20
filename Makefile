# Cortex - Autonomous Agent Orchestrator
# Makefile for development, build, and operations

SHELL := /usr/bin/env bash

# Build settings
BINARY_NAME := cortex
BUILD_DIR := build
DIST_DIR := $(BUILD_DIR)/dist
VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# Go settings
GO := go
GOFLAGS := -trimpath
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)"

# Race test settings
RACE_PACKAGES := \
	./internal/scheduler/... \
	./internal/store/... \
	./internal/learner/... \
	./internal/dispatch/... \
	./internal/chief/...
RACE_TIMEOUT ?= 10m
RACE_LOCK_WAIT ?= 120
RACE_JSON_OUT ?=
RACE_CI_TIMEOUT ?= 25m
RACE_CI_LOCK_WAIT ?= 60
RACE_CI_JSON_OUT := $(BUILD_DIR)/test-race.jsonl
RACE_CI_LOG_OUT := $(BUILD_DIR)/test-race.log

# BD lock cleanup settings
BD_LOCK_CLEANUP_AGE_MINUTES ?= 5
BD_LOCK_CLEANUP_FORCE ?= 0
BD_LOCK_CLEANUP_REQUIRE_FORCE ?= 0
BD_LOCK_CLEANUP_REPORT_TO_MATRIX ?= 0
BD_LOCK_CLEANUP_MATRIX_ROOM ?=
BD_LOCK_CLEANUP_MATRIX_ACCOUNT ?= duc

# Scripts
SCRIPT_DIR := scripts
DEV_SCRIPTS := $(SCRIPT_DIR)/dev
RELEASE_SCRIPTS := $(SCRIPT_DIR)/release
OPS_SCRIPTS := $(SCRIPT_DIR)/ops

# Source files
SRC_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*' -not -path './.beads/*')

.PHONY: all help build build-all install clean test test-race test-race-ci lint fmt vet
.PHONY: lint-beads cleanup-bd-locks cleanup-bd-locks-escalation
.PHONY: service-install service-start service-stop service-logs
.PHONY: release snapshot docker

.DEFAULT_GOAL := help

##@ Development

help: ## Display this help message
	@echo "Cortex $(VERSION) - Available targets:"
	@echo ""
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: $(SRC_FILES) ## Build cortex binary
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) ./cmd/cortex/

build-all: ## Build all binaries (cortex + tools)
	$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_NAME) ./cmd/cortex/
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/db-backup ./cmd/db-backup/
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/db-restore ./cmd/db-restore/

install: build ## Build and install cortex to ~/.local/bin
	mkdir -p ~/.local/bin
	cp $(BINARY_NAME) ~/.local/bin/

clean: ## Remove build artifacts
	rm -f $(BINARY_NAME)
	rm -rf $(BUILD_DIR)/
	rm -f *.test

test: ## Run all tests
	$(GO) test ./...

test-verbose: ## Run all tests with verbose output
	$(GO) test -v ./...

##@ Code Quality

lint: fmt vet ## Run all linters

fmt: ## Format Go code
	$(GO) fmt ./...

vet: ## Run go vet
	$(GO) vet ./...

lint-beads: ## Validate open/in-progress beads have acceptance criteria + DoD gates
	$(DEV_SCRIPTS)/lint-beads.sh

lint-docs: ## Check markdown docs for broken internal references
	@bash $(DEV_SCRIPTS)/docs-lint.sh

##@ Testing

test-race: ## Run race tests for concurrency-critical packages
	TEST_SAFE_GO_TEST_TIMEOUT=$(RACE_TIMEOUT) \
	TEST_SAFE_BD_LOCK_CLEANUP_MINUTES=$(BD_LOCK_CLEANUP_AGE_MINUTES) \
	TEST_SAFE_BD_LOCK_CLEANUP_REQUIRE_FORCE=$(BD_LOCK_CLEANUP_REQUIRE_FORCE) \
	TEST_SAFE_LOCK_WAIT_SEC=$(RACE_LOCK_WAIT) \
	TEST_SAFE_JSON_OUT="$(RACE_JSON_OUT)" \
	$(DEV_SCRIPTS)/test-safe.sh -race $(RACE_PACKAGES)

test-race-ci: ## CI race entrypoint with timeout/log output
	@mkdir -p $(BUILD_DIR)
	@set -o pipefail; \
	TEST_SAFE_GO_TEST_TIMEOUT=$(RACE_CI_TIMEOUT) \
	TEST_SAFE_BD_LOCK_CLEANUP_MINUTES=$(BD_LOCK_CLEANUP_AGE_MINUTES) \
	TEST_SAFE_BD_LOCK_CLEANUP_REQUIRE_FORCE=$(BD_LOCK_CLEANUP_REQUIRE_FORCE) \
	TEST_SAFE_LOCK_WAIT_SEC=$(RACE_CI_LOCK_WAIT) \
	TEST_SAFE_JSON_OUT="$(RACE_CI_JSON_OUT)" \
	$(DEV_SCRIPTS)/test-safe.sh -race $(RACE_PACKAGES) 2>&1 | tee "$(RACE_CI_LOG_OUT)"

##@ Operations

cleanup-bd-locks: ## Remove stale bd lock files under .beads
	BD_LOCK_CLEANUP_FORCE="$(BD_LOCK_CLEANUP_FORCE)" \
	BD_LOCK_CLEANUP_REQUIRE_FORCE="$(BD_LOCK_CLEANUP_REQUIRE_FORCE)" \
	BD_LOCK_CLEANUP_REPORT_TO_MATRIX="$(BD_LOCK_CLEANUP_REPORT_TO_MATRIX)" \
	BD_LOCK_CLEANUP_MATRIX_ROOM="$(BD_LOCK_CLEANUP_MATRIX_ROOM)" \
	BD_LOCK_CLEANUP_MATRIX_ACCOUNT="$(BD_LOCK_CLEANUP_MATRIX_ACCOUNT)" \
	$(DEV_SCRIPTS)/cleanup-bd-locks.sh "$(BD_LOCK_CLEANUP_AGE_MINUTES)"

cleanup-bd-locks-escalation: ## Require explicit force before removing stale bd locks
	BD_LOCK_CLEANUP_REQUIRE_FORCE=1 \
	BD_LOCK_CLEANUP_FORCE="$(BD_LOCK_CLEANUP_FORCE)" \
	BD_LOCK_CLEANUP_REPORT_TO_MATRIX="$(BD_LOCK_CLEANUP_REPORT_TO_MATRIX)" \
	BD_LOCK_CLEANUP_MATRIX_ROOM="$(BD_LOCK_CLEANUP_MATRIX_ROOM)" \
	BD_LOCK_CLEANUP_MATRIX_ACCOUNT="$(BD_LOCK_CLEANUP_MATRIX_ACCOUNT)" \
	$(DEV_SCRIPTS)/cleanup-bd-locks.sh "$(BD_LOCK_CLEANUP_AGE_MINUTES)"

##@ Systemd Service

service-install: ## Install user systemd service
	mkdir -p ~/.config/systemd/user/
	cp deploy/systemd/cortex.service ~/.config/systemd/user/
	systemctl --user daemon-reload

service-start: ## Start and enable user systemd service
	systemctl --user enable --now cortex.service

service-stop: ## Stop and disable user systemd service
	systemctl --user stop cortex.service
	systemctl --user disable cortex.service

service-logs: ## View systemd service logs
	journalctl --user -u cortex.service -f

##@ Release

release: ## Create a new release (requires VERSION)
ifndef VERSION
	$(error VERSION is required. Usage: make release VERSION=x.y.z)
endif
	$(RELEASE_SCRIPTS)/bump-version.sh $(VERSION)
	$(RELEASE_SCRIPTS)/create-release-tag.sh $(VERSION)
	$(RELEASE_SCRIPTS)/generate-changelog.sh

snapshot: ## Create a snapshot release (current commit)
	$(RELEASE_SCRIPTS)/generate-changelog.sh --snapshot

dry-run: ## Dry run release process
	$(RELEASE_SCRIPTS)/dry-run-release.sh

##@ Docker

docker-build: ## Build Docker image
	docker build -t cortex:$(VERSION) -f build/package/Dockerfile .

docker-run: ## Run Docker container
	docker run --rm -v $(PWD)/cortex.toml:/etc/cortex/cortex.toml cortex:$(VERSION)

##@ Utilities

check: ## Run pre-commit checks (fmt, vet, test)
	$(MAKE) fmt
	$(MAKE) vet
	$(MAKE) test

info: ## Display build information
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Go Version: $(shell $(GO) version)"
