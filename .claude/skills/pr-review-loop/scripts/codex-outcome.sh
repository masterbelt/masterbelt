#!/usr/bin/env bash
# codex-outcome.sh — detect a Codex (chatgpt-codex-connector[bot]) review outcome
# on a PR from FIRST-CLASS ARTIFACTS, not the ephemeral 👀/👍 reaction.
#
# Packages the review-outcome probes (paginated, SHA-pinned, SINCE-scoped) so the loop
# does not re-derive fragile jq each round. It reports an OUTCOME and nothing
# more: it deliberately does NOT triage findings, and it never infers approval
# from silence — those are model judgment (see SKILL.md).
#
# Usage:
#   codex-outcome.sh <pr> <since-iso8601> <head-sha> [--watch [max_polls] [interval_sec]]
#
#   OUTCOME=FINDINGS             new bot inline comments OR a submitted COMMENTED
#                                review after SINCE → findings to triage
#   OUTCOME=APPROVED             the review is clean for this round: a "Didn't find any
#                                major issues" comment for <head-sha> after SINCE, OR a
#                                +1 reaction after SINCE → the review is clean. The +1 is honored by
#                                an explicit decision so a reaction-only clean review
#                                still converges; its residual risk (a prior run's late
#                                +1) is bounded only by the SINCE round scope.
#   OUTCOME=RUNNING              only 👀 / nothing terminal yet (single probe)
#   OUTCOME=NO_EYES              --watch saw no 👀 within the grace window (default 1200s;
#                                env NO_EYES_GRACE_SECONDS) and nothing terminal: the push
#                                was not picked up — re-post "@codex review" and re-watch,
#                                rather than wait out a review that never began
#   OUTCOME=TIMEOUT_NO_FINDINGS  --watch elapsed with no findings and no commit-scoped
#                                verdict. NOT an approval — keep waiting or surface to
#                                the human; do not treat silence alone as approval.
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
# Seconds to wait for 👀 before declaring the trigger missed — time-based, not a poll
# count, so the grace holds regardless of the --watch interval. The default is generous:
# a review here has started anywhere from seconds to ~18 minutes after a push, and too
# short a grace re-triggers a review that was merely slow to start.
NO_EYES_GRACE_SECS=${NO_EYES_GRACE_SECONDS:-1200}
# Bias the round cutoff a couple seconds earlier: GitHub timestamps are second-
# precision, so an artifact created in the same wall-clock second the trigger was
# recorded would be dropped by a strict ">". Prior-round artifacts are minutes old,
# so the small backward bias cannot pull them into this round.
SINCE_EFF=$(date -u -d "$SINCE - 2 seconds" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo "$SINCE")

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
  # Decision sources — a read OR jq-parse failure on any of these must fail closed,
  # so an unreadable findings source is never silently read as "no findings".
  pc=$(fetch     "repos/$REPO/pulls/$PR/comments"   pulls_comments.json)  || rc=1
  revraw=$(fetch "repos/$REPO/pulls/$PR/reviews"    pulls_reviews.json)   || rc=1
  icraw=$(fetch  "repos/$REPO/issues/$PR/comments"  issues_comments.json) || rc=1
  # Reactions feed the +1 approval and the NO_EYES liveness check, so a read
  # failure must be carried (RX_ERR), not silently read as "no reactions".
  rxraw=$(fetch  "repos/$REPO/issues/$PR/reactions" issues_reactions.json) && RX_ERR=0 || { rxraw='[]'; RX_ERR=1; }
  [ -n "$pc" ]     || pc='[]'
  [ -n "$revraw" ] || revraw='[]'
  [ -n "$icraw" ]  || icraw='[]'
  [ -n "$rxraw" ]  || rxraw='[]'
  # findings: inline review comments after the trigger, pinned to the pushed head so a
  #     prior run's comments landing late on the OLD head are not mistaken for findings
  #     on the new one. Match EITHER commit_id (the SHA the comment applies to) OR
  #     original_commit_id (where it was first made) — a fresh head comment can carry an
  #     earlier original_commit_id, so checking only one field would drop it ...
  F=$(printf '%s' "$pc" | jq --arg b "$BOT" --arg s "$SINCE_EFF" --arg sha "$SHA9" \
        '[.[]|select(.user.login==$b and .created_at>$s and (((.commit_id // "")|startswith($sha)) or ((.original_commit_id // "")|startswith($sha))))]|length') || rc=1
  # ... OR a submitted COMMENTED review with a NON-EMPTY body after the trigger
  #     (findings can ride the review body with no inline comments). The body check
  #     mirrors fetch-findings.sh, so an empty-body review never reports FINDINGS
  #     with nothing to triage.
  REV=$(printf '%s' "$revraw" | jq --arg b "$BOT" --arg s "$SINCE_EFF" --arg sha "$SHA9" \
        '[.[]|select(.user.login==$b and (.submitted_at//"")>$s and .state=="COMMENTED" and ((.body//"")|length>0) and ((.commit_id // "")|startswith($sha)))]|length') || rc=1
  # clean verdict: the "no issues" comment, pinned to YOUR sha AND STRICTLY after the
  #     trigger (unbiased $SINCE, like the +1) — an approval artifact must not count a
  #     prior verdict that landed a second or two before a same-SHA re-trigger.
  V=$(printf '%s' "$icraw" | jq --arg b "$BOT" --arg s "$SINCE" --arg sha "$SHA9" \
        '[.[]|select(.user.login==$b and .created_at>$s and (.body|test("Didn.t find any major issues")) and (.body|test($sha)))]|length') || rc=1
  # a +1 STRICTLY after the trigger. By an explicit project decision this counts as
  #     approval (so a clean review that leaves only the reaction still converges).
  #     Reactions carry no sha, so this uses the unbiased $SINCE (not $SINCE_EFF):
  #     the 2s same-second bias is right for findings but would let a prior run's
  #     +1 from just before the push count as approval.
  P=$(printf '%s' "$rxraw" | jq --arg b "$BOT" --arg s "$SINCE" \
        '[.[]|select(.user.login==$b and .content=="+1" and .created_at>$s)]|length') || rc=1
  # liveness: did the review actually start this round? An 👀 after the trigger
  #     means it started; its prolonged absence means the trigger never took.
  EYES=$(printf '%s' "$rxraw" | jq --arg b "$BOT" --arg s "$SINCE_EFF" \
        '[.[]|select(.user.login==$b and .content=="eyes" and .created_at>$s)]|length') || RX_ERR=1
  PROBE_ERR=$rc
}

decide(){
  if   [ "${F:-0}" -gt 0 ] || [ "${REV:-0}" -gt 0 ]; then echo FINDINGS
  elif [ "${PROBE_ERR:-0}" -ne 0 ];                  then echo ERROR
  elif [ "${V:-0}" -gt 0 ] || [ "${P:-0}" -gt 0 ];   then echo APPROVED
  else echo RUNNING; fi
}

line(){ echo "findings_inline=$F new_reviews=$REV clean_verdict=$V eyes=$EYES plus1=$P err=$PROBE_ERR rx_err=$RX_ERR"; }

if [ "$WATCH" -eq 0 ]; then
  probe; d=$(decide)
  echo "[probe $(date -u +%H:%M:%SZ)] $(line) -> $d"
  echo "OUTCOME=$d"
  [ "$d" = ERROR ] && exit 1 || exit 0
fi

last=RUNNING; eyes_seen=0
WATCH_START=$(date +%s 2>/dev/null || echo 0)
for i in $(seq 1 "$MAXP"); do
  probe; d=$(decide); last=$d
  [ "${EYES:-0}" -gt 0 ] && eyes_seen=1
  echo "[poll $i $(date -u +%H:%M:%SZ)] $(line) -> $d"
  case "$d" in FINDINGS|APPROVED) echo "OUTCOME=$d"; exit 0 ;; esac
  # The trigger never took: no 👀 after a generous ELAPSED grace and nothing terminal.
  # Surface NO_EYES so the caller can re-post "@codex review" instead of waiting out a
  # review that never started. Elapsed-based (not poll-count) so it holds at any
  # interval; skipped on an ERROR poll (reactions unreadable) and if the clock is
  # unavailable, so a review that is merely slow to start is not re-triggered.
  now=$(date +%s 2>/dev/null || echo 0)
  if [ "$d" != ERROR ] && [ "${RX_ERR:-0}" -eq 0 ] && [ "$eyes_seen" -eq 0 ] \
     && [ "$WATCH_START" -gt 0 ] && [ "$now" -gt 0 ] && [ $((now - WATCH_START)) -ge "$NO_EYES_GRACE_SECS" ]; then
    echo "OUTCOME=NO_EYES"; exit 0
  fi
  # ERROR or RUNNING → keep polling (a transient failure may clear next round).
  [ "$i" -lt "$MAXP" ] && sleep "$INT"
done
# Don't bury a persistent read failure as "no findings": if the last poll could
# not read a findings source, surface ERROR rather than TIMEOUT_NO_FINDINGS.
if [ "$last" = ERROR ]; then echo "OUTCOME=ERROR"; exit 1; fi
echo "OUTCOME=TIMEOUT_NO_FINDINGS"
exit 0
