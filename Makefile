# Makefile for crated

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%d)
LDFLAGS  = -X main.version=$(VERSION)

.PHONY: build test lint clean install coverage

## build: Build the crated binary
build:
	@go build -ldflags "$(LDFLAGS)" -o bin/crated ./cmd/crated

## install: Install crated to $GOPATH/bin
install:
	@go install -ldflags "$(LDFLAGS)" ./cmd/crated

## test: Run all tests
test:
	@go test -race -v ./...

## test-short: Run tests without verbose output
test-short:
	@go test -race ./...

## lint: Run linters
lint:
	@golangci-lint run ./...

## coverage: Generate and view test coverage report
coverage:
	@go test -race -coverprofile=coverage.out -covermode=atomic ./...
	@go tool cover -func=coverage.out
	@echo "\nTo view in browser: go tool cover -html=coverage.out"

## clean: Remove build artifacts
clean:
	@rm -rf bin/ dist/

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
