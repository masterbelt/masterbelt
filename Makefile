GO      ?= go
BIN_DIR := bin

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

# fmt formats all Go sources in place.
.PHONY: fmt
fmt:
	$(GO) fmt ./...

# vet runs go vet over the module.
.PHONY: vet
vet:
	$(GO) vet ./...
