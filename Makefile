GO ?= go
BIN := bin/got0genpaper
SUBJECT ?= 408
DIFFICULTY ?= default
DIFFICULTY_HIST ?=
DIFFICULTY_COUNTS ?=
VERSION_FILE ?= VERSION
VERSION ?= $(shell if test -f "$(VERSION_FILE)"; then tr -d '[:space:]' < "$(VERSION_FILE)"; else echo dev; fi)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

.PHONY: help fmt fmt-check mod-verify test test-race test-integration test-corpus test-output-audit vet build check selftest generate pdf version

help:
	@echo 'make fmt        Format Go source files'
	@echo 'make fmt-check   Check Go source formatting'
	@echo 'make mod-verify  Verify downloaded Go modules'
	@echo 'make test       Run all Go tests'
	@echo 'make test-race  Run tests with the race detector'
	@echo 'make test-integration  Run local full-pipeline tests (explicit, no CI)'
	@echo 'make test-corpus  Parse and compose bundled historical corpora (explicit)'
	@echo 'make test-output-audit  Reassemble and compile persisted output/live artifacts (explicit)'
	@echo 'make vet        Run go vet'
	@echo 'make build      Build bin/got0genpaper'
	@echo 'make check      Run format, module, test, vet, and build checks'
	@echo 'make selftest   Check local runtime and data dependencies'
	@echo 'make generate SUBJECT="数学二" DIFFICULTY=hard  Generate a paper (uses the configured LLM)'
	@echo 'make generate SUBJECT="数学二" DIFFICULTY_COUNTS=2,10,10  Use exact difficulty counts'
	@echo 'make pdf        Compile output/*.tex into PDFs'
	@echo 'make version    Print the local binary version'

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

test-integration:
	RUN_PIPELINE_TESTS=1 $(GO) test ./cmd/got0genpaper -run 'TestRunPipelineSmoke|TestRunPipelineZeroRepairRoundsOnlyValidates|TestStageFlowWithState' -v

test-corpus:
	RUN_CORPUS_TESTS=1 $(GO) test ./cmd/got0genpaper -run 'TestMath2CompositionUsesDifficultyTargets|TestAllSelectableSubjectsCanComposeFromBundledQuestions' -v

test-output-audit:
	RUN_OUTPUT_AUDIT=1 $(GO) test ./cmd/got0genpaper -run TestReassembleAudit -v

vet:
	$(GO) vet ./...

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/got0genpaper

check: fmt-check mod-verify test vet build

selftest: build
	./$(BIN) test

generate: build
	./$(BIN) generate --subject "$(SUBJECT)" --difficulty "$(DIFFICULTY)" --difficulty-hist "$(DIFFICULTY_HIST)" --difficulty-counts "$(DIFFICULTY_COUNTS)"

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
