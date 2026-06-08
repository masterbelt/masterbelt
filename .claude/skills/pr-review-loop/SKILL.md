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

Pull the inline comments (the substance — the review *body* is usually boilerplate). **Paginate**, or on a multi-page PR you see only the first page and can miss fresh findings (and Stage 7 can falsely declare convergence). `gh api`'s own `--slurp` is rejected together with `--jq`, so paginate and merge the pages through an external `jq -s 'add'` before sorting:

```
gh api --paginate repos/<owner>/<repo>/pulls/<n>/comments \
  | jq -s 'add | sort_by(.created_at) | reverse | .[] | "ID:\(.id)|\(.user.login)|\(.path):\(.line // .original_line)|\(.original_commit_id[0:9])|\(.created_at)\nIN_REPLY_TO:\(.in_reply_to_id // "none")\n\(.body)\n==="'
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
4. **Make all gates green** (`go test ./...`, then `make test` / `make vet` / `make verify-generated`) before moving on.

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

- **Resolve the thread once it is handled** — a reply is not the same as resolving, and an open thread reads as "still outstanding". After you have *fixed* or *disproven* a finding, mark its review thread resolved (leave a thread you escalated to the human open — that one is theirs to close). Resolving is a GraphQL mutation keyed by the thread id, so map the comment's REST id (`databaseId`) to its thread, then resolve:

```
# the thread id for each unresolved thread, paired with its first comment's REST id
gh api graphql -f query='{ repository(owner:"<owner>", name:"<repo>"){ pullRequest(number:<n>){
  reviewThreads(first:100){ nodes{ id isResolved comments(first:1){ nodes{ databaseId } } } } } } }' \
  --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved|not) | "\(.id)\t\(.comments.nodes[0].databaseId)"'

# resolve the one whose databaseId matches the comment you addressed
gh api graphql -f query='mutation($t:ID!){ resolveReviewThread(input:{threadId:$t}){ thread{ isResolved } } }' -f t=<thread-id>
```

The PR's "unresolved conversations" count is the human's at-a-glance progress bar: drive it to zero for everything you handled, so what remains is exactly what still needs a person.

## Stage 7 — Trigger if needed, then detect the outcome (no human webhook)

This is the step that removes the human from the relay. The reviewer signals its state with **reactions on the PR body** (PRs live in the issues namespace) and with review comments — learn the three:

- **👀 `eyes`** — the review has *started* and is running. This is **not** a verdict; keep waiting. It lands within seconds of a trigger.
- **👍 `+1`** — *approved, no findings*. This is **convergence** → Stage 8.
- **inline comments / a submitted review** — *findings* → back to Stage 1.

Triggering — the part the earlier "never touch `@codex review`" advice got wrong:

- A **fix push to an already-open PR auto-triggers** a fresh review; you normally do not comment `@codex review`.
- But the **first review on a PR you opened yourself may not auto-start** — the "opened / ready-for-review" event is unreliable or slow here. **Post `@codex review` once** to kick it off; within seconds you should see the 👀 reaction. The same applies to any push that produces no 👀 after a few minutes — re-post `@codex review` to (re)trigger that one.

Detecting the outcome: after the trigger/push, poll for the **terminal** signal — a 👍 `+1` (approve) or new comments (findings) — and treat 👀 `eyes` as "still running", never as the answer:

All three lookups must **paginate** (pipe `gh api --paginate` to `jq -s 'add'`, since `--slurp` is rejected with `--jq`); a multi-page PR otherwise hides the bot's reaction/comments on a later page and the loop stalls or converges falsely:

```
# all the bot's reactions, to read state — eyes (running) vs +1 (approved)
gh api --paginate repos/<owner>/<repo>/issues/<n>/reactions \
  | jq -s 'add | map(select(.user.login|test("codex"))) | .[] | "\(.content)\t\(.created_at)"'

# convergence test — APPROVE is content == "+1" only, never eyes
gh api --paginate repos/<owner>/<repo>/issues/<n>/reactions \
  | jq -s '[add[] | select((.user.login|test("codex")) and .content=="+1")] | length'

# findings — bot inline comments across all pages, and the latest bot review
gh api --paginate repos/<owner>/<repo>/pulls/<n>/comments \
  | jq -s '[add[] | select(.user.login|test("codex"))] | length'
gh api --paginate repos/<owner>/<repo>/pulls/<n>/reviews \
  | jq -s '[add[] | select(.user.login|test("codex"))] | last | "\(.state)\t\(.submitted_at)"'
```

- **Poll in modest intervals**, not tightly — the verdict takes minutes (observed on this repo: the 👀 lands within seconds of the trigger, but the 👍 / comments arrive roughly 10–20 minutes later). Schedule a wake-up / background poll rather than blocking the turn. Record your trigger/push time so you only count signals newer than it.
- **No 👀 a few minutes after your trigger means the trigger did not take** — re-post `@codex review`. A 👀 but no verdict after a generous wait is worth surfacing to the human.
- **A findings-free review counts as approval too** — if a new bot review lands with no new inline comments and no unresolved threads, treat it as the approve signal even if you did not catch the reaction (reactions can be cleared, e.g. on merge). The robust test for convergence is "a fresh review happened and it introduced no actionable comment."
- **Non-convergence guard:** if the loop keeps surfacing *new substantive* issues after several rounds, or the same finding keeps coming back, that is itself a signal — pause and summarize to the human rather than grinding. A reviewer can also drift into self-contradiction or nits; recognising "this has converged enough" or "this needs a human" is part of the skill.

## Stage 8 — Finish

When the review has converged and every gate plus CI is green:

- Summarize the rounds: what was fixed, what was a misfire (with evidence), what the human decided, and the gates added.
- **Merge only if the human has authorized it.** Otherwise stop and report that the PR is review-clean, gates green, CI green, and hand the merge decision back. If authorized, merge per the project's policy (this repo: squash merge with a clean message — no plan IDs — then delete the branch), and do the post-merge bookkeeping the project expects (sync the default branch, delete the local branch, mark the plan/roadmap done).

## Anti-patterns

- **Being the human webhook in reverse** — fixing, pushing, and then stopping to be *told* the auto-review came. Detect it yourself: poll for the new comments or the approve reaction (Stage 7).
- **Assuming the first review auto-starts** — relying on the "opened / ready-for-review" event to kick off the first review on a PR you opened. It is unreliable here; post `@codex review` once and watch for the 👀.
- **Mistaking 👀 for approval** — the `eyes` reaction means the review *started*, not that it passed. Wait for 👍 `+1` (approve) or comments (findings); never converge on `eyes`.
- **Missing the silent approve** — treating "no new comments" as "still waiting" forever. No-findings is signalled by the 👍 `+1` reaction / a comment-free review, not by a comment; poll for that signal too, or the loop never finishes.
- **Fixing a misfire** because pushing back feels confrontational. Disprove it.
- **Flip-flopping** to satisfy a self-contradicting reviewer. Escalate the contradiction (Stage 5).
- **A fix with no gate.** The same finding returns; the loop never converges.
- **Replying but not resolving.** An answered-but-open thread reads as outstanding and hides your progress; resolve every thread you fixed or disproved (leave only the escalated ones open).
- **Reading only the first REST page.** Without pagination you miss findings or the approval on a later page; `gh api --paginate … | jq -s 'add | …'` (gh's `--slurp` is rejected with `--jq`).
- **Force-pushing** a reviewed branch, or squashing mid-review.
- **Plan/section refs in commit messages or code comments.** State the concept.
- **Over-asking.** A clear bug or a clear misfire is yours to handle without a human round-trip; the human is for the forks, not the easy calls.
