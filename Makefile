SHELL := /usr/bin/env bash

RACE_PACKAGES := \
	./internal/scheduler/... \
	./internal/store/... \
	./internal/learner/... \
	./internal/dispatch/... \
	./internal/chief/...

.DEFAULT_GOAL := help

.PHONY: help build install clean test test-race service-install service-start service-stop

help:
	@echo "Available targets:"
	@echo "  make build        - Build cortex binary"
	@echo "  make install      - Build and install cortex to ~/.local/bin"
	@echo "  make clean        - Remove build artifacts"
	@echo "  make test         - Run all tests"
	@echo "  make test-race    - Run race tests for concurrency-critical packages via scripts/test-safe.sh"
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

test-race:
	scripts/test-safe.sh -race $(RACE_PACKAGES)

service-install:
	mkdir -p ~/.config/systemd/user/
	cp cortex.service ~/.config/systemd/user/
	systemctl --user daemon-reload

service-start:
	systemctl --user enable --now cortex.service

service-stop:
	systemctl --user stop cortex.service
	systemctl --user disable cortex.service
