#!/usr/bin/env bash
# Gate for codex-outcome.sh — drives the detector with fixtures for the failure
# modes the Codex review caught across two rounds, plus the happy paths. Run with
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

# 1) findings posted as a review BODY only, no inline comments (round-1 finding #2)
D="$TMP/review_body_only"; mk "$D"
cat > "$D/pulls_reviews.json" <<JSON
[{"user":{"login":"$BOT"},"state":"COMMENTED","submitted_at":"2026-06-13T03:46:39Z","body":"### Codex Review"}]
JSON
assert "review-body-only => FINDINGS" FINDINGS "$(run "$D")"

# 2) stale no-issues verdict for the SAME sha but BEFORE this round (round-1 finding #3)
D="$TMP/stale_verdict"; mk "$D"
cat > "$D/issues_comments.json" <<JSON
[{"user":{"login":"$BOT"},"created_at":"2026-06-13T01:00:00Z","body":"Codex Review: Didn't find any major issues. Reviewed commit: $SHA"}]
JSON
assert "stale same-sha verdict => not approved" RUNNING "$(run "$D")"

# 3) only 👀, nothing terminal — must NOT infer approval from silence (round-1 finding #1)
D="$TMP/eyes_only"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"eyes","created_at":"2026-06-13T03:42:25Z"}]
JSON
assert "eyes-only => RUNNING (no silent approve)" RUNNING "$(run "$D")"

# 4) a bare +1 after the trigger is NOT commit-scoped — must NOT approve (round-2 finding B)
D="$TMP/bare_plus1"; mk "$D"
cat > "$D/issues_reactions.json" <<JSON
[{"user":{"login":"$BOT"},"content":"+1","created_at":"2026-06-13T03:43:00Z"}]
JSON
assert "bare +1 => not approved" RUNNING "$(run "$D")"

# 5) a findings source cannot be read — fail closed even with a verdict present (round-2 finding A)
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

echo "---"
[ "$fails" -eq 0 ] && { echo "all green"; exit 0; } || { echo "$fails failed"; exit 1; }
