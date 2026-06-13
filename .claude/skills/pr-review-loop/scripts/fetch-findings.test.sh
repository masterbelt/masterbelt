#!/usr/bin/env bash
# Gate for fetch-findings.sh — a fixture PR with bot/non-bot, in/out-of-round
# inline comments and review bodies; asserts only this round's bot findings show,
# and crucially that a review-body-only finding is included (not dropped).
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SUT="$HERE/fetch-findings.sh"
SINCE='2026-06-13T03:42:18Z'
BOT='chatgpt-codex-connector[bot]'
fails=0
assert(){ if [ "$2" = "$3" ]; then echo "ok   - $1"; else echo "FAIL - $1: expected [$2], got [$3]"; fails=$((fails+1)); fi; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
D="$TMP/fx"; mkdir -p "$D"
cat > "$D/pulls_comments.json" <<JSON
[
  {"id":1,"user":{"login":"$BOT"},"path":"a.go","line":10,"original_commit_id":"abcdef0123","created_at":"2026-06-13T03:46:40Z","in_reply_to_id":null,"body":"inline finding one"},
  {"id":2,"user":{"login":"$BOT"},"path":"a.go","line":11,"original_commit_id":"abcdef0123","created_at":"2026-06-13T03:00:00Z","in_reply_to_id":null,"body":"OLD pre-round finding"},
  {"id":3,"user":{"login":"someoneelse"},"path":"a.go","line":12,"original_commit_id":"abcdef0123","created_at":"2026-06-13T03:50:00Z","in_reply_to_id":null,"body":"not the bot"}
]
JSON
cat > "$D/pulls_reviews.json" <<JSON
[
  {"user":{"login":"$BOT"},"state":"COMMENTED","commit_id":"abcdef0123","submitted_at":"2026-06-13T03:46:39Z","body":"REVIEW BODY FINDING with no inline comment"},
  {"user":{"login":"$BOT"},"state":"COMMENTED","commit_id":"abcdef0123","submitted_at":"2026-06-13T03:47:00Z","body":""},
  {"user":{"login":"$BOT"},"state":"APPROVED","commit_id":"abcdef0123","submitted_at":"2026-06-13T03:48:00Z","body":"lgtm"}
]
JSON

OUT=$(FINDINGS_FIXTURE="$D" bash "$SUT" 43 "$SINCE")

assert "this round's inline finding shown"      1 "$(grep -c '^ID:1|' <<<"$OUT")"
assert "pre-round inline finding hidden"        0 "$(grep -c '^ID:2|' <<<"$OUT")"
assert "non-bot comment hidden"                 0 "$(grep -c '^ID:3|' <<<"$OUT")"
assert "review-body-only finding shown"         1 "$(grep -c 'REVIEW BODY FINDING' <<<"$OUT")"
assert "empty-body review hidden"               0 "$(grep -c '^REVIEW:2026-06-13T03:47' <<<"$OUT")"
assert "non-COMMENTED (approved) review hidden" 0 "$(grep -c 'lgtm' <<<"$OUT")"
assert "exactly two finding blocks"             2 "$(grep -c '^===' <<<"$OUT")"

echo "---"
[ "$fails" -eq 0 ] && { echo "all green"; exit 0; } || { echo "$fails failed"; exit 1; }
