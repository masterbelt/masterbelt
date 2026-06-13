#!/usr/bin/env bash
# codex-outcome.sh — detect a Codex (chatgpt-codex-connector[bot]) review outcome
# on a PR from FIRST-CLASS ARTIFACTS, not the ephemeral 👀/👍 reaction.
#
# Packages the Stage 7 probes (paginated, SHA-pinned, SINCE-scoped) so the loop
# does not re-derive fragile jq each run. It reports an OUTCOME and nothing more:
# it deliberately does NOT triage findings, and it never infers approval from
# silence — those are model judgment (see SKILL.md Stages 2-5, 7).
#
# Usage:
#   codex-outcome.sh <pr> <since-iso8601> <head-sha> [--watch [max_polls] [interval_sec]]
#
#   OUTCOME=FINDINGS             new bot inline comments OR a submitted COMMENTED
#                                review after SINCE → back to Stage 1
#   OUTCOME=APPROVED             a "Didn't find any major issues" comment for <head-sha>
#                                posted after SINCE, or a +1 reaction after SINCE → Stage 8
#   OUTCOME=RUNNING              only 👀 / nothing terminal yet (single probe)
#   OUTCOME=TIMEOUT_NO_FINDINGS  --watch elapsed with no findings and no positive verdict.
#                                This is NOT an approval — keep waiting or surface to the
#                                human; do not advance to Stage 8 on silence alone.
#
# Testing: set CODEX_OUTCOME_FIXTURE=<dir> holding pulls_comments.json,
# issues_comments.json, pulls_reviews.json, issues_reactions.json to bypass gh.
set -uo pipefail
BOT='chatgpt-codex-connector[bot]'

usage(){ echo "usage: codex-outcome.sh <pr> <since-iso8601> <head-sha> [--watch [max_polls] [interval_sec]]" >&2; exit 2; }
[ $# -ge 3 ] || usage
PR=$1; SINCE=$2; SHA=$3; shift 3
WATCH=0; MAXP=20; INT=90
if [ "${1:-}" = "--watch" ]; then WATCH=1; MAXP=${2:-20}; INT=${3:-90}; fi
SHA9=${SHA:0:9}

fetch(){ # fetch <api-path> <fixture-file>
  if [ -n "${CODEX_OUTCOME_FIXTURE:-}" ]; then
    cat "$CODEX_OUTCOME_FIXTURE/$2" 2>/dev/null || echo '[]'
  else
    gh api --paginate "$1" 2>/dev/null | jq -s 'add // []'
  fi
}

if [ -n "${CODEX_OUTCOME_FIXTURE:-}" ]; then
  REPO="fixture/fixture"
else
  REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner) || { echo "OUTCOME=ERROR"; exit 1; }
fi

probe(){
  local pc icraw revraw rxraw
  pc=$(fetch    "repos/$REPO/pulls/$PR/comments"   pulls_comments.json)
  icraw=$(fetch "repos/$REPO/issues/$PR/comments"  issues_comments.json)
  revraw=$(fetch "repos/$REPO/pulls/$PR/reviews"   pulls_reviews.json)
  rxraw=$(fetch "repos/$REPO/issues/$PR/reactions" issues_reactions.json)
  # findings: inline review comments after the trigger ...
  F=$(printf '%s'  "$pc"     | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and .created_at>$s)]|length')
  # ... OR a submitted COMMENTED review after the trigger (findings can ride the
  #     review body with no inline comments).
  REV=$(printf '%s' "$revraw" | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and (.submitted_at//"")>$s and .state=="COMMENTED")]|length')
  # clean verdict: the "no issues" comment, pinned to YOUR sha AND scoped to this
  #     round (created_at>$SINCE), so an older verdict for the same sha cannot match.
  V=$(printf '%s'  "$icraw"  | jq --arg b "$BOT" --arg s "$SINCE" --arg sha "$SHA9" \
        '[.[]|select(.user.login==$b and .created_at>$s and (.body|test("Didn.t find any major issues")) and (.body|test($sha)))]|length')
  # corroboration only: a +1 after the trigger.
  P=$(printf '%s'  "$rxraw"  | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and .content=="+1" and .created_at>$s)]|length')
}

decide(){
  if   [ "${F:-0}" -gt 0 ] || [ "${REV:-0}" -gt 0 ]; then echo FINDINGS
  elif [ "${V:-0}" -gt 0 ] || [ "${P:-0}"   -gt 0 ]; then echo APPROVED
  else echo RUNNING; fi
}

if [ "$WATCH" -eq 0 ]; then
  probe
  echo "[probe $(date -u +%H:%M:%SZ)] findings_inline=$F new_reviews=$REV clean_verdict=$V plus1=$P"
  echo "OUTCOME=$(decide)"
  exit 0
fi

for i in $(seq 1 "$MAXP"); do
  probe; d=$(decide)
  echo "[poll $i $(date -u +%H:%M:%SZ)] findings_inline=$F new_reviews=$REV clean_verdict=$V plus1=$P -> $d"
  [ "$d" != RUNNING ] && { echo "OUTCOME=$d"; exit 0; }
  [ "$i" -lt "$MAXP" ] && sleep "$INT"
done
echo "OUTCOME=TIMEOUT_NO_FINDINGS"
exit 0
