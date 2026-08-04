# Immutable releases

ETH402 releases are source tags, two single-platform OCI images, and the
evidence binding those artifacts to one source commit. A release is not a
deployment and never enables settlement. Production must consume the recorded
digest, not a tag.

The release workflow is intentionally isolated from production. It has no
environment, deployment job, cloud identity, RPC credential, database secret,
SMTP credential, signer credential, or funded key. Its PostgreSQL password is
the fixed local test value used by an ephemeral Actions service container.

## One-time repository controls

Before the first public release, an administrator must:

1. Make the repository public, or use GitHub Enterprise Cloud and explicitly
   review removal of the workflow's public-visibility guard. GitHub artifact
   attestations are unavailable to private repositories on GitHub Free, Pro,
   and Team.
2. Enable release immutability under **Settings → General → Releases**. It
   applies only to releases created after it is enabled. With this setting,
   publishing locks the tag and assets and GitHub creates a release attestation.
3. Restrict the default `GITHUB_TOKEN` to read access. The workflow grants write
   access only to the separate publication/attestation jobs (`packages`,
   `attestations`, and OIDC) and the final release job (`contents`). Every action
   referenced by the publication boundary is GitHub-owned; registry login and
   push use the runner's Docker client. Third-party build, scanner, and SBOM
   actions run in a different job with `contents: read` only.
4. Protect `main`, require the ordinary CI workflow, require review, and limit
   who can create tags matching `v*`. Enable the dependency graph, Dependabot
   alerts/security updates, secret scanning, and push protection. The checked-in
   Dependabot file enables version-update pull requests; it does not switch on
   those repository settings.
5. Confirm GHCR package visibility and retention. Never permit release tags or
   digest-addressed manifests to be overwritten or garbage-collected while a
   supported deployment references them.

Those are GitHub settings and are not changed by repository code. Do not claim
immutable releases, native secret scanning, or attestations are active until an
administrator has verified them on the repository and on a completed release.

## Prepare and publish

1. Start from a clean, reviewed commit on `main`. Record the full commit SHA,
   migration list, reviewer disposition, and intended version. Any change after
   review needs explicit reviewer disposition.
2. Confirm the normal `ci` workflow passed for that exact commit. Review every
   Dependabot update, especially x402, go-ethereum, signing, PostgreSQL, base
   image, and workflow-action changes; automation never approves them.
3. Create a signed annotated SemVer tag locally and verify it before pushing:

   ```sh
   git switch main
   git pull --ff-only
   git status --short
   git tag -s vX.Y.Z -m 'ETH402 vX.Y.Z'
   git verify-tag vX.Y.Z
   git push origin vX.Y.Z
   ```

   A prerelease suffix such as `v1.0.0-rc.1` is accepted. The workflow resolves
   the exact annotated-tag object through GitHub's API and requires its
   cryptographic verification result to be `verified`; unsigned, malformed, or
   unverified tags fail. It also refuses branch dispatches, lightweight tags,
   and commits not reachable from `origin/main`. A manual run must select the
   existing tag in the GitHub ref selector; it is for retrying infrastructure
   failures, not for releasing a branch.
4. The workflow reruns formatting, vet, build, unit and race tests, destructive
   migration/integration tests, incident simulations, abuse tests, bounded
   parser fuzzing, golangci-lint, govulncheck, and full-history Gitleaks. All
   inputs are local fixtures and fakes. It does not run the mainnet-fork E2E,
   staged alert-delivery exercise, cloud-IAM review, or funded mainnet test.
5. For each of `facilitator` and `policysigner`, a read-only job builds one
   `linux/amd64` image, scans that exact local image with Trivy, generates an
   SPDX JSON SBOM with Syft, and freezes the image and SBOM in a checksummed,
   one-day workflow artifact. A HIGH or CRITICAL OS or library vulnerability
   blocks that handoff; exceptions require a separately reviewed, expiring
   policy change rather than ignoring a failed run.
6. A separate minimal job verifies the artifact checksums and local image ID,
   reloads the exact scanned image, and pushes it using the runner's Docker
   client. Only GitHub-owned pinned actions run in that job. The
   registry-returned digest is the subject of GitHub build-provenance and SBOM
   attestations. The final job attaches digest records, build identity, SPDX SBOMs,
   Sigstore bundles, a combined manifest, and SHA-256 checksums to a GitHub
   release, then publishes it. It refuses to overwrite an existing release.
   When release immutability is enabled, publication also locks the tag/assets.

Release versions are single-use before and after the GitHub Release exists. The
publication job checks GHCR immediately before each push and refuses any tag
that already resolves. If a run pushes either image and then fails—during the
other image, attestation, evidence upload, draft creation, or publication—the
version is burned. Preserve the failed run and registry digest as evidence and
start again with a new patch version; do not overwrite or delete the orphan tag.
Likewise, if the final job is interrupted after creating its draft, preserve and
inspect the draft, but do not retry a workflow that would repush its images.
Never move a published release tag.

## Verify and deploy by digest

Download the release assets, validate their checksums, and compare all three
identities:

```sh
sha256sum --check SHA256SUMS
gh attestation verify oci://ghcr.io/eth402/facilitator@sha256:<digest> \
  --repo ETH402/facilitator \
  --signer-workflow ETH402/facilitator/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z --source-digest <commit>
gh attestation verify oci://ghcr.io/eth402/policysigner@sha256:<digest> \
  --repo ETH402/facilitator \
  --signer-workflow ETH402/facilitator/.github/workflows/release.yml \
  --source-ref refs/tags/vX.Y.Z --source-digest <commit>
gh release verify vX.Y.Z --repo ETH402/facilitator
```

Confirm that each attestation names the expected source repository, workflow,
tag, and commit. Compare each digest with `release-manifest.txt` and the GHCR
package. Record the facilitator digest, policy-signer digest, commit, tag,
migration set, configuration checksum, and review reference in the deployment
change record.

Deploy only fully qualified digest references:

```text
ghcr.io/eth402/facilitator@sha256:<verified-digest>
ghcr.io/eth402/policysigner@sha256:<verified-digest>
```

The policy signer and facilitator are deliberately separate images and must run
under separate identities. Apply migrations from the same facilitator digest
before rolling out the application, run the production preflight, and follow
[Deployment](DEPLOYMENT.md), [Operations](OPERATIONS.md), and the rollback plan.
Tag equality, a green release workflow, and valid attestations do not authorize
funded settlement or replace an independent security review.

## Known limits

- The workflow emits only `linux/amd64`. Adding another platform changes the
  reviewed artifact and requires scanning and recording each platform manifest.
- Passing the exact scanned image across the permission boundary consumes more
  Actions artifact storage and transfer time than rebuilding in the publishing
  job. The compressed intermediate is retained for one day; release evidence is
  retained separately and the deployable image remains in GHCR by digest.
- GitHub-hosted tests cannot prove production IAM, TLS, backups, alert delivery,
  authenticated RPC independence, KMS behavior, or real mainnet transaction
  behavior.
- Vulnerability databases can be incomplete or later revised. Re-scan deployed
  digests continuously; do not treat a release-time scan as a permanent result.
- Provenance proves how an artifact was built, not that its source is correct.
  Verify attestations at deployment and retain the independent review evidence.
