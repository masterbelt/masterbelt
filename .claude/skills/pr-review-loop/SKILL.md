---
name: pr-review-loop
description: >-
  Drive the PR review→fix loop with the automated reviewer (Codex on GitHub) to convergence, so a human never has to relay each review by hand. You start the first review on a self-opened PR with @codex review (fix pushes auto-trigger), then detect the reviewer's outcome from its PR reactions and comments — 👀 means the review started, 👍 means approved with no findings, inline comments mean findings — triage each finding into bug / misfire / judgment call, fix real bugs with a regression gate, disprove misfires with evidence, and STOP to ask the human only when a finding is a genuine design/scope decision or the reviewer contradicts an earlier accepted one. Use when a PR has (or is about to get) automated code-review comments to work through — "address the review", "レビュー来た", "Codex review", "handle the PR feedback", or right after opening/updating a reviewed PR.
argument-hint: "[PR number, e.g. 2 — defaults to the current branch's PR]"
---

# Drive the PR review loop to convergence

An automated reviewer (Codex, posting as `chatgpt-codex-connector[bot]`) reviews this repo's pull requests and leaves inline comments. The wrong way to work through them is for a human to read each review, paste it to you, wait for a fix, push, and re-trigger — the human becomes a "human webhook". This skill removes the human from that relay: **you detect the reviews, triage them, fix or rebut each, push, detect the next review's outcome, and loop until it converges — pausing for the human only at the genuine decisions.**

The point is the triage and the escalation, not the mechanics. Three things make this skill worth following:

1. **Not every finding is a fix.** A finding is a real bug, a misfire (the reviewer is wrong — prove it), or a judgment call (no clean answer). Treating all three as "fix it" produces churn and wrong changes.
2. **Every real fix ships with a regression gate** that goes red without the fix (the repo's "find a defect → write a gate that catches it next time" rule). A fix with no gate invites the same finding again.
3. **You stop and ask the human at the real forks** — a self-contradicting reviewer, a scope/design decision the plan does not settle, a fix that trades one valid behaviour for another. Guessing there is how a loop goes wrong.

## Stage 0 — Identify the PR and the reviewer

- Find the PR: the argument, or the current branch's PR (`gh pr view --json number,url,headRefName,state`). Confirm it is open.
- Note the reviewer's login (`chatgpt-codex-connector[bot]`) and your own, so you can tell review comments from your own replies.
- Know the gates this repo merges on (run them green before every push). The `go` aggregate check `needs` every job in `.github/workflows/go.yml` — treat that workflow as the authoritative list and match it: `gofmt -l pkg cmd internal toolchain` (must print nothing), `make check-fmt` (the belt-fmt corpus), `make vet` (vet + golangci, incl. complexity/funlen/gocognit), `make verify-generated` (no generated drift), `make test` (unit + snapshots + tree-sitter cst-pin + fmt corpus + grammar tests), and `make perf` (the deterministic performance gates). `go test ./...` is the fast per-fix check; run the full set before pushing — a change can pass `make test`/`vet`/`verify-generated` yet still fail required CI on the format or perf jobs. (If `make test` fails immediately after `make generate`, re-run it once — `tree-sitter generate` briefly leaves `parser.c` mid-write and the cst-pin reads it racily.)

## Stage 1 — Fetch the current review, fresh

Pull the inline comments (the usual substance) **and** the submitted review *bodies*. The inline comments are where findings normally live, but Codex can also post a finding in the review body with no inline comment — Stage 7 detects that as `FINDINGS`, so a comments-only fetch would bounce you back here with nothing to triage and the loop would spin. **Paginate** both, or on a multi-page PR you see only the first page and can miss fresh findings (and Stage 7 can falsely declare convergence). `gh api`'s own `--slurp` is rejected together with `--jq`, so paginate and merge the pages through an external `jq -s 'add'`. **Run `scripts/fetch-findings.sh <n> "$SINCE"`** — it fetches and formats both streams, paginated and round-scoped, so a review-body-only finding is never dropped (pinned by `fetch-findings.test.sh`). The snippets below are what it runs:

```
# inline review comments (the usual substance)
gh api --paginate repos/<owner>/<repo>/pulls/<n>/comments \
  | jq -s 'add | sort_by(.created_at) | reverse | .[] | "ID:\(.id)|\(.user.login)|\(.path):\(.line // .original_line)|\(.original_commit_id[0:9])|\(.created_at)\nIN_REPLY_TO:\(.in_reply_to_id // "none")\n\(.body)\n==="'

# submitted review BODIES from the bot, scoped to this round — catches a finding
# that rides the review body with no inline comment (else FINDINGS → Stage 1 spins)
gh api --paginate repos/<owner>/<repo>/pulls/<n>/reviews \
  | jq -s --arg b "chatgpt-codex-connector[bot]" --arg since "$SINCE" 'add | map(select(.user.login==$b and .state=="COMMENTED" and (.submitted_at // "") > $since and (.body|length>0))) | .[] | "REVIEW:\(.submitted_at)\n\(.body)\n==="'
```

- Process only comments **from the reviewer** that you have **not addressed yet**. Decide "mine vs the bot's" by the comment's author (`user.login`), **not** by `in_reply_to_id`: the reviewer can post a *fresh finding* as a reply inside an existing thread, so it carries `in_reply_to_id` too — skipping every reply with that field set would silently drop it. A comment is "already handled" only when *you* authored it or its id is in the set you have already addressed.
- Each finding usually carries a severity (P1/P2). Order your work by severity, but triage every one.

## Stage 2 — Triage each finding into one of three buckets

For each finding, decide which bucket it is **before** touching code:

- **Real bug** — the code is wrong or under-specified, and the fix is clear from the codebase, the plan, or conventions. → Stage 3.
- **Misfire** — the reviewer is mistaken (it misread the code, or asserts a behaviour the tooling does not actually have). → Stage 4. Do not change code on a claim you can cheaply disprove; prove it instead.
- **Judgment call** — there is no clean answer: the finding forces a design or scope decision the plan/north-star does not settle, two fixes trade one valid behaviour for another, the fix would reach into out-of-scope ("not yet") work, or it contradicts a decision an earlier round already accepted. → Stage 5 (stop and ask the human).

When unsure which bucket, prefer Stage 4's "verify first": reproduce the claim with the actual tool (run the parser, the CLI, the generator) before deciding.

## Stage 3 — Fix a real bug, with a regression gate

1. **Fix it minimally**, in the spirit of the surrounding code. Stay inside the PR's scope — do not start the "not yet" work to satisfy a finding.
2. **Add a gate that catches this exact defect next time.** Put it where the repo already pins that class (a snapshot, a table-driven parser/lowering test, the document fuzz alphabet, a highlight golden). Then **prove the gate works**: temporarily revert the fix, run the test, confirm it goes **red**, restore the fix, confirm **green**. A gate you have not seen fail is not a gate. (Disable the fix in a way that still compiles — e.g. flip a condition that still reads its variable — so you are testing the gate, not a build error.)
3. **Hardening a symmetric hole is fair game** — if a finding exposes a class (e.g. one recovery path swallowing the next token), fix the analogous path too and pin it, so the reviewer does not file the mirror image next round. This is *closing the class*, not scope creep; mechanical scope-widening (new features) is not.
4. **Make all the Stage 0 gates green** before moving on — `go test ./...` for the fast inner loop, then the full required set (`gofmt -l …`, `make check-fmt`, `make vet`, `make verify-generated`, `make test`, `make perf`). The format and perf jobs are required CI too, so the shorter `test`/`vet`/`verify-generated` trio is not enough.

## Stage 4 — Disprove a misfire, with evidence

1. **Reproduce the claim with the real tool**, not by reasoning. If the reviewer says "X produces a parse error", run the parser/CLI/generator over X and a few neighbours and capture the output.
2. **Make no code change.** Reply to the comment with the commands and their output, stating plainly why it does not reproduce, and offer to pin any specific case the reviewer has in mind. (Example from real use: a claim that a context keyword was globally reserved by the tree-sitter grammar — running the pinned CLI over `const master = 1`, `type master = nint`, … all parsed clean, because keyword extraction is context-aware.)

## Stage 5 — Stop and ask the human at a real fork

Some findings are not yours to decide. **Stop the loop and ask** (AskUserQuestion, with 2–4 concrete options and a recommendation) when:

- The reviewer **contradicts an earlier accepted decision** — e.g. round N asked for behaviour A on a construct, round N+1 asks for the opposite on the same construct. Surface the contradiction; do not silently flip-flop. (Context keywords in recovery are a classic source: "treat the keyword as the next member" vs "treat it as a name" are mutually exclusive.)
- The finding forces a **design or scope decision** the plan / north-star does not settle, or pulls in work the plan deferred ("not yet").
- Two valid fixes **trade one correct behaviour for another** and you cannot pick on principle alone.
- The "right" fix would **conflict with the charter / north-star** (then the north-star is fixed first — that is a human decision).

Frame the question with the evidence you gathered (the contradiction, the tradeoff), your recommendation, and the options. Once the human decides, record the decision (in the PR reply and, if the project keeps planning notes, there), then resume the loop. Do **not** escalate the easy buckets — a clear bug or a clear misfire is yours to handle; over-asking is its own failure.

## Stage 6 — Commit, push, reply

- **One commit per concern**, Conventional Commits with a scope (`fix(parser): …`, `feat(editor): …`), and the required trailer. The message describes *what changed and why* in plain terms — **no internal plan IDs, section numbers, or milestone tags** (the repo's commit/comment rule); state the concept, not its provenance.
- **Never force-push while the PR is under review.** Add commits on top — the reviewer reviewed a SHA, and a force-push rewrites it and breaks the threads; the squash at merge collapses the history anyway.
- **Push, then reply to each inline comment** with how it was addressed and the fix commit's short SHA (or, for a misfire, the evidence):

```
gh api repos/<owner>/<repo>/pulls/<n>/comments/<comment-id>/replies \
  -f body="Fixed in <sha>. <what changed> Pinned by <test>."
```

A reply per comment is what lets the reviewer (and the human) see the loop is actually closing, not stalling.

- **Resolve the thread once it is handled** — a reply is not the same as resolving, and an open thread reads as "still outstanding" and **keeps the PR from going merge-ready**, so you must resolve, not just reply. After you have *fixed* or *disproven* a finding, mark its review thread resolved (leave a thread you escalated to the human open — that one is theirs to close). Resolving is a GraphQL mutation keyed by the thread id, so map the comment you addressed to its thread, then resolve. Heads-up: resolving the *bot's* thread is a separate external write, so an automated-permission mode may gate it independently of your replies — if it gets denied, surface it so the human can authorize it or add a permission rule (e.g. allow the `resolveReviewThread` mutation); do not treat the denial as "resolving is optional". **Run `scripts/resolve-thread.sh <n> <comment-id>…`** — it maps each comment to its thread by an **exact** match on *any* comment's `databaseId` (a finding posted as a *reply* is not the thread's first comment) across the **paginated** threads connection, then resolves; both traps that bite a hand-written `grep`/`jq` are baked in and pinned by `resolve-thread.test.sh`. The snippets below are what it runs:

```
# every unresolved thread, paged, with ALL of its comment ids
gh api graphql --paginate -F owner=<owner> -F repo=<repo> -F number=<n> -f query='
query($owner:String!,$repo:String!,$number:Int!,$endCursor:String){
  repository(owner:$owner,name:$repo){ pullRequest(number:$number){
    reviewThreads(first:100, after:$endCursor){
      nodes{ id isResolved comments(first:50){ nodes{ databaseId } } }
      pageInfo{ hasNextPage endCursor } } } } }' \
  --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved|not) | "\(.id)\t\([.comments.nodes[].databaseId]|join(","))"'

# resolve the thread whose id list contains the comment you addressed
gh api graphql -f query='mutation($t:ID!){ resolveReviewThread(input:{threadId:$t}){ thread{ isResolved } } }' -f t=<thread-id>
```

The PR's "unresolved conversations" count is the human's at-a-glance progress bar: drive it to zero for everything you handled, so what remains is exactly what still needs a person. These `gh` snippets are illustrative for the PR sizes this loop actually meets; a pathological thread (50+ comments in one thread) or a very large thread set would need the inner `comments` connection paginated as well — extend them if you ever hit that, rather than treating the one-liners as exhaustive.

## Stage 7 — Trigger if needed, then detect the outcome (no human webhook)

This is the step that removes the human from the relay. **Key off Codex's first-class artifacts, not its reaction.** The reaction is the one GitHub signal that never arrives over a webhook and is cleared on merge, and Codex's own contract is "if it has suggestions it comments, *otherwise it reacts with 👍*" — so on a clean pass the 👍 may be the **only** thing it leaves (observed on this repo: a no-findings approval that posted just `+1` and no text, and reactions that were gone after merge). Anchoring convergence on that reaction is the root cause of the loop stalling. Treat the artifacts as the source of truth and the reaction as mere corroboration:

- **findings** — new bot **inline review comments**, or a submitted **review** (state `COMMENTED`, body opening `### 💡 Codex Review`), after your push → back to Stage 1. Always first-class, always reliable.
- **clean approval** — Codex *may* post a PR issue-comment `Codex Review: Didn't find any major issues …` carrying a `**Reviewed commit:** <sha>` line, *or* it may leave only a 👍 `+1` reaction with no text at all (both observed here). Approve **only on that commit-scoped verdict comment for your SHA** (posted after your trigger). The `+1` is *corroboration only* — it carries no commit, so a slow prior run's `+1` can land after your new trigger and "approve" the wrong HEAD; never gate on it, and never approve on the mere *absence* of findings, which cannot tell "passed" from "still running" → Stage 8.
- **👀 `eyes`** — the review *started*: corroboration that the trigger took, **not** a verdict. Keep waiting. It lands within seconds of a trigger.

Triggering — the part the earlier "never touch `@codex review`" advice got wrong:

- A **fix push to an already-open PR auto-triggers** a fresh review; you normally do not comment `@codex review`.
- But the **first review on a PR you opened yourself may not auto-start** — the "opened / ready-for-review" event is unreliable or slow here. **Post `@codex review` once** to kick it off; within seconds you should see the 👀 reaction. The same applies to any push that produces no 👀 after a few minutes — re-post `@codex review` to (re)trigger that one.

Detecting the outcome: after the trigger/push, run the bundled helper instead of re-deriving the probes by hand — it packages exactly the checks below (paginated, SHA-pinned, SINCE-scoped) and prints one verdict line:

```
scripts/codex-outcome.sh <n> "$SINCE" "$HEAD_SHA" [--watch [max_polls] [interval_sec]]
# → OUTCOME=FINDINGS | APPROVED | RUNNING | TIMEOUT_NO_FINDINGS | ERROR
```

`FINDINGS` → Stage 1; `APPROVED` → Stage 8; `RUNNING` / `TIMEOUT_NO_FINDINGS` → keep waiting, and on a long-stalled 👀 surface it to the human — the helper never infers approval from silence, and neither should you. The snippets below are what it runs, kept here so you can read and extend it. Two rules or every probe misfires: **paginate** (`gh api --paginate … | jq -s 'add'`, since `--slurp` is rejected with `--jq`) so a later page is not hidden, and **scope to this round** — apply a `created_at > $SINCE` cutoff to *every* time-based probe (the clean verdict included, or a re-review of the same SHA matches a prior round's verdict) and additionally pin the verdict to the pushed SHA. Record `SINCE` = the moment you triggered/pushed (ISO 8601) and `HEAD_SHA` = the SHA you pushed:

```
SINCE=<your trigger/push time, e.g. 2026-01-02T03:04:05Z>
HEAD_SHA=<the SHA you pushed, e.g. 9bf8482978>
BOT="chatgpt-codex-connector[bot]"

# findings (primary) — bot inline review-comments after your trigger; >0 ⇒ Stage 1
gh api --paginate repos/<owner>/<repo>/pulls/<n>/comments \
  | jq -s --arg b "$BOT" --arg since "$SINCE" '[add[] | select(.user.login==$b and .created_at > $since)] | length'

# findings (primary) — OR a submitted COMMENTED review after your trigger: Codex can put
# findings in the review body with NO inline comments, which the probe above would miss
gh api --paginate repos/<owner>/<repo>/pulls/<n>/reviews \
  | jq -s --arg b "$BOT" --arg since "$SINCE" '[add[] | select(.user.login==$b and (.submitted_at // "") > $since and .state=="COMMENTED")] | length'

# clean verdict (primary) — the "no issues" comment pinned to YOUR sha AND this round
# (created_at > $SINCE), so a prior verdict for the same sha cannot false-converge; 1 ⇒ approved
gh api --paginate repos/<owner>/<repo>/issues/<n>/comments \
  | jq -s --arg b "$BOT" --arg since "$SINCE" --arg sha "$HEAD_SHA" '[add[] | select(.user.login==$b and .created_at > $since and (.body|test("Didn.t find any major issues")) and (.body|test($sha[0:9])))] | length'

# corroboration only — the bot's reactions (eyes = running, +1 = approved); never the sole gate
gh api --paginate repos/<owner>/<repo>/issues/<n>/reactions \
  | jq -s --arg b "$BOT" --arg since "$SINCE" 'add | map(select(.user.login==$b and .created_at > $since)) | .[] | "\(.content)\t\(.created_at)"'
```

Then decide: **any findings probe > 0 → Stage 1.** Approve **only on the commit-scoped verdict comment for your SHA** (the helper's `APPROVED`) — it is the one positive artifact that pins the pass to *your* HEAD. The `+1` is corroboration: it carries no commit, so a slow prior run's `+1` can land after your trigger and "approve" the wrong HEAD; never gate on it, and never approve on silence. A 👀 with nothing after it means the review is still running or has stalled, **not** that it passed: keep polling, and after a generous wait surface it to the human, because delayed findings still arrive. And **fail closed** — if a findings source cannot be read (a transient `gh`/API failure → the helper's `ERROR`), retry; never read an unread source as "no findings".

- **Poll in modest intervals**, not tightly — the verdict takes minutes (observed on this repo: the 👀 lands within seconds of the trigger, but the 👍 / comments arrive roughly 10–20 minutes later). Schedule a wake-up / background poll rather than blocking the turn. Record your trigger/push time so you only count signals newer than it.
- **No 👀 a few minutes after your trigger means the trigger did not take** — re-post `@codex review`. A 👀 but no verdict after a generous wait is worth surfacing to the human.
- **Convergence is the commit-scoped verdict, not a quiet reaction** — a clean pass is the "Didn't find any major issues" comment for *your* SHA (the helper's `APPROVED`). Reactions clear on merge and a `+1` carries no commit, so never converge on a reaction alone; if that verdict comment never lands but everything is quiet, that is a stall to surface to the human, not an approval.
- **Non-convergence guard:** if the loop keeps surfacing *new substantive* issues after several rounds, or the same finding keeps coming back, that is itself a signal — pause and summarize to the human rather than grinding. A reviewer can also drift into self-contradiction or nits; recognising "this has converged enough" or "this needs a human" is part of the skill.

## Stage 8 — Finish

When the review has converged and every gate plus CI is green:

- Summarize the rounds: what was fixed, what was a misfire (with evidence), what the human decided, and the gates added.
- **Merge only if the human has authorized it.** Otherwise stop and report that the PR is review-clean, gates green, CI green, and hand the merge decision back. If authorized, merge per the project's policy (this repo: squash merge with a clean message — no plan IDs — then delete the branch), and do the post-merge bookkeeping the project expects (sync the default branch, delete the local branch, mark the plan/roadmap done).

## Anti-patterns

- **Being the human webhook in reverse** — fixing, pushing, and then stopping to be *told* the auto-review came. Detect it yourself: poll for the new comments or the verdict (Stage 7).
- **Assuming the first review auto-starts** — relying on the "opened / ready-for-review" event to kick off the first review on a PR you opened. It is unreliable here; post `@codex review` once and watch for the 👀.
- **Mistaking 👀 for approval** — the `eyes` reaction means the review *started*, not that it passed. Wait for the findings artifacts or the commit-scoped verdict (the "Didn't find any major issues" comment for your SHA); never converge on `eyes`.
- **Approving on silence or a bare reaction** — inferring a pass from "no findings yet" while a 👀 has landed but nothing terminal has, or from a lone `+1`. Silence cannot tell "approved" from "still running" (delayed findings still arrive), and the `+1` carries no commit — a slow prior run's `+1` can land after your trigger and "approve" the wrong HEAD. Wait for the commit-scoped verdict comment for your SHA; treat 👀/👍 as corroboration only, surface a long-stalled review to the human, and expect no reaction to survive merge.
- **Probing only inline comments for findings** — Codex can post a finding as a `COMMENTED` review *body* with no inline comment; an inline-only probe reports zero and the loop false-approves. Probe `/pulls/<n>/reviews` too, scoped to your trigger (the helper does both).
- **Fixing a misfire** because pushing back feels confrontational. Disprove it.
- **Flip-flopping** to satisfy a self-contradicting reviewer. Escalate the contradiction (Stage 5).
- **A fix with no gate.** The same finding returns; the loop never converges.
- **Replying but not resolving.** A reply is not a resolve: an answered-but-open thread reads as outstanding, hides your progress, and keeps the PR from going merge-ready. Resolve every thread you fixed or disproved (leave only the escalated ones open). The resolve is a *separate* write on the bot's thread that an automated-permission mode can gate on its own — if it is blocked, that is a permissions rule to add, not a step to skip.
- **Reading only the first REST page.** Without pagination you miss findings or the approval on a later page; `gh api --paginate … | jq -s 'add | …'` (gh's `--slurp` is rejected with `--jq`).
- **Force-pushing** a reviewed branch, or squashing mid-review.
- **Plan/section refs in commit messages or code comments.** State the concept.
- **Over-asking.** A clear bug or a clear misfire is yours to handle without a human round-trip; the human is for the forks, not the easy calls.
