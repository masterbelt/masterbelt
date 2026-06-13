#!/usr/bin/env bash
# Gate for resolve-thread.sh — pins the comment->thread mapping against the exact
# slips that bit a hand-written grep: a comment that is FIRST in its list (right
# after the tab), a finding posted as a REPLY (not first), and a substring/no-match.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SUT="$HERE/resolve-thread.sh"
fails=0
assert(){ if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1: expected [$2], got [$3]"; fails=$((fails+1)); fi; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
FX="$TMP/threads.tsv"
printf 'PRRT_A\t3407400039,3407415335\n'      > "$FX"
printf 'PRRT_B\t3407400041,3407415355\n'     >> "$FX"
printf 'PRRT_C\t111,3407400043\n'            >> "$FX"

map(){ RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" --map-only 43 "$1" | cut -f2; }
miss(){ RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" --map-only 43 "$1"; }

assert "first-in-list (post-tab) maps"  PRRT_A "$(map 3407400039)"
assert "reply (second in list) maps"    PRRT_B "$(map 3407415355)"
assert "non-first after other first"    PRRT_C "$(map 3407400043)"
assert "unknown id -> no thread"        "9999999 -> no unresolved thread" "$(miss 9999999)"
assert "substring must NOT match"       "340740 -> no unresolved thread"  "$(miss 340740)"

# --list prints all unresolved threads
assert "--list shows all threads" 3 "$(RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" --list 43 | grep -c '^PRRT_')"

# a failed resolve write must propagate a non-zero exit, not report success
printf 'PRRT_FAIL\t555\n' >> "$FX"
RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" 43 555 >/dev/null 2>&1
assert "resolve write failure => non-zero exit" 1 "$?"
RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" 43 3407400039 >/dev/null 2>&1
assert "successful resolve => zero exit" 0 "$?"

# resolve/map with no comment ids must error, not no-op to a false "all resolved"
RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" 43 >/dev/null 2>&1
assert "no comment ids => usage error" 2 "$?"
RESOLVE_THREAD_FIXTURE="$FX" bash "$SUT" --list 43 >/dev/null 2>&1
assert "--list with only a PR is allowed" 0 "$?"

echo "---"
[ "$fails" -eq 0 ] && { echo "all green"; exit 0; } || { echo "$fails failed"; exit 1; }
