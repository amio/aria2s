.DEFAULT_GOAL := help

.PHONY: help build test test-stage1 test-stage2

help: ## Show available development commands
	@printf "Usage: make <target>\n\nTargets:\n"
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the asv binary
	go build -o bin/asv .

test: ## Run the full Go test suite
	go test ./...

test-stage1: ## Run Stage 1 service CLI checks
	go test ./internal/... ./cmd

test-stage2: ## Run Stage 2 console-focused checks
	go test ./internal/aria2 ./internal/app ./internal/tui ./cmd
