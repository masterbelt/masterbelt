#!/usr/bin/env bash
# resolve-thread.sh — resolve the review thread(s) containing the given review
# comment id(s). Bakes in the comment->thread mapping (EXACT match on any comment
# in the thread, paginated) that is easy to get wrong by hand — a reply is not the
# thread's first comment, and a naive grep slips on tab/substring boundaries.
#
# Usage:
#   resolve-thread.sh <pr> <comment-id> [<comment-id> ...]   # map + resolve
#   resolve-thread.sh --list <pr>                            # print unresolved threads
#   resolve-thread.sh --map-only <pr> <comment-id> ...       # print "<cid>\t<thread>", no write
#
# Testing: set RESOLVE_THREAD_FIXTURE=<file> with lines "<thread-id>\t<cid>,<cid>,..."
# (unresolved threads only). gh is then not called and resolve is simulated.
set -uo pipefail

MODE=resolve
case "${1:-}" in
  --list)     MODE=list;    shift ;;
  --map-only) MODE=map;     shift ;;
esac
[ $# -ge 1 ] || { echo "usage: resolve-thread.sh [--list|--map-only] <pr> [<comment-id> ...]" >&2; exit 2; }
PR=$1; shift
# resolve/map act on comment ids; without any, the loop below would no-op and exit
# 0, falsely signalling "all resolved". Only --list may run with just a PR.
if [ "$MODE" != list ] && [ $# -lt 1 ]; then
  echo "error: no comment ids given (resolve/map needs at least one; use --list to inspect)" >&2
  exit 2
fi

threads_tsv(){ # emits "<thread-id>\t<cid,cid,...>" for UNRESOLVED threads
  if [ -n "${RESOLVE_THREAD_FIXTURE:-}" ]; then
    cat "$RESOLVE_THREAD_FIXTURE"
  else
    local or; or=$(gh repo view --json nameWithOwner -q .nameWithOwner) || return 1
    # The outer thread connection is paged; the inner comments are capped at 100,
    # which covers any real Codex thread. A comment id in a larger thread simply
    # will not map, and map_cid's caller then reports it unresolved with a non-zero
    # exit (not a silent success) — full nested pagination is deliberately deferred.
    gh api graphql --paginate -F owner="${or%/*}" -F repo="${or#*/}" -F number="$PR" -f query='
      query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){
        repository(owner:$owner,name:$repo){ pullRequest(number:$number){
          reviewThreads(first:100, after:$endCursor){
            nodes{ id isResolved comments(first:100){ nodes{ databaseId } } }
            pageInfo{ hasNextPage endCursor } } } } }' \
      --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved|not) | "\(.id)\t\([.comments.nodes[].databaseId]|join(","))"'
  fi
}

map_cid(){ # <tsv> <cid> -> thread id (EXACT match on any comment); empty if none
  awk -F'\t' -v c="$2" '{n=split($2,a,","); for(i=1;i<=n;i++) if(a[i]==c){print $1; exit}}' <<<"$1"
}

resolve_tid(){ # <thread-id>; returns non-zero if the resolve did not succeed
  if [ -n "${RESOLVE_THREAD_FIXTURE:-}" ]; then
    case "$1" in
      *FAIL*) echo "(dry) FAILED $1"; return 1 ;;
      *)      echo "(dry) would resolve $1"; return 0 ;;
    esac
  fi
  local res
  res=$(gh api graphql -f query='mutation($t:ID!){ resolveReviewThread(input:{threadId:$t}){ thread{ isResolved } } }' -f t="$1" \
          --jq '.data.resolveReviewThread.thread.isResolved') || return 1
  echo "$1 resolved=$res"
  [ "$res" = "true" ]
}

TSV=$(threads_tsv) || { echo "ERROR: cannot read review threads" >&2; exit 1; }

if [ "$MODE" = list ]; then printf '%s\n' "$TSV"; exit 0; fi

rc=0
for CID in "$@"; do
  TID=$(map_cid "$TSV" "$CID")
  if [ -z "$TID" ]; then echo "$CID -> no unresolved thread"; rc=1; continue; fi
  if [ "$MODE" = map ]; then printf '%s\t%s\n' "$CID" "$TID"; continue; fi
  out=$(resolve_tid "$TID"); st=$?
  echo "$CID -> $out"
  [ "$st" -eq 0 ] || rc=1
done
exit $rc
