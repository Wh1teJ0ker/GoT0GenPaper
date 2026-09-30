GO ?= go
BIN := bin/got0genpaper
SUBJECT ?= 408
VERSION_FILE ?= VERSION
VERSION ?= $(shell if test -f "$(VERSION_FILE)"; then tr -d '[:space:]' < "$(VERSION_FILE)"; else echo dev; fi)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.PHONY: help fmt fmt-check mod-verify test test-race vet build check selftest generate pdf version

help:
	@echo 'make fmt        Format Go source files'
	@echo 'make fmt-check   Check Go source formatting'
	@echo 'make mod-verify  Verify downloaded Go modules'
	@echo 'make test       Run all Go tests'
	@echo 'make test-race  Run tests with the race detector'
	@echo 'make vet        Run go vet'
	@echo 'make build      Build bin/got0genpaper'
	@echo 'make check      Run format, module, test, vet, and build checks'
	@echo 'make selftest   Check local runtime and data dependencies'
	@echo 'make generate SUBJECT="数学二"  Generate a paper (uses the configured LLM)'
	@echo 'make pdf        Compile output/*.tex into PDFs'
	@echo 'make version    Print the local binary version'

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/got0genpaper

check: fmt-check mod-verify test vet build

selftest: build
	./$(BIN) test

generate: build
	./$(BIN) generate --subject "$(SUBJECT)"

pdf: build
	./$(BIN) compile

version: build
	./$(BIN) version

fmt:
	gofmt -w $$(find cmd internal -type f -name '*.go' -print)

fmt-check:
	test -z "$$(gofmt -l $$(find cmd internal -type f -name '*.go' -print))"

mod-verify:
	$(GO) mod verify
