#!/usr/bin/env bash
# fetch-findings.sh — print the current review's findings for triage: the bot's
# inline review comments AND its submitted review bodies, paginated and (optionally)
# scoped to this round. Stabilizes the Stage 1 fetch so a review-body-only finding
# is never dropped and pagination is never forgotten.
#
# Usage: fetch-findings.sh <pr> [since-iso8601]
#   With <since>, only artifacts after it are shown (this round). Omit to show all.
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
[ $# -ge 1 ] || { echo "usage: fetch-findings.sh <pr> [since-iso8601]" >&2; exit 2; }
PR=$1; SINCE=${2:-1970-01-01T00:00:00Z}

fetch(){ # <api-path> <fixture-file>
  if [ -n "${FINDINGS_FIXTURE:-}" ]; then
    [ -f "$FINDINGS_FIXTURE/$2" ] && cat "$FINDINGS_FIXTURE/$2" || echo '[]'
  else
    gh api --paginate "$1" 2>/dev/null | jq -s 'add // []'
  fi
}

if [ -n "${FINDINGS_FIXTURE:-}" ]; then
  REPO="fixture/fixture"
else
  REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner) || { echo "ERROR: cannot resolve repo" >&2; exit 1; }
fi

inline=$(fetch  "repos/$REPO/pulls/$PR/comments" pulls_comments.json)
reviews=$(fetch "repos/$REPO/pulls/$PR/reviews"  pulls_reviews.json)

printf '%s' "$inline" | jq -r --arg b "$BOT" --arg s "$SINCE" '
  [.[] | select(.user.login==$b and .created_at>$s)]
  | sort_by(.created_at) | reverse | .[]
  | "ID:\(.id)|\(.path):\(.line // .original_line)|\((.original_commit_id // "")[0:9])|\(.created_at)\nIN_REPLY_TO:\(.in_reply_to_id // "none")\n\(.body)\n==="'

printf '%s' "$reviews" | jq -r --arg b "$BOT" --arg s "$SINCE" '
  [.[] | select(.user.login==$b and .state=="COMMENTED" and (.submitted_at // "")>$s and ((.body // "")|length>0))]
  | sort_by(.submitted_at) | .[]
  | "REVIEW:\(.submitted_at)|\((.commit_id // "")[0:9])\n\(.body)\n==="'
