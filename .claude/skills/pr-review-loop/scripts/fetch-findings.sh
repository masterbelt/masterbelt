#!/usr/bin/env bash
# fetch-findings.sh — print the current review's findings for triage: the bot's
# inline review comments AND its submitted review bodies, paginated and (optionally)
# scoped to this round. Stabilizes the triage fetch so a review-body-only finding
# is never dropped and pagination is never forgotten.
#
# Usage: fetch-findings.sh <pr> [since-iso8601] [head-sha]
#   With <since>, only artifacts after it are shown (this round). Omit to show all.
#   With <head-sha>, only findings on that commit are shown (drops stale old-head ones).
#
# Output blocks (newest inline first, then review bodies):
#   ID:<id>|<path>:<line>|<commit9>|<created_at>
#   IN_REPLY_TO:<id|none>
#   <body>
#   ===
#   REVIEW:<submitted_at>|<commit9>
#   <body>
#   ===
#
# Testing: set FINDINGS_FIXTURE=<dir> with pulls_comments.json and pulls_reviews.json.
set -uo pipefail
BOT='chatgpt-codex-connector[bot]'
[ $# -ge 1 ] || { echo "usage: fetch-findings.sh <pr> [since-iso8601] [head-sha]" >&2; exit 2; }
PR=$1; SINCE=${2:-1970-01-01T00:00:00Z}
# Optional head SHA: when given, only findings on that commit are shown, so a prior
# run's old-head comment landing after the push is not triaged. Empty → no head
# filter (startswith "" is always true), keeping the 2-arg form backward compatible.
HEAD9=${3:-}; HEAD9=${HEAD9:0:9}
# Bias the cutoff a couple seconds earlier so a same-second artifact is not dropped by
# the strict ">". This must match codex-outcome.sh, or a finding it counts as this round
# goes unprinted here and the loop bounces back with nothing to triage.
SINCE_EFF=$(date -u -d "$SINCE - 2 seconds" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "$SINCE")

fetch(){ # <api-path> <fixture-file>; returns non-zero if the source cannot be read
  if [ -n "${FINDINGS_FIXTURE:-}" ]; then
    local f="$FINDINGS_FIXTURE/$2"
    if [ -f "$f" ]; then
      [ "$(cat "$f")" = "ERROR" ] && return 1
      cat "$f"
    else
      echo '[]'
    fi
  else
    gh api --paginate "$1" 2>/dev/null | jq -s 'add // []'
  fi
}

if [ -n "${FINDINGS_FIXTURE:-}" ]; then
  REPO="fixture/fixture"
else
  REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner) || { echo "ERROR: cannot resolve repo" >&2; exit 1; }
fi

inline=$(fetch  "repos/$REPO/pulls/$PR/comments" pulls_comments.json) || { echo "ERROR: failed to read inline comments" >&2; exit 1; }
reviews=$(fetch "repos/$REPO/pulls/$PR/reviews"  pulls_reviews.json)  || { echo "ERROR: failed to read reviews" >&2; exit 1; }

printf '%s' "$inline" | jq -r --arg b "$BOT" --arg s "$SINCE_EFF" --arg sha "$HEAD9" '
  [.[] | select(.user.login==$b and .created_at>$s and (((.commit_id // "")|startswith($sha)) or ((.original_commit_id // "")|startswith($sha))))]
  | sort_by(.created_at) | reverse | .[]
  | "ID:\(.id)|\(.path):\(.line // .original_line)|\((.original_commit_id // "")[0:9])|\(.created_at)\nIN_REPLY_TO:\(.in_reply_to_id // "none")\n\(.body)\n==="'

printf '%s' "$reviews" | jq -r --arg b "$BOT" --arg s "$SINCE_EFF" --arg sha "$HEAD9" '
  [.[] | select(.user.login==$b and .state=="COMMENTED" and (.submitted_at // "")>$s and ((.body // "")|length>0) and ((.commit_id // "")|startswith($sha)))]
  | sort_by(.submitted_at) | .[]
  | "REVIEW_ID:\(.id)|\(.submitted_at)|\((.commit_id // "")[0:9])\n\(.body)\n==="'
