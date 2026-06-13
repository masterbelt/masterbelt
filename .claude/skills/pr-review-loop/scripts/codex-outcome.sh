#!/usr/bin/env bash
# codex-outcome.sh — detect a Codex (chatgpt-codex-connector[bot]) review outcome
# on a PR from FIRST-CLASS ARTIFACTS, not the ephemeral 👀/👍 reaction.
#
# Packages the Stage 7 probes (paginated, SHA-pinned, SINCE-scoped) so the loop
# does not re-derive fragile jq each round. It reports an OUTCOME and nothing
# more: it deliberately does NOT triage findings, and it never infers approval
# from silence — those are model judgment (see SKILL.md Stages 2-5, 7).
#
# Usage:
#   codex-outcome.sh <pr> <since-iso8601> <head-sha> [--watch [max_polls] [interval_sec]]
#
#   OUTCOME=FINDINGS             new bot inline comments OR a submitted COMMENTED
#                                review after SINCE → back to Stage 1
#   OUTCOME=APPROVED             a "Didn't find any major issues" comment for <head-sha>
#                                posted after SINCE → Stage 8. Approval requires this
#                                COMMIT-SCOPED artifact; a bare +1 is NOT enough (it
#                                carries no sha, and a slow prior run's +1 can land
#                                after a new trigger), so +1 is corroboration only.
#   OUTCOME=RUNNING              only 👀 / nothing terminal yet (single probe)
#   OUTCOME=TIMEOUT_NO_FINDINGS  --watch elapsed with no findings and no commit-scoped
#                                verdict. NOT an approval — keep waiting or surface to
#                                the human; do not advance to Stage 8 on silence alone.
#   OUTCOME=ERROR                a required probe could not be read (e.g. a transient gh
#                                failure). Fails closed — never reports APPROVED on a
#                                findings source it could not actually check.
#
# Testing: set CODEX_OUTCOME_FIXTURE=<dir> holding pulls_comments.json,
# issues_comments.json, pulls_reviews.json, issues_reactions.json to bypass gh.
# A fixture file whose content is the literal word ERROR simulates a read failure.
set -uo pipefail
BOT='chatgpt-codex-connector[bot]'

usage(){ echo "usage: codex-outcome.sh <pr> <since-iso8601> <head-sha> [--watch [max_polls] [interval_sec]]" >&2; exit 2; }
[ $# -ge 3 ] || usage
PR=$1; SINCE=$2; SHA=$3; shift 3
WATCH=0; MAXP=20; INT=90
if [ "${1:-}" = "--watch" ]; then WATCH=1; MAXP=${2:-20}; INT=${3:-90}; fi
SHA9=${SHA:0:9}

# fetch <api-path> <fixture-file> — echoes a JSON array; returns non-zero if the
# source could not be read (so callers can fail closed).
fetch(){
  if [ -n "${CODEX_OUTCOME_FIXTURE:-}" ]; then
    local f="$CODEX_OUTCOME_FIXTURE/$2"
    if [ -f "$f" ]; then
      [ "$(cat "$f")" = "ERROR" ] && return 1
      cat "$f"
    else
      echo '[]'
    fi
  else
    local out
    out=$(gh api --paginate "$1" 2>/dev/null) || return 1
    printf '%s' "$out" | jq -s 'add // []'
  fi
}

if [ -n "${CODEX_OUTCOME_FIXTURE:-}" ]; then
  REPO="fixture/fixture"
else
  REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner) || { echo "OUTCOME=ERROR"; exit 1; }
fi

probe(){
  local rc=0 pc revraw icraw rxraw
  # Decision sources — a read failure on any of these must fail closed.
  pc=$(fetch     "repos/$REPO/pulls/$PR/comments"   pulls_comments.json)  || rc=1
  revraw=$(fetch "repos/$REPO/pulls/$PR/reviews"    pulls_reviews.json)   || rc=1
  icraw=$(fetch  "repos/$REPO/issues/$PR/comments"  issues_comments.json) || rc=1
  # Corroboration only — its failure does not gate the decision.
  rxraw=$(fetch  "repos/$REPO/issues/$PR/reactions" issues_reactions.json) || rxraw='[]'
  PROBE_ERR=$rc
  [ -n "$pc" ]     || pc='[]'
  [ -n "$revraw" ] || revraw='[]'
  [ -n "$icraw" ]  || icraw='[]'
  [ -n "$rxraw" ]  || rxraw='[]'
  # findings: inline review comments after the trigger ...
  F=$(printf '%s' "$pc" | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and .created_at>$s)]|length')
  # ... OR a submitted COMMENTED review after the trigger (findings can ride the
  #     review body with no inline comments).
  REV=$(printf '%s' "$revraw" | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and (.submitted_at//"")>$s and .state=="COMMENTED")]|length')
  # clean verdict: the "no issues" comment, pinned to YOUR sha AND this round
  #     (created_at>$SINCE) — the only COMMIT-SCOPED approval artifact.
  V=$(printf '%s' "$icraw" | jq --arg b "$BOT" --arg s "$SINCE" --arg sha "$SHA9" \
        '[.[]|select(.user.login==$b and .created_at>$s and (.body|test("Didn.t find any major issues")) and (.body|test($sha)))]|length')
  # corroboration only: a +1 after the trigger. Reactions carry no sha, so this
  #     never gates APPROVED — a prior run's +1 can land after a new trigger.
  P=$(printf '%s' "$rxraw" | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and .content=="+1" and .created_at>$s)]|length')
}

decide(){
  if   [ "${F:-0}" -gt 0 ] || [ "${REV:-0}" -gt 0 ]; then echo FINDINGS
  elif [ "${PROBE_ERR:-0}" -ne 0 ];                  then echo ERROR
  elif [ "${V:-0}" -gt 0 ];                          then echo APPROVED
  else echo RUNNING; fi
}

line(){ echo "findings_inline=$F new_reviews=$REV clean_verdict=$V plus1=$P(corrob) err=$PROBE_ERR"; }

if [ "$WATCH" -eq 0 ]; then
  probe; d=$(decide)
  echo "[probe $(date -u +%H:%M:%SZ)] $(line) -> $d"
  echo "OUTCOME=$d"
  [ "$d" = ERROR ] && exit 1 || exit 0
fi

for i in $(seq 1 "$MAXP"); do
  probe; d=$(decide)
  echo "[poll $i $(date -u +%H:%M:%SZ)] $(line) -> $d"
  case "$d" in FINDINGS|APPROVED) echo "OUTCOME=$d"; exit 0 ;; esac
  # ERROR or RUNNING → keep polling (a transient failure may clear next round).
  [ "$i" -lt "$MAXP" ] && sleep "$INT"
done
echo "OUTCOME=TIMEOUT_NO_FINDINGS"
exit 0
