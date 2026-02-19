SHELL := /usr/bin/env bash

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
RACE_CI_JSON_OUT ?= .tmp/test-race.jsonl
RACE_CI_LOG_OUT ?= .tmp/test-race.log
BD_LOCK_CLEANUP_AGE_MINUTES ?= 5
BD_LOCK_CLEANUP_FORCE ?= 0
BD_LOCK_CLEANUP_REQUIRE_FORCE ?= 0
BD_LOCK_CLEANUP_REPORT_TO_MATRIX ?= 0
BD_LOCK_CLEANUP_MATRIX_ROOM ?=
BD_LOCK_CLEANUP_MATRIX_ACCOUNT ?= duc

.DEFAULT_GOAL := help

.PHONY: help build install clean test lint-beads test-race test-race-ci cleanup-bd-locks cleanup-bd-locks-escalation service-install service-start service-stop

help:
	@echo "Available targets:"
	@echo "  make build        - Build cortex binary"
	@echo "  make install      - Build and install cortex to ~/.local/bin"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make test         - Run all tests"
	@echo "  make lint-beads   - Validate open/in-progress beads have acceptance criteria + DoD gates"
	@echo "  make test-race    - Run race tests for concurrency-critical packages via scripts/test-safe.sh"
	@echo "  make cleanup-bd-locks - Remove stale bd lock files under .beads"
	@echo "  make cleanup-bd-locks-escalation - Require explicit force before removing stale bd locks"
	@echo "  make test-race-ci - CI race entrypoint with timeout/log output for debugging failures"
	@echo "  make service-install - Install user systemd service"
	@echo "  make service-start   - Start and enable user systemd service"
	@echo "  make service-stop    - Stop and disable user systemd service"

build:
	go build -o cortex ./cmd/cortex/

install: build
	cp cortex ~/.local/bin/

clean:
	rm -f cortex

test:
	go test ./...

lint-beads:
	scripts/lint-beads.sh

test-race:
	TEST_SAFE_GO_TEST_TIMEOUT=$(RACE_TIMEOUT) \
	TEST_SAFE_BD_LOCK_CLEANUP_MINUTES=$(BD_LOCK_CLEANUP_AGE_MINUTES) \
	TEST_SAFE_BD_LOCK_CLEANUP_REQUIRE_FORCE=$(BD_LOCK_CLEANUP_REQUIRE_FORCE) \
	TEST_SAFE_LOCK_WAIT_SEC=$(RACE_LOCK_WAIT) \
	TEST_SAFE_JSON_OUT="$(RACE_JSON_OUT)" \
	scripts/test-safe.sh -race $(RACE_PACKAGES)

test-race-ci:
	@mkdir -p .tmp
	@set -o pipefail; \
	TEST_SAFE_GO_TEST_TIMEOUT=$(RACE_CI_TIMEOUT) \
	TEST_SAFE_BD_LOCK_CLEANUP_MINUTES=$(BD_LOCK_CLEANUP_AGE_MINUTES) \
	TEST_SAFE_BD_LOCK_CLEANUP_REQUIRE_FORCE=$(BD_LOCK_CLEANUP_REQUIRE_FORCE) \
	TEST_SAFE_LOCK_WAIT_SEC=$(RACE_CI_LOCK_WAIT) \
	TEST_SAFE_JSON_OUT="$(RACE_CI_JSON_OUT)" \
	scripts/test-safe.sh -race $(RACE_PACKAGES) 2>&1 | tee "$(RACE_CI_LOG_OUT)"

cleanup-bd-locks:
	BD_LOCK_CLEANUP_FORCE="$(BD_LOCK_CLEANUP_FORCE)" \
	BD_LOCK_CLEANUP_REQUIRE_FORCE="$(BD_LOCK_CLEANUP_REQUIRE_FORCE)" \
	BD_LOCK_CLEANUP_REPORT_TO_MATRIX="$(BD_LOCK_CLEANUP_REPORT_TO_MATRIX)" \
	BD_LOCK_CLEANUP_MATRIX_ROOM="$(BD_LOCK_CLEANUP_MATRIX_ROOM)" \
	BD_LOCK_CLEANUP_MATRIX_ACCOUNT="$(BD_LOCK_CLEANUP_MATRIX_ACCOUNT)" \
	scripts/cleanup-bd-locks.sh "$(BD_LOCK_CLEANUP_AGE_MINUTES)"

cleanup-bd-locks-escalation:
	BD_LOCK_CLEANUP_REQUIRE_FORCE=1 \
	BD_LOCK_CLEANUP_FORCE="$(BD_LOCK_CLEANUP_FORCE)" \
	BD_LOCK_CLEANUP_REPORT_TO_MATRIX="$(BD_LOCK_CLEANUP_REPORT_TO_MATRIX)" \
	BD_LOCK_CLEANUP_MATRIX_ROOM="$(BD_LOCK_CLEANUP_MATRIX_ROOM)" \
	BD_LOCK_CLEANUP_MATRIX_ACCOUNT="$(BD_LOCK_CLEANUP_MATRIX_ACCOUNT)" \
	scripts/cleanup-bd-locks.sh "$(BD_LOCK_CLEANUP_AGE_MINUTES)"

service-install:
	mkdir -p ~/.config/systemd/user/
	cp cortex.service ~/.config/systemd/user/
	systemctl --user daemon-reload

service-start:
	systemctl --user enable --now cortex.service

service-stop:
	systemctl --user stop cortex.service
	systemctl --user disable cortex.service
