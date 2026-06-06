GO            ?= go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR       := bin

.DEFAULT_GOAL := build

# build compiles every command under ./cmd into BIN_DIR, one binary per command
# directory (e.g. cmd/masterbelt -> bin/masterbelt). New commands are picked up
# automatically.
.PHONY: build
build:
	$(GO) build -o $(BIN_DIR)/ ./cmd/...

# clean removes the built binaries.
.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

# test runs the full test suite: the Go packages and the VS Code extension's
# grammar tests (which go test cannot reach — the generated TextMate grammar
# is pinned there).
.PHONY: test
test:
	$(GO) test ./...
	cd toolchain/editors/vscode && node --test

# generate runs code generation (the diagnostic tables, etc.).
.PHONY: generate
generate:
	$(GO) generate ./...

# verify-generated regenerates and fails if any generated file changed — the
# guard for a CSV (or other generator input) edited without rerunning
# `make generate`, or a regenerate whose output was never committed. Intended to
# run in CI on a clean checkout; the diff is scoped to the generator's outputs
# (the *_gen.go files and the editor grammar) so an unrelated working-tree edit
# does not trip it.
GENERATED := $(shell git ls-files '*_gen.go' 'toolchain/editors/vscode/syntaxes/*.json' 'toolchain/editors/vscode/language-configuration.json')
.PHONY: verify-generated
verify-generated:
	$(GO) generate ./...
	git diff --exit-code -- $(GENERATED)

# fmt formats all Go sources in place.
.PHONY: fmt
fmt:
	$(GO) fmt ./...

# vet runs go vet and golangci-lint (configured by .golangci.yml) over the
# module.
.PHONY: vet
vet:
	$(GO) vet ./...
	$(GOLANGCI_LINT) run ./...
