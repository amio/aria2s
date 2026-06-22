.PHONY: build test test-stage1

build:
	go build -o bin/asv .

test:
	go test ./...

test-stage1:
	go test ./internal/... ./cmd
