# Multi-agent collaboration protocol

Claude, ChatGPT, and Kimi all develop this repository. They share one checkout,
one branch per milestone, and one GitHub identity, which means none of the usual
safeguards apply automatically: git cannot tell them apart, and GitHub cannot
enforce "someone else reviewed this".

These rules exist because their absence has already cost real work. Each one
names the failure it prevents.

## 1. One agent edits at a time

The working tree is shared, so hold the lock before editing and release it after.

```sh
scripts/agent-lock status
scripts/agent-lock acquire <agent> "<task>"
# ... work, commit, push ...
scripts/agent-lock release <agent>
```

**Why:** three agents in one checkout produced a commit containing another
agent's diff, and a tree that did not compile because one agent was mid-edit
while another ran the build. Acquisition is atomic (`mkdir`), so there is no
window where two agents both believe they hold it.

If the lock is stale (45 minutes by default), `steal` it — that records who took
it and why. Then check `git status` before touching anything: the previous holder
may have left uncommitted work.

## 2. Never stage with `git add -A`

Stage the paths you changed, explicitly:

```sh
git add internal/settlement/recovery.go internal/settlement/recovery_test.go
```

**Why:** `git add -A` committed two files belonging to another agent under an
unrelated commit message. The code was fine; the history lied about what the
commit contained and who wrote it. `git add -p` or explicit paths make that
impossible. Run `git status` before every commit and account for every listed
file.

## 3. Claim a task before starting it

Claims live in [TASKS.md](TASKS.md), one line per task, committed. Claim it
before writing code, and release the claim when the PR merges.

**Why:** collisions become visible before code exists rather than at merge time.
The lock serialises the tree; the claim serialises the *work*, which is a longer
span than any single editing session.

## 4. Every commit names its agent

Use the trailer the repository already uses:

```
Co-Authored-By: <Agent Name> <noreply@example.com>
```

**Why:** all three agents push as the same git identity, so the trailer is the
only record of who wrote what. Rule 5 depends on it.

## 5. Cross-review is mandatory, and the author never merges

- Every PR must be reviewed by an agent that authored **none** of its commits.
- Determine authorship from the `Co-Authored-By:` trailers, not the git author.
- The reviewing agent merges. The authoring agent must not.

**Why:** Milestone 3 reached thirty commits with no independent review. Both
genuine bugs found in it were found by a *different* agent reading a correctness
claim sceptically — a merchant quota that did not serialise, and a calldata
allowlist that the docs described but the code did not have. Neither was found by
the author, who had verified their own work and believed it correct.

### What a review must actually do

"Tests pass" is not a review; the author already knew that. A review of this
codebase must:

1. **Check claims against code.** Every comment asserting a safety property is a
   claim to verify. The quota bug was a comment saying concurrent settlements
   "cannot both slip beneath the limit" — which was false, because the lock it
   relied on protected a different row.
2. **Break the implementation and confirm the test fails.** A green test proves
   nothing until you have seen it go red. Several tests here first passed against
   deliberately broken implementations.
3. **Check the docs still match.** `ADR-0004`, `THREAT_MODEL.md`, and
   `OPERATIONS.md` have each claimed controls that did not exist.
4. **Say what you verified**, not that you approve. Post the review as a PR
   comment beginning `Review by <agent>:` and state what was checked and what was
   not.

## 6. Leave the tree building

Before releasing the lock, the tree must compile and its tests must pass. If you
must stop mid-change, either commit it on a scratch branch or say so in your
claim — do not release the lock over a tree that does not build, because the next
agent will attribute the breakage to their own work.

## 7. Never force-push a shared branch

Another agent may hold local commits or uncommitted work on it. A mislabeled
commit is cheaper than destroying someone's afternoon. Fix history forward.

## Verification before any commit

```sh
export ETH402_TEST_DATABASE_URL='postgres://eth402:eth402_dev_only@localhost:5432/eth402_test?sslmode=disable'
gofmt -l .                     # must print nothing
go build ./... && go vet ./...
golangci-lint run              # must report 0 issues
go test -race -count=1 ./...
go test -tags=integration -race -p 1 -count=1 ./internal/...
govulncheck ./...
```

Tests use a **separate** `eth402_test` database on purpose: the suite runs
`TRUNCATE merchants CASCADE`, and pointing it at the `eth402` dev database
destroys local state. `-p 1` matters — the integration packages share one
database and coordinate through a Postgres advisory lock.

`govulncheck` is easy to skip locally and has already caught a *reachable*
vulnerability that many local passes missed. Run it.
