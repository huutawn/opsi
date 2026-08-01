# Bootstrap Worker release and single-service staging rollout

Status: source procedure only. Publishing and staging mutation require a reviewed
commit and an explicit operator run; this runbook does not claim live acceptance.

## Publish a reviewed revision

Run the `publish-bootstrap-worker` workflow from the `developer` branch in the
canonical `huutawn/opsi` repository. Enter the selected full 40-character
reviewed revision and the exact confirmation `publish-bootstrap-worker`. The
workflow requires that revision to equal the selected `developer` event SHA;
it rejects forks, other refs, malformed revisions, and every event other than
`workflow_dispatch` before checkout. It also refuses to overwrite an existing
full-revision tag.

The workflow checks out the selected event SHA, builds repository-root context
with `cloud/Dockerfile` target `bootstrap-worker` for `linux/amd64`, and pushes:

```text
ghcr.io/huutawn/opsi-bootstrap-worker:<40-character-source-revision>
```

The tag is for lookup only. Deployment identity is the emitted immutable
reference:

```text
ghcr.io/huutawn/opsi-bootstrap-worker@sha256:<64-lowercase-hex>
```

The `bootstrap-worker-release-<revision>` workflow artifact contains
`bootstrap-worker-release.json`. The same source revision, image digest,
platform, workflow run ID, and creation time appear in the job summary. This
JSON record is factual provenance metadata, not a signed cryptographic
attestation.

## Deploy only Bootstrap Worker

Download the manifest artifact to the staging control-plane host and check out
the reviewed Opsi revision containing this helper. Record the currently
approved Worker digest before mutation. For the current R5-017 staging baseline
the rollback target is:

```text
sha256:220d3ecc7dba018871707fc57612b3730259fd90b23dfde454a3299759167cff
```

Run from `/home/ubuntu/opsi`:

```bash
scripts/bootstrap-worker-release.py deploy \
  --manifest /protected/path/bootstrap-worker-release.json \
  --expected-current-digest sha256:220d3ecc7dba018871707fc57612b3730259fd90b23dfde454a3299759167cff \
  --compose-project opsi-staging \
  --compose-directory /home/ubuntu/opsi/deploy/staging-control-plane \
  --service bootstrap-worker \
  --health-timeout 180
```

An immutable image reference may replace `--manifest` by using `--image`, but it
must use the canonical GHCR repository and a lowercase SHA-256 digest. The
manifest path is preferred because it also validates the full source revision,
source repository, platform, workflow run, and release timestamp.

Before mutation the helper requires the running container's canonical
`RepoDigest` and the persisted `OPSI_BOOTSTRAP_WORKER_IMAGE` binding to equal
`--expected-current-digest`. A host-local lock prevents concurrent release
operations. The helper then prints `rollback_target`, pulls the new image,
backs up `.env`, atomically changes only that one non-secret binding, and runs
Compose with:

```text
up -d --no-deps --force-recreate bootstrap-worker
```

The helper waits for Worker health, proves the recreated container's actual
repository digest, and checks `https://opsidev.site/health`. It prints
`final_image` only after all checks pass. It never reads or prints container
environment values and never targets Cloud, PostgreSQL, or the reverse proxy.
Any failed command or health check returns nonzero; rollback remains explicit.

The atomic backup is stored beside `.env` as
`.env.bootstrap-worker-release.<UTC>.<unique>.bak`. The active `.env` binding
survives later Compose recreates and normal staging startup.

For the explicit R5-017 barrier run only, append the existing compatible
override after preparing its run-specific non-production Worker config:

```text
--compose-file compose.e2e-bootstrap-barrier.yaml
```

Normal staging deployment omits this flag. There is no release-specific compose
stack or second Worker service.

## Roll back only Bootstrap Worker

Use the previous immutable reference printed as `rollback_target`. Set
`--expected-current-digest` to the digest factually running when rollback starts:

```bash
scripts/bootstrap-worker-release.py rollback \
  --to ghcr.io/huutawn/opsi-bootstrap-worker@sha256:220d3ecc7dba018871707fc57612b3730259fd90b23dfde454a3299759167cff \
  --expected-current-digest sha256:<digest-currently-running> \
  --compose-project opsi-staging \
  --compose-directory /home/ubuntu/opsi/deploy/staging-control-plane \
  --service bootstrap-worker \
  --health-timeout 180
```

Rollback applies the same fail-closed current-digest check, pulls the target
before binding mutation, atomically persists it, recreates only Worker, waits
for Worker health, verifies Cloud health, and prints the factual final digest.
Rollback failure returns nonzero and must not be recorded as deployment success.
