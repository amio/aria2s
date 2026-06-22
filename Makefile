.PHONY: build test test-stage1 test-stage2

build:
	go build -o bin/asv .

test:
	go test ./...

test-stage1:
	go test ./internal/... ./cmd

test-stage2:
	go test ./internal/aria2 ./internal/app ./internal/tui ./cmd
