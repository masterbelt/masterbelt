#!/usr/bin/env sh
# leak-signal.sh — steady-memory / leak SIGNAL for the LSP edit loop
# (D-1 M5, §4.2 "定常メモリの単調増加 = リーク疑い"; M-mem-rss).
#
# WHAT IT MEASURES: it runs the incremental edit-replay benchmark
# (BenchmarkIncremental) with a long -benchtime so many keystrokes are replayed
# against the live program, and reports the resulting bytes/op as a trend
# number. A steadily rising bytes/op night over night is the suspected-leak
# signal — the memo table growing without bound as edits accumulate.
#
# THIS IS ADVISORY AND A SIGNAL ONLY. The real leak DETECTION (periodic heap +
# memo-table sampling) belongs to D-1 M6's LSP instrumentation, owned by another
# change. Until those hooks exist, this bytes/op-over-a-long-edit-sequence proxy
# suffices (D-1 §4.2). It NEVER fails the build on a memory number — wall-clock
# and memory are trend metrics (D-1 §1); the script exits non-zero only if the
# benchmark fails to build/run.
set -eu

GO="${GO:-go}"
# A long edit sequence so retained-memory drift, if any, has room to show.
BENCHTIME="${BENCHTIME:-2s}"
BENCHCOUNT="${BENCHCOUNT:-3}"
PKG="${PKG:-./pkg/belt/semantic/}"

raw="$(mktemp)"
trap 'rm -f "$raw"' EXIT

"$GO" test -run '^$' -bench BenchmarkIncremental -benchmem \
	-benchtime="$BENCHTIME" -count="$BENCHCOUNT" "$PKG" | tee "$raw"

echo
echo "## Steady-memory / leak signal (nightly, advisory)"
echo
echo "Incremental edit-replay over a long keystroke sequence. The B/op column is"
echo "the per-edit retained-memory proxy (D-1 M-mem-rss). A steady night-over-"
echo "night rise is the suspected-leak signal — the memo table growing without"
echo "bound (D-1 §4.2). This is a SIGNAL, never a gate: real leak detection is"
echo "M6's LSP heap/memo sampling. Wall-clock and memory are trend metrics"
echo "(D-1 §1)."
echo
echo "| edit-replay size | ns/op | B/op (retained proxy) | allocs/op |"
echo "|---|---:|---:|---:|"
awk '
  /ns\/op/ {
    name = $1
    sub(/-[0-9]+$/, "", name)
    ns = ""; bytes = ""; allocs = ""
    for (i = 1; i <= NF; i++) {
      if ($(i+1) == "ns/op")     ns = $i
      if ($(i+1) == "B/op")      bytes = $i
      if ($(i+1) == "allocs/op") allocs = $i
    }
    printf "| %s | %s | %s | %s |\n", name, ns, (bytes==""?"-":bytes), (allocs==""?"-":allocs)
  }
' "$raw"
