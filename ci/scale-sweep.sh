#!/usr/bin/env sh
# scale-sweep.sh — run the scale-curve benchmarks and emit a Markdown table of
# the size sweep (D-1 M5, §4.2 M-scale + §5). This is the body of the nightly
# perf-nightly workflow, factored out so it runs identically locally:
#
#   sh ci/scale-sweep.sh > sweep.md
#
# WHAT IT MEASURES: BenchmarkColdCompile and BenchmarkIncremental carry a
# sub-benchmark per generator size (files=.../decls=.../depth=.../branch=...),
# so ns/op and B/op across those rows trace the size curve. Watching the curve
# over nights makes an O(n^2) cliff visible before it ships (D-1 M-scale).
#
# THIS IS ADVISORY. Wall-clock and memory are TREND metrics (D-1 §1) — this
# script NEVER decides pass/fail; it only formats. The deterministic gates live
# in `make perf`. The script exits non-zero only if the benchmark binary itself
# fails to build/run (a real breakage), not for any performance number.
set -eu

GO="${GO:-go}"
BENCHTIME="${BENCHTIME:-200ms}"
BENCHCOUNT="${BENCHCOUNT:-3}"
PKGS="${PKGS:-./internal/beltgen/ ./pkg/masterbelt/semantic/}"
RE="${RE:-BenchmarkColdCompile|BenchmarkIncremental}"

raw="$(mktemp)"
trap 'rm -f "$raw"' EXIT

# Run the sweep; -run '^$' skips unit tests so only benches execute. Failure to
# build/run propagates (set -e); performance numbers never cause a non-zero exit.
# shellcheck disable=SC2086
"$GO" test -run '^$' -bench "$RE" -benchmem \
	-benchtime="$BENCHTIME" -count="$BENCHCOUNT" $PKGS | tee "$raw"

echo
echo "## Scale sweep (nightly, advisory)"
echo
echo "Size curve over the synthetic corpus. Wall-clock and memory are **trend**"
echo "metrics (D-1 §1) — this table is reported, never a gate. Watch ns/op and"
echo "B/op climb across the size rows: a super-linear jump between adjacent sizes"
echo "is the O(n^2) cliff this sweep exists to surface (D-1 M-scale, §4.2)."
echo
echo "| benchmark / size | ns/op | B/op | allocs/op |"
echo "|---|---:|---:|---:|"
# Parse Go's benchmark output: a result line is
#   BenchmarkName/sub-24   <iters>   <ns> ns/op   <B> B/op   <allocs> allocs/op
awk '
  /ns\/op/ {
    name = $1
    sub(/-[0-9]+$/, "", name)   # strip the -GOMAXPROCS suffix
    ns = ""; bytes = ""; allocs = ""
    for (i = 1; i <= NF; i++) {
      if ($(i+1) == "ns/op")     ns = $i
      if ($(i+1) == "B/op")      bytes = $i
      if ($(i+1) == "allocs/op") allocs = $i
    }
    printf "| %s | %s | %s | %s |\n", name, ns, (bytes==""?"-":bytes), (allocs==""?"-":allocs)
  }
' "$raw"
