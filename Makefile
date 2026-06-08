GO            ?= go
GOLANGCI_LINT ?= golangci-lint
BIN_DIR       := bin
DIST_DIR      := dist

.DEFAULT_GOAL := build

# build compiles every command under ./cmd into BIN_DIR, one binary per command
# directory (e.g. cmd/masterbelt -> bin/masterbelt). New commands are picked up
# automatically. The binary reports its own version from Go's build info (the
# recorded VCS revision and commit date) — nothing is stamped in here.
.PHONY: build
build:
	$(GO) build -o $(BIN_DIR)/ ./cmd/...

# dist cross-builds the CLI for every release target into DIST_DIR, each archived
# deterministically with a SHA256SUMS manifest. The work lives in build/dist.sh
# so the same artifacts are produced locally and in CI (no CI-only build path).
.PHONY: dist
dist:
	DIST_DIR=$(DIST_DIR) sh build/dist.sh

# repro-check builds the release binary twice and asserts the two are
# byte-identical — the reproducible-build regression check. The flags match
# build/dist.sh; the version comes from the commit, so a rebuild reproduces it.
.PHONY: repro-check
repro-check:
	@tmp=$$(mktemp -d); \
	for o in a b; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -buildvcs=true -ldflags "-s -w -buildid=" -o $$tmp/$$o ./cmd/masterbelt; \
	done; \
	if cmp -s $$tmp/a $$tmp/b; then echo "reproducible: two linux/amd64 builds are byte-identical"; rm -rf $$tmp; \
	else echo "NOT reproducible: the two builds differ"; rm -rf $$tmp; exit 1; fi

# clean removes the built binaries and the dist artifacts.
.PHONY: clean
clean:
	rm -rf $(BIN_DIR) $(DIST_DIR)

# test runs the full test suite: the Go packages and the VS Code extension's
# grammar tests (which go test cannot reach — the generated TextMate grammar
# is pinned there).
.PHONY: test
test:
	$(GO) test ./...
	cd toolchain/editors/vscode && node --test

# fuzz runs the text-representation fuzzers (F-4 F1) for a short burst each:
# the unmarshalers must accept or reject arbitrary input without panicking.
# The same targets run their seed corpora as plain tests on every `make test`;
# this target is the standing deeper pass (CI's nightly slot once D-1 lands).
FUZZTIME ?= 30s
.PHONY: fuzz
fuzz:
	$(GO) test ./pkg/belt/parser/concrete/ -run FuzzCSTUnmarshal -fuzz FuzzCSTUnmarshal -fuzztime $(FUZZTIME)
	$(GO) test ./pkg/belt/parser/abstract/ -run FuzzASTUnmarshal -fuzz FuzzASTUnmarshal -fuzztime $(FUZZTIME)
	$(GO) test ./pkg/belt/semantic/ -run FuzzIRUnmarshal -fuzz FuzzIRUnmarshal -fuzztime $(FUZZTIME)

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

# bench runs the performance benchmarks (D-1 M2): the cold-compile and scale
# benches over the synthetic corpus (internal/beltgen) and the incremental
# edit-replay bench (pkg/belt/semantic). -benchmem reports the allocation
# counts the deterministic alloc gate (D-1 M3/M4) reads.
.PHONY: bench
bench:
	$(GO) test -bench=. -benchmem ./...

# bench-trend is the scoped wall-clock/memory benchmark the PR trend workflow
# (.github/workflows/perf-trend.yml) runs on both the PR and the merge-base so
# benchstat can compare them. It is a TREND measurement (D-1 §1, §4.2): time and
# bytes/op are advisory and NEVER a fail condition — only the deterministic
# counts in `make perf` gate. The scope is the cold-compile and incremental
# edit-replay benches (the same ones `make bench` covers) with a fixed, short
# -benchtime/-count so it finishes in CI time; benchstat's repeated samples are
# what make the comparison meaningful, not any single run. Override BENCHTIME /
# BENCHCOUNT / BENCHOUT to retarget (the workflow points BENCHOUT at new.txt /
# old.txt). Shared-runner variance means the result is advisory (D-1 §9).
BENCH_TREND_PKGS ?= ./internal/beltgen/ ./pkg/belt/semantic/
BENCH_TREND_RE   ?= BenchmarkColdCompile|BenchmarkIncremental
BENCHTIME        ?= 100ms
BENCHCOUNT       ?= 6
.PHONY: bench-trend
bench-trend:
	$(GO) test -run '^$$' -bench '$(BENCH_TREND_RE)' -benchmem \
		-benchtime=$(BENCHTIME) -count=$(BENCHCOUNT) $(BENCH_TREND_PKGS) \
		$(if $(BENCHOUT),| tee $(BENCHOUT),)

# bench-save runs bench-trend and writes its output to a file (default
# bench.txt) for later comparison: `make bench-save BENCHOUT=old.txt` on main,
# `make bench-save BENCHOUT=new.txt` on the branch, then `make benchstat`.
BENCHOUT ?=
.PHONY: bench-save
bench-save:
	$(MAKE) bench-trend BENCHOUT=$(or $(BENCHOUT),bench.txt)

# benchstat compares two saved bench-trend runs with golang.org/x/perf/cmd/
# benchstat (run via `go run` so no global install is needed). This is the same
# tool and invocation the PR trend workflow uses. It only REPORTS deltas; it has
# no fail mode here. Usage: `make benchstat OLD=old.txt NEW=new.txt`.
OLD ?= old.txt
NEW ?= new.txt
.PHONY: benchstat
benchstat:
	$(GO) run golang.org/x/perf/cmd/benchstat@latest $(OLD) $(NEW)

# perf runs the deterministic performance gates (D-1 §4.1) — the non-flaky
# hard fails CI relies on: the reuse snapshot (an edit's recompute footprint
# vs its golden, the over-invalidation guard) and the allocation ceilings
# (cold and incremental allocs/op). Wall-clock is a trend, measured by `bench`,
# never a fail condition; everything here is a deterministic count.
.PHONY: perf
perf:
	$(GO) test ./pkg/belt/semantic/ -run 'TestReuseSnapshot|TestColdCompileAllocCeiling|TestIncrementalAllocCeiling' -count=1

# prof captures a profile of a check run via the root's cross-cutting flags
# (D-1 §2): `make prof PROF_ARGS="check path/to/project"` writes cpu.prof and
# mem.prof to the working directory. Open with `go tool pprof`.
PROF_ARGS ?= check pkg/belt/testdata/projects/midsize
.PHONY: prof
prof: build
	$(BIN_DIR)/masterbelt --cpuprofile cpu.prof --memprofile mem.prof $(PROF_ARGS)
