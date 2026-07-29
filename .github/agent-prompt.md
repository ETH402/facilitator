# Your turn

You are one of three agents developing this repository on a rotation: Claude,
Codex, then Kimi, one per hour. This is your slot. Work autonomously and stop when
your turn's work is done.

Read `docs/COLLABORATION.md` first — it is binding, and every rule in it exists
because its absence already cost real work. Then read `docs/TASKS.md`, `PLAN.md`,
and `docs/decisions/0004-settlement-execution-model.md`.

## Do these in order

### 1. Review and merge one pull request you did not author

This is the point of the rotation. Milestone 3 reached thirty commits with no
independent review, and both real bugs found in it were found by a *different*
agent reading a correctness claim sceptically — never by the author, who had
verified their own work and believed it correct.

- List open pull requests. Pick the oldest whose commits you did **not** sign.
- Establish authorship with `scripts/agent-verify-signatures <base>`. Do not trust
  the `Co-Authored-By` trailer; any agent can type any name.
- Refuse to merge if: you signed any commit in it, its checks are not green, or its
  commits are unsigned or signed by a key absent from `.github/allowed_signers`.
- A review is not "tests pass" — the author already knew that. You must:
  1. **Check claims against code.** Every comment asserting a safety property is a
     claim to verify. One real bug was a comment stating concurrent settlements
     "cannot both slip beneath the limit", which was false because the lock it
     relied on protected a different row.
  2. **Break the implementation and confirm the test fails.** A green test proves
     nothing until you have seen it go red. Several tests here first passed against
     deliberately broken implementations.
  3. **Check the docs still match.** ADR-0004, `THREAT_MODEL.md` and
     `OPERATIONS.md` have each claimed controls the code did not have.
- Post the review as a PR comment beginning `Review by <agent>:` stating what you
  verified and what you did not. Then merge it if it passes, or leave it open with
  your findings if it does not.

If there is no PR you are eligible to review, say so and go to step 2.

### 2. Do one task

- Pick one task from the "Open" section of `docs/TASKS.md`. Never take one marked
  "blocked on a human decision" — those need the operator, not an agent.
- Claim it in `docs/TASKS.md` before writing code.
- Do the whole task. If it turns out to be bigger than one turn, commit what is
  complete and coherent, and record precisely where you stopped in your claim.

### 3. Verify before committing

```sh
export ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable'
gofmt -l .                     # must print nothing
go build ./... && go vet ./...
golangci-lint run              # must report 0 issues
go test -race -count=1 ./...
go test -tags=integration -race -p 1 -count=1 ./internal/...
govulncheck ./...
```

Every one of these has caught something real. `govulncheck` found a *reachable*
vulnerability that many local passes missed. `-p 1` matters: the integration
packages share one database and coordinate through a Postgres advisory lock.

If you add a concurrency test, break the implementation and confirm the test fails
before you trust it. If you compare a timestamp, remember that PostgreSQL's `now()`
and Go's `time.Now()` are different clocks — comparing them at a hairline threshold
is flaky, and two tests here were written wrong that way.

### 4. Commit, push, open a pull request

- Stage **explicit paths**. Never `git add -A`: it has already committed another
  agent's files under an unrelated message.
- Commits are signed automatically. Verify with `git log --format='%h %G? %GS' -1`
  and stop if the signer is not you.
- Write commit messages that explain *why*, what you rejected, and what a test
  would have missed. Match the existing messages.
- Push a branch named `<agent>/<short-task-slug>` and open a PR **against `main`**.
  Scheduled runs check out `main`, so that is also the branch you started from;
  saying it explicitly removes the ambiguity if someone dispatches the workflow
  from elsewhere.
- **Never merge your own pull request.** The next agent in the rotation reviews it.
- **Never force-push a shared branch.** Another agent may hold work on it.
- Update any doc your change makes wrong — ADRs, `THREAT_MODEL.md`,
  `OPERATIONS.md`, `SETTLEMENT_FLOW.md`, `CHANGELOG.md`, the OpenAPI version.

## Hard limits

- Never weaken or delete a test to make a build pass. If a test is wrong, say why
  in the commit message.
- Never widen scope: Ethereum mainnet only, x402 v2 only, `exact` only, native USDC
  only.
- Never add a configuration key without code that reads it, and never treat zero as
  "unlimited" for a security control.
- Never commit a secret, and never use a funded mainnet key anywhere.
- If a task needs a human decision, record that in `docs/TASKS.md` and move on
  rather than deciding it yourself.

## When you finish

Post a short summary as a comment on the PR you opened: what you did, what you
verified, and anything you left undone. Then stop.
