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

## 4. Every commit is signed by its agent

Run this once per agent, then commit normally — signing is automatic:

```sh
scripts/agent-signing-setup <agent>     # claude, codex, kimi
```

Signing identity is bound to the **lock**: `agent-lock acquire <agent>` points this
checkout at that agent's key, and `release` disables signing again. So the agent
doing the work is the agent whose key signs, without anyone having to remember.

That binding is necessary, not decorative. The checkout is shared, so
`.git/config` is shared: repo-local signing config set by one agent would sign the
next agent's commits too. It also overrides any *global* signing key — before this
existed, a global config signed all 33 commits on this branch with the operator's
personal key, so the audit trail claimed a human had personally signed work three
agents wrote.

A commit made without holding the lock is **unsigned**, which the verifier flags.
That is deliberate: unattributed is a gap, misattributed is a lie.

Signing goes through `scripts/agent-ssh-sign`, which clears `SSH_AUTH_SOCK` before
calling `ssh-keygen`. Without that, an SSH agent on the machine that owns its own
keys — 1Password's, for instance — intercepts the request and refuses, because the
agent's signing key is a plain file it has never heard of.

Keep the `Co-Authored-By:` trailer as well; it is readable at a glance. But the
**signature** is the authority.

**Why:** all three agents push as one GitHub identity, so a trailer is only as
trustworthy as whichever agent typed it — any agent can write any name. A
signature cannot be produced without the private key. Rule 5 depends on
establishing authorship, so it depends on this.

The keys are signing-only and never registered for authentication, so a leaked
one permits forged attribution, not repository access. Public keys live in
`.github/allowed_signers`, which is the repository's record of which key belongs
to which agent.

## 5. Cross-review is mandatory, and the author never merges

- Every PR must be reviewed by an agent that authored **none** of its commits.
- Establish authorship from **signatures**, not the git author and not the
  trailer:

  ```sh
  scripts/agent-verify-signatures        # who signed each commit on this branch
  ```

  It exits non-zero if any commit is unsigned or signed by a key absent from
  `.github/allowed_signers`, so it can gate the review.
- The reviewing agent merges. The authoring agent must not.

GitHub cannot enforce this: one account cannot be "someone else". The signatures
make a violation *detectable after the fact*, which is the strongest guarantee
available short of giving each agent its own GitHub account.

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

## The hourly rotation

`.github/workflows/agent-rotation.yml` runs one agent per hour by UTC hour:
`claude` at 00:00, `codex` at 01:00, `kimi` at 02:00, repeating. Override the pick
with the `workflow_dispatch` input. The turn instructions live in
`.github/agent-prompt.md`, versioned separately from the YAML so they are
reviewable.

Each turn reviews a pull request it did not author *before* doing its own work,
which is what makes rule 5 self-sustaining rather than aspirational. Turns branch
from `main` and open pull requests against `main`; there is no staging branch, so a
merged review lands on `main` directly.

GitHub runs `schedule:` workflows **only from the default branch**, so the workflow
file must exist on `main` before the rotation can fire at all. Until then nothing
happens at the hour, silently.

Setup it needs, one-time:

| Secret | Purpose |
|---|---|
| `AGENT_SIGNING_KEY_CLAUDE` / `_CODEX` / `_KIMI` | private signing key per agent, signing-only |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `KIMI_API_KEY` | model access per CLI |
| `AGENT_PAT` (optional but wanted) | so pushed commits trigger CI; pushes made with `GITHUB_TOKEN` deliberately do not start other workflows |

| Variable | Purpose |
|---|---|
| `KIMI_INSTALL` | Kimi's install command. It ships as a standalone binary with its own OAuth flow rather than an npm package, so the command is not hardcoded — a guessed URL would fail silently every third hour. |

Two scheduler facts worth knowing: GitHub cron is UTC and **not punctual** — a run
can lag well past the hour under load — and scheduled workflows are **disabled
after 60 days** without repository activity.

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
