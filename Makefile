VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X github.com/amio/aria2s/cmd.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help build test

help: ## Show available development commands
	@printf "Usage: make <target>\n\nTargets:\n"
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the aria2s binary
	go build -ldflags "$(LDFLAGS)" -o bin/aria2s .

test: ## Run the full Go test suite
	go test ./...


