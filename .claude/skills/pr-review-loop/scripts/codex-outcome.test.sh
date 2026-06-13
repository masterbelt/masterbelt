#!/usr/bin/env bash
# Gate for codex-outcome.sh — drives the detector with a fixture per failure mode
# (and the happy paths) and asserts the OUTCOME. Run with
# `bash codex-outcome.test.sh`; non-zero exit on any failure.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SUT="$HERE/codex-outcome.sh"
SINCE='2026-06-13T03:42:18Z'
SHA='f0a483e061'
BOT='chatgpt-codex-connector[bot]'
fails=0

run(){ CODEX_OUTCOME_FIXTURE="$1" bash "$SUT" 43 "$SINCE" "$SHA" | sed -n 's/^OUTCOME=//p'; }
assert(){ if [ "$2" = "$3" ]; then echo "ok   - $1 ($3)"; else echo "FAIL - $1: expected $2, got $3"; fails=$((fails+1)); fi; }
mk(){ mkdir -p "$1"; for f in pulls_comments issues_comments pulls_reviews issues_reactions; do echo '[]' > "$1/$f.json"; done; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

# 1) findings posted as a review BODY only, no inline comments
D="$TMP/review_body_only"; mk "$D"
cat > "$D/pulls_reviews.json" <<JSON
[{"user":{"login":"$BOT"},"state":"COMMENTED","submitted_at":"2026-06-13T03:46:39Z","body":"### Codex Review"}]
JSON
assert "review-body-only => FINDINGS" FINDINGS "$(run "$D")"

# 2) stale no-issues verdict for the SAME sha but BEFORE this round
D="$TMP/stale_verdict"; mk "$D"
cat > "$D/issues_comments.json" <<JSON
[{"user":{"login":"$BOT"},"created_at":"2026-06-13T01:00:00Z","body":"Codex Review: Didn't find any major issues. Reviewed commit: $SHA"}]
JSON
assert "stale same-sha verdict => not approved" RUNNING "$(run "$D")"

# 3) only 👀, nothing terminal — must NOT infer approval from silence
D="$TMP/eyes_only"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"eyes","created_at":"2026-06-13T03:42:25Z"}]
JSON
assert "eyes-only => RUNNING (no silent approve)" RUNNING "$(run "$D")"

# 4) a +1 in this round counts as approval, so a reaction-only clean review converges
D="$TMP/plus1_round"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"+1","created_at":"2026-06-13T03:43:00Z"}]
JSON
assert "round-scoped +1 => APPROVED" APPROVED "$(run "$D")"

# 4b) a +1 from before this round must not approve — bounds a prior run's late reaction
D="$TMP/plus1_stale"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"+1","created_at":"2026-06-13T01:00:00Z"}]
JSON
assert "stale +1 (before round) => not approved" RUNNING "$(run "$D")"

# 5) a findings source cannot be read — fail closed even with a verdict present
D="$TMP/probe_error"; mk "$D"
echo 'ERROR' > "$D/pulls_comments.json"
cat > "$D/issues_comments.json" <<JSON
[{"user":{"login":"$BOT"},"created_at":"2026-06-13T03:55:00Z","body":"Codex Review: Didn't find any major issues. Reviewed commit: $SHA"}]
JSON
assert "unreadable findings source => ERROR (fail closed)" ERROR "$(run "$D")"

# 6) fresh commit-scoped verdict, after SINCE => APPROVED (happy path)
D="$TMP/fresh_verdict"; mk "$D"
cat > "$D/issues_comments.json" <<JSON
[{"user":{"login":"$BOT"},"created_at":"2026-06-13T03:55:00Z","body":"Codex Review: Didn't find any major issues. Reviewed commit: $SHA"}]
JSON
assert "fresh verdict => APPROVED" APPROVED "$(run "$D")"

# 7) inline finding after SINCE => FINDINGS (happy path)
D="$TMP/inline"; mk "$D"
cat > "$D/pulls_comments.json" <<JSON
[{"user":{"login":"$BOT"},"created_at":"2026-06-13T03:46:40Z","body":"P2 reject ..."}]
JSON
assert "inline comment => FINDINGS" FINDINGS "$(run "$D")"

# 8) --watch that only ever sees an unreadable findings source must end ERROR, not TIMEOUT
D="$TMP/watch_error"; mk "$D"; echo 'ERROR' > "$D/pulls_comments.json"
assert "watch persistent error => ERROR (not TIMEOUT)" ERROR \
  "$(CODEX_OUTCOME_FIXTURE="$D" bash "$SUT" 43 "$SINCE" "$SHA" --watch 1 0 | sed -n 's/^OUTCOME=//p')"

# 9) --watch with no 👀 within the grace window => NO_EYES (trigger never took)
D="$TMP/no_eyes"; mk "$D"
assert "watch, no eyes within grace => NO_EYES" NO_EYES \
  "$(EYES_GRACE_POLLS=2 CODEX_OUTCOME_FIXTURE="$D" bash "$SUT" 43 "$SINCE" "$SHA" --watch 3 0 | sed -n 's/^OUTCOME=//p')"

# 10) --watch WITH 👀 present must NOT cry NO_EYES (review started, just slow)
D="$TMP/eyes_running"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"eyes","created_at":"2026-06-13T03:43:00Z"}]
JSON
assert "watch, eyes present => not NO_EYES" TIMEOUT_NO_FINDINGS \
  "$(EYES_GRACE_POLLS=2 CODEX_OUTCOME_FIXTURE="$D" bash "$SUT" 43 "$SINCE" "$SHA" --watch 3 0 | sed -n 's/^OUTCOME=//p')"

# an empty-body COMMENTED review with no inline comments is not an actionable finding
D="$TMP/empty_review_body"; mk "$D"
cat > "$D/pulls_reviews.json" <<JSON
[{"user":{"login":"$BOT"},"state":"COMMENTED","submitted_at":"2026-06-13T03:46:39Z","body":""}]
JSON
assert "empty-body review => not findings" RUNNING "$(run "$D")"

# an artifact created in the same wall-clock second as SINCE still counts this round
D="$TMP/same_second"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"+1","created_at":"$SINCE"}]
JSON
assert "same-second +1 => APPROVED" APPROVED "$(run "$D")"

# a reactions read error must not be read as "no eyes" and fire a false NO_EYES
D="$TMP/rx_err_watch"; mk "$D"; echo 'ERROR' > "$D/issues_reactions.json"
assert "watch, reactions unreadable => not NO_EYES" TIMEOUT_NO_FINDINGS \
  "$(EYES_GRACE_POLLS=2 CODEX_OUTCOME_FIXTURE="$D" bash "$SUT" 43 "$SINCE" "$SHA" --watch 3 0 | sed -n 's/^OUTCOME=//p')"

# a jq parse/filter failure on a findings source must fail closed, not approve
D="$TMP/jq_fail"; mk "$D"
echo '{"not":"an array"}' > "$D/pulls_comments.json"
cat > "$D/issues_comments.json" <<JSON
[{"user":{"login":"$BOT"},"created_at":"2026-06-13T03:55:00Z","body":"Codex Review: Didn't find any major issues. Reviewed commit: $SHA"}]
JSON
assert "jq failure on a findings source => ERROR" ERROR "$(run "$D")"

# the default 👀 grace is generous, so a short watch must not declare NO_EYES early
D="$TMP/no_eyes_default"; mk "$D"
assert "default grace not exceeded in 3 polls => not NO_EYES" TIMEOUT_NO_FINDINGS \
  "$(CODEX_OUTCOME_FIXTURE="$D" bash "$SUT" 43 "$SINCE" "$SHA" --watch 3 0 | sed -n 's/^OUTCOME=//p')"

echo "---"
[ "$fails" -eq 0 ] && { echo "all green"; exit 0; } || { echo "$fails failed"; exit 1; }
