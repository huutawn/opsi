# Opsi Roadmap v4

| Metadata | Value |
|---|---|
| Status | Canonical active roadmap |
| Last updated | 2026-07-13 |
| Architecture decision | `docs/architecture_decisions/ADR-004-trusted-artifact-cd.md` |
| Current implementation truth | `docs/current_state.md` and `docs/status_matrix.md` |

This file is the single canonical active roadmap for Opsi. Requirements and
target architecture are not implementation evidence. A capability exists only
when the status matrix cites code, tests, executable checks, and any required
real-infrastructure evidence.

## 1. Objective

Roadmap v4 orders the work required to move from the current development
control plane and legacy/manual Git-source deployment path to a production
delivery system with:

```text
GitHub Actions build/test
-> OCI registry
-> registry/repository@sha256:<digest>
-> Opsi Cloud BuildRecord
-> DeploymentJob
-> eligible healthy Agent
-> K3s/containerd rollout
```

The roadmap also preserves the user-owned AI boundary: trusted CD is governed
by a preconfigured `DeploymentPolicy`, while AI-originated mutations use the
separate Safe ActionPlane and human `ApprovalGrant` flow.

## 2. Current-state warning

The trusted OCI artifact flow in this roadmap remains target architecture. P03
restores the Agent executable and a direct Linux release artifact only; it does
not implement application artifact delivery, Cloud routing, or CD.

Current implementation facts remain:

- Agent supports cloning and building Git source for deployment.
- Image-source deployment is rejected before runtime execution.
- GitHub App user authorization, installation authentication, repository
  mapping, GitHub Actions OIDC, `BuildRecord`, digest deployment,
  `DeploymentPolicy`, PR preview deployment, managed Traefik, private registry,
  and artifact signing are not implemented.
- The Git-source path is a legacy/manual development path during migration. It
  is not the production delivery target, is not removed by P02, and must not be
  described as deprecated runtime code until an implementation task migrates
  callers and removes or explicitly bounds it.
- P01 code is complete, but the clean control-plane VPS checkpoint was not run
  because no clean Ubuntu VPS was available. Its status is
  `DEFERRED / UNPROVEN`; it remains a blocker before production acceptance.
- No available evidence supports a production-readiness claim.

## 3. Architectural invariants

### 3.1 Production artifact identity

- Git commit SHA is source identity and provenance, not the runtime artifact.
- The authoritative production runtime identity is
  `registry/repository@sha256:<digest>`.
- Production deployment requests must contain a full immutable `sha256`
  digest. A mutable tag alone must be rejected.
- Human-readable tags such as `sha-0123456789ab`, `main-0123456789ab`, and
  `pr-123-0123456789ab` may coexist, but Agent deploys the digest.
- Cloud stores artifact metadata and routing state; it does not receive source
  archives, Docker build contexts, or image tarballs.

### 3.2 Repository and runtime ownership

The canonical routing relationship is:

```text
GitHub installation
-> GitHub repository ID
-> Opsi project
-> Opsi service
-> environment
-> runtime
-> eligible healthy Agent
```

Repository identity belongs to the service mapping, not to an Agent or VPS.
Agent is a replaceable runtime target. Cloud must not bind a repository directly
to an Agent ID or node identity.

### 3.3 Separate GitHub trust paths

The following trust paths are distinct and must not share semantics merely
because they all originate at GitHub:

1. **GitHub App user authorization:** the GitHub App Client ID and Client Secret
   support user login or linkage of a GitHub identity to Opsi. GitHub numeric
   user ID is the identity subject.
2. **GitHub App installation authorization:** Cloud uses GitHub App ID and the
   App private key to mint installation access tokens for installation or
   repository metadata and GitHub status/check operations.
3. **GitHub webhook verification:** Cloud verifies the per-App webhook secret,
   event identity, and delivery identity before accepting an event.
4. **GitHub Actions OIDC:** a workflow obtains a GitHub OIDC JWT. Cloud verifies
   at least `iss`, `aud`, `exp`, `nbf`, `repository_id`,
   `repository_owner_id`, `ref`, `sha`, `event_name`, `run_id`, `run_attempt`,
   `workflow`, and `job_workflow_ref`, plus replay and configured policy.
5. **OCI registry authentication:** GitHub runner push authority and Agent pull
   authority are registry credentials. They are not GitHub OAuth credentials.

OIDC does not replace the GitHub App installation token, does not replace the
OCI registry, and is not artifact storage.

### 3.4 Build and trust ownership

The repository owns build instructions:

- build context;
- Dockerfile;
- platform;
- test command;
- service identifier;
- optional deployment metadata.

Cloud owns trust and routing metadata:

- installation ID and repository ID;
- project/service mapping;
- allowed workflow, event, and refs;
- environment/runtime mapping;
- deployment policy;
- registry repository allowlist.

GitHub Actions submits a versioned `BuildRecord` containing:

- repository ID;
- commit SHA and ref;
- event name;
- run ID and run attempt;
- workflow identity;
- image repository and image digest;
- optional provenance digest.

Cloud must bind every security-relevant request-body field to verified OIDC
claims and configured mapping. Repository, SHA, ref, workflow, or event values
are never trusted merely because they appear in JSON.

### 3.5 Trusted CD policy versus ActionPlane

- `DeploymentPolicy` is a preconfigured trusted-CD policy owned by an
  appropriately authorized user.
- An allowed push, workflow, event, ref, repository, artifact repository, and
  environment may create a `DeploymentJob` without a new human approval for
  every deployment.
- Trusted CD is not an AI action and does not create an `ActionPlan` or
  `ApprovalGrant`.
- AI-originated mutation still requires the Safe ActionPlane, deterministic
  Agent policy, and separate human approval.
- AI must never approve a CD deployment or modify the deployment policy through
  an unapproved action.
- Automatic rollback is part of the already authorized deployment transaction;
  it does not create a new AI `ActionPlan` solely to recover a failed rollout.

### 3.6 Pull request policy

- A pull request from the same repository may build.
- Preview deployment occurs only when an explicit policy permits it.
- Fork pull requests fail closed by default.
- Untrusted fork code must never receive a write token or production secret.
- Preview environments are isolated and have an enforced TTL and cleanup path.
- Pull request approval is not production approval.
- Production accepts only allowlisted refs, events, workflows, repositories,
  artifact repositories, and environments.

### 3.7 Cloud data minimization

Cloud may store:

- `BuildRecord` metadata;
- repository ID and commit SHA;
- image digest;
- workflow and run identifiers;
- deployment result metadata;
- provenance references.

Cloud must not store:

- source repository contents;
- Docker build context;
- raw build logs;
- application secrets;
- registry password plaintext;
- kubeconfig;
- raw runtime logs.

## 4. Phase order and dependency rules

Phases execute in order unless a task explicitly states that work may proceed
without closing a manual checkpoint. Documentation P02 may proceed while the
P01 clean-control-plane checkpoint is deferred, but the deferred checkpoint
remains open and blocks production acceptance.

Dependency rules:

1. Do not claim a later capability from contracts, documentation, scaffolding,
   or command availability alone.
2. P03-P06 establish a releasable Agent and resumable, hardened bootstrap before
   GitHub-controlled production delivery depends on remote runtimes.
3. P07-P10 establish GitHub App identity, installation, webhook, repository, and
   service ownership before OIDC records can route production work.
4. P11-P13 establish OIDC verification and real runner proof before Cloud may
   trust a `BuildRecord`.
5. P14-P16 establish one digest-only artifact deployment path before runtime
   delivery and CD E2E tasks rely on it.
6. P17-P21 establish typed rendering, exposure, readiness, rollback, main-branch
   CD, and safe preview behavior before development acceptance.
7. P22-P25 implement the separate evidence, Safe ActionPlane, and MCP boundary;
   these tasks must not be used to authorize trusted CD.
8. P26 must pass twice before production hardening acceptance begins.
9. P27-P32 are mandatory production gates. No production-readiness claim is
   permitted before P32 passes with reviewed evidence.

## 5. Verification rules

- Every behavior change requires targeted tests, failure cases, static checks,
  and broader regression checks proportional to risk.
- Documentation-only tasks require link/reference checks, terminology checks,
  whitespace checks, full diff review, and proof that no runtime file changed.
- A manual, VPS, GitHub runner, MCP client, staging, or production-like
  checkpoint passes only with a redacted, reproducible evidence artifact tied
  to the tested revision and environment.
- A command path, test script, workflow file, or runbook is not evidence that a
  real-infrastructure checkpoint passed.
- Deferred checkpoints remain `DEFERRED / UNPROVEN`; they are not `PASS`,
  `DONE`, or implied evidence.
- Failed or skipped verification is reported with the exact missing dependency
  or condition.
- Production acceptance requires all code and documentation checks plus every
  mandatory real-infrastructure checkpoint listed below.

## 6. Ordered work P01-P32

### Phase A - Baseline

#### P01 - Development control-plane env baseline

Status: `CODE COMPLETE`

Clean control-plane VPS checkpoint: `DEFERRED / UNPROVEN`.

- Baseline implementation commit:
  `83d93704c25f3303f65a4a9c90b4037cff6c0aa9`.
- The development control-plane code/package work is retained as completed.
- No clean Ubuntu VPS evidence exists.
- This deferral does not block P02 documentation, but remains a blocker before
  production acceptance.

#### P02 - Roadmap v4 and Trusted Artifact CD ADR

Status: `DOC_ONLY` after the required documentation checks and commit.

- Establish this file as the canonical roadmap.
- Adopt `docs/architecture_decisions/ADR-004-trusted-artifact-cd.md`.
- Align active documentation without changing runtime code or claiming target
  capabilities are implemented.

### Phase B - Bootstrap foundation

#### P03 - Restore Agent executable and deterministic release artifact

Status: `CODE COMPLETE`.

VPS/installer proof: `UNPROVEN`.

- The Agent entrypoint composes the existing config loader and runtime without
  copying `agent/internal/server` behavior.
- The direct Linux amd64 binary embeds explicit version and full commit
  metadata and is accompanied by deterministic SHA-256 and JSON manifests.
- Local verification rebuilds with separate Go caches and compares the binary,
  checksum, and manifest byte-for-byte in the same toolchain.
- The artifact is not published or hosted over HTTPS. Bootstrap Worker was not
  changed or tested against it, and no VPS evidence was created.

#### P04 - Resumable BootstrapJob state machine

Status: `NEXT`.

- Persist per-step transitions.
- Resume idempotently.
- Handle checkpoints safely across lease changes.
- Retry after Worker or Cloud restart.

#### P05 - Bootstrap supply-chain and transport hardening

- Pin the K3s version.
- Verify installer and artifacts.
- Enforce SSH known-host verification.
- Require Agent-to-Cloud HTTPS.
- Make install and upgrade behavior idempotent.
- Establish the canonical versioned systemd install layout and integrate
  upgrade/rollback behavior.

#### P06 - Clean target VPS bootstrap proof

Checkpoint: `[VPS CHECKPOINT]`

Test on a real clean Ubuntu target:

- successful bootstrap;
- Worker restart;
- Cloud restart;
- target reboot;
- retry and resume;
- Agent heartbeat;
- known-host mismatch;
- invalid checksum;
- TLS failure.

### Phase C - GitHub App control plane

#### P07 - GitHub App user authorization

- Replace generic OAuth with GitHub App user authorization.
- Use GitHub numeric user ID as the identity subject.
- Enforce PKCE and state.
- Validate environment configuration.

#### P08 - GitHub App installation authentication and webhooks

- Configure App ID.
- Handle the App private key safely.
- Create installation JWTs and installation access tokens.
- Verify webhook signatures.
- Process installation and repository events.

#### P09 - Repository and service mapping

- Store installation identity.
- Store GitHub repository ID.
- Map project and service ownership.
- Enforce RBAC and ownership.
- Do not bind a repository to an Agent.

#### P10 - `opsi init` and repository bootstrap

- Create `.opsi/opsi-cd.yaml`.
- Create or propose `.github/workflows/opsi-cd.yaml`.
- Require user review before commit.
- Never silently modify a repository.

Checkpoint: `[GITHUB/VPS CHECKPOINT]` using a real GitHub App and public HTTPS.

### Phase D - Trusted build and artifact delivery

#### P11 - GitHub Actions OIDC verifier

- Verify JWKS, issuer, audience, expiry, and not-before.
- Enforce replay protection and claim policy.
- Enforce the configured workflow allowlist.

#### P12 - Build session and BuildRecord

- Define a versioned contract.
- Bind the request to verified OIDC identity.
- Enforce idempotency by repository, run, and attempt.
- Store digest and provenance metadata.
- Store no raw build logs.

#### P13 - GitHub-hosted runner and public GHCR proof

Checkpoint: `[GITHUB RUNNER CHECKPOINT]`

- Build a real image.
- Push a public GHCR image.
- Submit an OIDC-authenticated `BuildRecord`.
- Verify request body fields against JWT claims.

#### P14 - Digest-only deployment contract

- Add the image-source deployment contract.
- Require a full `sha256` image digest.
- Reject mutable-tag-only production requests.
- Preserve the legacy Git path only as explicitly non-production during
  migration.

#### P15 - Cloud deployment routing

- Route repository to service.
- Route service to environment and runtime.
- Route runtime to an eligible healthy Agent.
- Persist a durable `DeploymentJob`.
- Apply policy checks and idempotency.

#### P16 - Agent image artifact deployment

- Do not clone or build for an image-source job.
- Pull or resolve the digest.
- Verify repository and digest.
- Deploy the immutable artifact.
- Report a sanitized result.

### Phase E - Runtime delivery and CD

#### P17 - Opsi-rendered Deployment and Service

- Use typed workload input.
- Identify the named application container.
- Never wildcard-replace sidecars.
- Support resources and probes.
- Apply ownership labels.

#### P18 - ExposureSpec and Traefik

- Render a ClusterIP Service.
- Use typed hostname and path input.
- Detect conflicts.
- Own the generated Traefik resource.
- Do not allow arbitrary manifest mutation.

#### P19 - Readiness, reconciliation and rollback

- Observe rollout status.
- Track the last known good digest.
- Roll back automatically.
- Run post-checks.
- Reconcile after restart.

#### P20 - Main branch CD E2E

Checkpoint: `[VPS + GITHUB CHECKPOINT]`

Prove the real flow:

```text
push main
-> GitHub Actions
-> GHCR
-> OIDC BuildRecord
-> Cloud job
-> Agent
-> K3s rollout
```

#### P21 - Pull request preview deployment

Checkpoint: `[VPS + GITHUB CHECKPOINT]`

- Support same-repository pull requests.
- Fail closed for forks.
- Use an isolated preview namespace or environment.
- Enforce TTL cleanup.
- Expose no production credentials.
- Enforce trust and approval policy.

### Phase F - Safe operations and user-owned AI

#### P22 - IncidentEvidence v1

- Produce factual bounded evidence.
- Redact sensitive content.
- Hash the evidence.
- Include a timeline.
- Tag prompt-injection content.

Checkpoint: `[VPS CHECKPOINT]` using a real failing workload.

#### P23 - Action contracts and deterministic policy

- Define `ActionPlan`.
- Define `ActionPreflight`.
- Define `ApprovalChallenge`.
- Define `ApprovalGrant`.
- Define `ActionResult`.
- Keep deterministic risk ownership in Agent.

#### P24 - Typed executors and approval safety

- Lock targets.
- Prevent replay.
- Enforce expiry.
- Bind current-state hash.
- Run post-checks.
- Report rollback results.
- Audit decisions and outcomes.

Checkpoint: `[VPS CHECKPOINT]`

#### P25 - CLI MCP bridge

- Expose read tools.
- Expose preflight.
- Expose challenge request.
- Expose no execute tool.
- Expose no approve tool.
- Never output an `ApprovalGrant`.

Checkpoint: `[MCP CLIENT CHECKPOINT]` with at least two real MCP clients.

### Phase G - Development acceptance

#### P26 - Full development E2E twice

Checkpoint: `[VPS + GITHUB CHECKPOINT]`

Run the complete flow twice from a clean state or a documented reset state.

### Phase H - Production hardening

#### P27 - Production identity and secret hardening

- Define credential lifecycle.
- Prove key rotation.
- Harden GitHub private-key handling.
- Define registry pull credentials.
- Add step-up controls.

#### P28 - Tenant and runtime isolation

- Enforce namespace policy.
- Enforce network policy.
- Enforce resource quota.
- Isolate secrets.
- Enforce project boundaries.

#### P29 - Supply-chain verification

- Produce an SBOM.
- Sign artifacts.
- Produce build provenance.
- Verify digests.
- Enforce Agent verification policy.

#### P30 - Production ingress, TLS and private registry

Checkpoint: `[STAGING CHECKPOINT]` on a real staging environment.

#### P31 - PostgreSQL durability and upgrade safety

- Prove backup and restore.
- Prove migration safety.
- Prove upgrade and rollback.
- Define and measure recovery objectives.

#### P32 - Disaster recovery and production acceptance

- Run fault injection.
- Prove repeated recovery.
- Prove clean deploy.
- Prove upgrade and rollback.
- Prove disaster recovery.
- Produce final reviewed evidence.

Checkpoint: `[PRODUCTION-LIKE STAGING CHECKPOINT]`

Opsi must not be described as production-ready before P32 passes.

## 7. Manual checkpoint register

| Checkpoint | Task | Required environment | Current status |
|---|---|---|---|
| CP-VPS-1 | P01 clean development control-plane install and restart | Clean Ubuntu control-plane VPS | `DEFERRED / UNPROVEN` |
| CP-VPS-2 | P06 bootstrap and failure/recovery matrix | Clean Ubuntu target VPS | `NOT_RUN` |
| CP-GH-VPS-1 | P10 real GitHub App and public HTTPS | GitHub plus VPS | `NOT_RUN` |
| CP-GH-RUNNER-1 | P13 OIDC BuildRecord and public GHCR | GitHub-hosted runner | `NOT_RUN` |
| CP-CD-1 | P20 main branch digest deployment | GitHub plus VPS | `NOT_RUN` |
| CP-PR-1 | P21 preview policy and TTL | GitHub plus VPS | `NOT_RUN` |
| CP-EVIDENCE-1 | P22 real failing workload evidence | VPS | `NOT_RUN` |
| CP-ACTION-1 | P24 approved typed executor | VPS | `NOT_RUN` |
| CP-MCP-1 | P25 two real MCP clients | MCP clients | `NOT_RUN` |
| CP-DEV-1 | P26 complete development flow twice | GitHub plus VPS | `NOT_RUN` |
| CP-STAGE-1 | P30 ingress, TLS, private registry | Real staging | `NOT_RUN` |
| CP-PRODLIKE-1 | P32 recovery and acceptance | Production-like staging | `NOT_RUN` |

## 8. Deferred checkpoint ledger

### CP-VPS-1 - P01 clean control-plane VPS

- Status: `DEFERRED / UNPROVEN`.
- Reason: no clean Ubuntu VPS was available.
- Evidence: none; no pass artifact exists.
- Effect on P02: does not block the documentation-only task.
- Effect on production acceptance: remains an open blocker until the required
  clean install and independent restart proof is executed and reviewed.

No later document or task may reinterpret this ledger entry as a pass.

## 9. Production acceptance definition

Production acceptance requires all of the following:

1. P01-P32 have evidence-backed status appropriate to their scope.
2. CP-VPS-1 is no longer deferred and has reviewed clean-control-plane proof.
3. Every mandatory VPS, GitHub, runner, MCP client, staging, and
   production-like checkpoint has passed with redacted reproducible evidence.
4. Production delivery uses an immutable OCI image digest routed through a
   verified OIDC-bound `BuildRecord`, configured `DeploymentPolicy`, durable
   `DeploymentJob`, and eligible healthy Agent.
5. No production path treats a Git SHA, mutable tag, repository body field,
   Agent ID binding, GitHub OAuth credential, or GitHub App installation token
   as the authoritative runtime artifact or long-lived registry pull identity.
6. PR preview security, rollback, restart reconciliation, tenant isolation,
   secret lifecycle, supply-chain verification, PostgreSQL recovery, upgrade,
   rollback, and disaster recovery have passed their ordered gates.
7. The user-owned AI/MCP boundary remains separate from trusted CD policy and
   no AI system can approve or directly execute a deployment.

Until these conditions and P32 are satisfied, production readiness remains
unproven.
