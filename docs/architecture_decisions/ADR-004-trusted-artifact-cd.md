# ADR-004: Trusted OCI Artifact Delivery with GitHub App and Actions OIDC

## Status

Accepted; original decision history retained below

## Date

2026-07-13

## Context

When this decision was accepted, Opsi supported a Git-source deployment path in
which Agent cloned source, built an image through containerd or Docker, and
applied repository-provided runtime manifests. Image-source deployment was
rejected. This historical state motivated the migration; the 2026-07-23
implementation addendum records its replacement.

That former path is not an acceptable canonical production CD design. It makes
the runtime node responsible for source retrieval and builds, treats a Git
revision as if it were the runtime artifact, complicates deterministic rebuilds,
and couples repository access to a replaceable Agent. It also lacks a clear
trust boundary between GitHub user identity, GitHub App installation authority,
webhook events, GitHub Actions workload identity, and registry credentials.

Opsi needs a production target in which GitHub Actions performs build and test,
an OCI registry stores the immutable artifact, Cloud verifies build identity and
routing policy, and Agent deploys the artifact by digest. Trusted CD must remain
separate from the user-owned AI Safe ActionPlane described by ADR-003.

The following capabilities are not implemented at the time of this decision:

- GitHub App user authorization;
- GitHub App installation token creation;
- GitHub Actions OIDC verification;
- repository-to-service mapping for trusted delivery;
- `BuildRecord` ingestion;
- digest-based image deployment;
- trusted-CD `DeploymentPolicy`;
- pull request preview environments;
- managed Traefik delivery;
- private registry support and artifact signing.

## Decision

### Production delivery identity

The production delivery flow is:

```text
GitHub Actions build/test
-> OCI registry push
-> image@sha256:<digest>
-> Opsi Cloud BuildRecord
-> DeploymentPolicy and routing
-> durable DeploymentJob
-> eligible healthy Agent
-> digest pull and K3s/containerd deployment
```

Git commit SHA is source identity and provenance. It is not the authoritative
runtime artifact. The production runtime identity is the complete immutable OCI
reference:

```text
registry/repository@sha256:<digest>
```

Mutable tags may exist for human navigation, including
`sha-0123456789ab`, `main-0123456789ab`, or `pr-123-0123456789ab`, but Agent
must deploy the digest. A mutable-tag-only production request fails closed.

### Repository ownership and routing

Repositories are owned by Opsi services, not by nodes. The canonical mapping is:

```text
GitHub installation
-> GitHub repository ID
-> Opsi project
-> Opsi service
-> environment
-> runtime
-> eligible healthy Agent
```

Agent is a replaceable runtime target. Cloud must not bind a repository directly
to an Agent ID or VPS identity. Routing chooses an Agent only after repository,
service, environment, runtime, policy, and health checks succeed.

### Build and policy ownership

The repository owns build instructions:

- build context;
- Dockerfile;
- platform;
- test command;
- service identifier;
- optional deployment metadata.

Cloud owns trust and routing metadata:

- GitHub installation ID;
- GitHub repository ID;
- Opsi project/service mapping;
- allowed workflow identity;
- allowed events and refs;
- environment/runtime mapping;
- `DeploymentPolicy`;
- OCI registry repository allowlist.

GitHub Actions submits a versioned `BuildRecord` containing:

- `repository_id`;
- `commit_sha`;
- `ref`;
- `event_name`;
- `run_id`;
- `run_attempt`;
- workflow identity;
- image repository;
- image digest;
- optional provenance digest.

Cloud must compare security-relevant request-body fields with verified OIDC
claims and configured trust metadata. It must never trust repository, SHA, ref,
event, or workflow merely because a JSON body contains the value.

### Trusted CD and ActionPlane separation

`DeploymentPolicy` is configured in advance by a user with the required Opsi
authorization. An allowed push or other allowed event from an allowed ref and
workflow may produce a `DeploymentJob` without a separate human approval for
every run.

Trusted CD is not an AI action. It does not create an AI `ActionPlan`,
`ApprovalChallenge`, or `ApprovalGrant`. AI-originated mutations still use the
Safe ActionPlane, deterministic Agent policy, and a separate human approval
channel. AI must not approve a CD deployment.

Automatic rollback is part of the deployment transaction authorized by
`DeploymentPolicy`. A failed deployment does not require a new AI `ActionPlan`
solely to restore the last known good digest.

### Pull request policy

- Same-repository pull requests may build.
- Preview deployment occurs only when an explicit policy permits it.
- Fork pull requests fail closed by default.
- Untrusted fork code never receives a write token or production secret.
- Preview environments are isolated, have TTL, and are cleaned up
  deterministically.
- Pull request approval is not production approval.
- Production accepts only configured repository, workflow, event, ref,
  artifact-repository, environment, and runtime combinations.

### Cloud data minimization

Cloud may store `BuildRecord` metadata, repository ID, commit SHA, image digest,
workflow/run identifiers, deployment result metadata, and provenance
references.

Cloud must not store source repository contents, Docker build context, raw build
logs, application secrets, registry password plaintext, kubeconfig, or raw
runtime logs.

## Detailed flow

### 1. GitHub App user authorization

Opsi uses the GitHub App Client ID and Client Secret for user login or for
linking a GitHub identity to an Opsi identity. The GitHub numeric user ID is the
external identity subject. The flow requires state and PKCE and is configured
per environment.

This user authorization proves an interactive user's GitHub identity. It does
not authorize repository installation API calls, verify webhooks, identify a
GitHub Actions job, or grant OCI registry access.

### 2. GitHub App installation authorization

Cloud uses the GitHub App ID and App private key to create a short-lived App JWT
and exchange it for an installation access token. Cloud uses that token only
when it needs installation or repository metadata or must create a GitHub
status/check allowed by policy.

Installation access tokens remain separate from user authorization and OIDC.
They are not long-lived Agent registry pull credentials.

### 3. GitHub webhook verification

Cloud verifies the webhook signature with the per-App webhook secret before
processing the event. It records and validates event name and delivery identity
for deduplication, replay handling, and audit. A verified webhook may update
installation or repository metadata or trigger policy evaluation, but it is not
proof that an artifact was built by an authorized workflow.

### 4. GitHub Actions build and registry push

The repository workflow checks out the intended source, runs configured tests,
builds the OCI image from repository-owned instructions, and pushes it to an
allowed OCI repository using runner-scoped registry push authority. The registry
returns or resolves the immutable image digest.

The runner may add readable tags, but all downstream Opsi records and runtime
jobs use the digest. The workflow does not send source or an image tar to Cloud.

### 5. GitHub Actions OIDC authentication

The workflow requests an OIDC JWT from GitHub and sends it to the Cloud
`BuildRecord` boundary. Cloud retrieves and caches GitHub JWKS with bounded
refresh, validates the signature, and verifies at least:

```text
iss
aud
exp
nbf
repository_id
repository_owner_id
ref
sha
event_name
run_id
run_attempt
workflow
job_workflow_ref
```

Cloud also applies replay protection, idempotency, repository mapping, workflow
allowlist, event/ref policy, environment policy, and registry repository
allowlist.

OIDC proves the workflow identity and selected run claims. It does not replace
the GitHub App installation token, does not authorize registry pull/push by
itself, does not replace the registry, and does not store the artifact.

### 6. BuildRecord verification

Cloud accepts a versioned `BuildRecord` only when:

1. the OIDC token is valid and not replayed;
2. `repository_id`, `commit_sha`, `ref`, `event_name`, `run_id`, `run_attempt`,
   and workflow identity match the verified claims;
3. the repository maps through its installation to the target Opsi service;
4. workflow, event, ref, environment, runtime, and artifact repository satisfy
   the configured `DeploymentPolicy`;
5. the image repository is allowlisted and the image digest is a complete valid
   `sha256` digest;
6. the idempotency key for repository/run/attempt is new or resolves to the
   identical prior record.

Cloud stores metadata and provenance references, not raw logs or build input.

### 7. DeploymentJob routing

For an allowed delivery, Cloud creates or reuses a durable idempotent
`DeploymentJob`. Routing follows repository -> service -> environment/runtime
and selects an eligible healthy Agent. The job carries the immutable image
repository and digest, typed workload/deployment input, and policy/routing
identity required for Agent validation. It does not carry Git source for the
image-source path.

### 8. Agent pull and deployment

Agent authenticates to the OCI registry with scoped pull authority appropriate
to the allowed repository. Agent verifies the repository/digest contract, pulls
or resolves the immutable digest, and deploys that digest through the typed
Opsi-rendered runtime path. It reports a bounded sanitized result to Cloud.

Agent must not clone or build source when processing an image-source job. The
former Git-source implementation was removed after migration updated every
caller and established one authoritative production path.

### 9. Rollout and rollback

Agent observes readiness, records the last known good digest, performs bounded
post-checks, and reconciles after restart. If the new rollout fails, automatic
rollback deploys the already trusted last known good digest within the same
authorized deployment transaction and reports the rollback outcome.

## Trust boundaries

| Boundary | Authentication or verification | Authority granted | Explicitly not granted |
|---|---|---|---|
| User <-> GitHub App authorization | App Client ID/Secret, state, PKCE, GitHub callback | Login/link GitHub numeric user identity | Installation API, webhook trust, Actions identity, registry access |
| Cloud <-> GitHub App installation API | App ID/private key -> App JWT -> short-lived installation token | Installation/repository metadata and configured status/check actions | User login, Actions build identity, Agent registry pull |
| GitHub webhook <-> Cloud | Per-App HMAC signature, event and delivery identity | Authentic event delivery for mapped App | Artifact integrity, workflow identity, registry authority |
| GitHub Actions <-> Cloud | GitHub OIDC JWT, JWKS, claims, replay/idempotency policy | Submit claim-bound `BuildRecord` for configured workflow/run | Installation API token, artifact storage, registry access |
| GitHub Actions <-> OCI registry | Scoped runner push credential or registry federation | Push artifact to allowed repository | Opsi user identity, Agent pull outside scope |
| Cloud policy/routing <-> Agent | Authenticated canonical PollJob contract | Deliver authorized immutable deployment job | Source archive transfer, raw build log transfer, AI approval |
| Agent <-> OCI registry | Scoped pull credential | Pull allowed repository/digest | GitHub user OAuth, broad registry administration |
| Agent <-> K3s/containerd | Agent-owned typed execution and resource identity | Deploy/reconcile/rollback allowed digest | Arbitrary AI command or mutable-tag production identity |
| AI/MCP <-> Safe ActionPlane | Redacted typed evidence, deterministic preflight, separate human grant | Approved typed AI-originated operation | Trusted CD approval or policy bypass |

Registry authentication is its own trust boundary. Runner push authority and
Agent pull authority have different principals, scopes, lifecycles, and storage
requirements. Registry credentials must never be treated as GitHub OAuth
credentials or stored as plaintext Cloud metadata.

## Consequences

- Production runtime identity becomes immutable and independently auditable.
- Build load and source credentials move out of Agent and into GitHub Actions.
- Cloud gains explicit `BuildRecord`, policy, mapping, and routing
  responsibilities but remains outside artifact storage and source/build-log
  storage.
- Agent gains a digest-only image deployment path and scoped registry pull
  responsibility.
- Repository ownership remains stable when an Agent or VPS is replaced.
- GitHub trust paths require separate credentials and validation logic; one
  successful path cannot authorize another.
- Production deployments can remain automatic after authorized policy setup,
  while AI-originated changes retain mandatory separate human approval.
- Same-repository previews can be supported safely, while fork pull requests
  fail closed unless a later, explicitly reviewed design creates an isolated
  no-secret build boundary.
- The original migration plan allowed temporary Git-source coexistence until
  every caller moved to the artifact path.

## Rejected alternatives

1. **Agent clone/build as the primary production CD path.** Rejected because it
   couples source and build credentials to runtime nodes, makes Agent
   replacement harder, and confuses source identity with artifact identity.
2. **Cloud receives a source tar or Docker image tar.** Rejected because Cloud
   must not become a source/build-context store or an artifact transport and
   scanning bottleneck; OCI registry protocols already own artifact storage and
   transfer.
3. **Deploy a mutable tag such as `latest`.** Rejected because a tag can move,
   cannot uniquely identify the reviewed runtime artifact, and makes rollback
   and audit nondeterministic.
4. **Bind a repository directly to Agent ID.** Rejected because repositories
   belong to services and Agents are replaceable runtime targets selected by
   environment, runtime, eligibility, and health.
5. **Use OIDC instead of a registry.** Rejected because OIDC authenticates a
   workload; it neither stores nor transports OCI artifacts and does not provide
   Agent pull semantics.
6. **Use a GitHub App installation token as the Agent's long-lived pull
   credential.** Rejected because installation authority and registry pull
   authority are separate trust domains, and a broad GitHub token is not an
   acceptable durable runtime secret.
7. **Require human approval for every trusted branch deployment.** Rejected
   because preconfigured `DeploymentPolicy` is the authorization boundary for
   routine trusted CD; per-run approval would conflate CD with manual or AI
   actions and prevent intended continuous delivery.
8. **Allow AI to approve a CD deployment.** Rejected because AI is untrusted
   advisory input, the AI request channel is not an approval channel, and trusted
   CD policy can be changed only through appropriately authorized Opsi control
   paths.

## Migration

1. P02 adopts this target without runtime changes.
2. P07-P10 replace generic GitHub user OAuth with GitHub App user authorization,
   add installation/webhook trust, and create repository/service mapping.
3. P11-P13 implement OIDC verification, `BuildRecord`, idempotency, and a real
   GitHub-hosted runner/GHCR proof.
4. P14-P16 add the digest-only deployment contract, Cloud routing, and Agent
   image artifact deployment.
5. P17-P21 add Opsi-owned workload rendering, exposure, readiness, rollback,
   main-branch CD, and preview policy.
6. The migration temporarily retained the Git-source development path until a
   separate implementation task updated every caller, test, contract, and
   document and removed it.
7. P27-P32 harden credentials, tenancy, supply chain, private registry,
   durability, upgrade, rollback, and disaster recovery before production
   acceptance.

No migration step may describe GitHub App, installation tokens, OIDC,
`BuildRecord`, digest deployment, `DeploymentPolicy`, previews, managed
Traefik, private registry, or signing as implemented before evidence exists.

## Verification

P02 verification was documentation-only:

- active documents reference `docs/opsi_roadmap_v5_production.md` and this ADR;
- active documents contain no reference to the absent roadmap v3 directory;
- current-state documents described the then-current Git clone/build behavior
  and image-source rejection;
- target documents identify the OCI image digest as the production runtime
  artifact and mutable tags as non-authoritative;
- GitHub App user authorization, installation authorization, webhook
  verification, GitHub Actions OIDC, and OCI registry authentication are
  documented as distinct trust paths;
- `DeploymentPolicy` and AI/manual ActionPlane approval remain separate;
- CP-VPS-1 remains `DEFERRED / UNPROVEN` with no fabricated evidence;
- `git diff --check` passes;
- the final diff contains only the approved Markdown paths.

Implementation verification is ordered by roadmap P07-P32 and requires targeted
tests plus the specified GitHub runner, VPS, staging, and production-like
checkpoints. This accepted decision is not implementation evidence.

## Implementation addendum — 2026-07-23

R5-005 through R5-011-S2 implemented the trusted path described by this ADR.
The one executable delivery path is:

```text
GitHub Actions OIDC
-> accepted BuildRecord
-> immutable OCI digest
-> TopologyPlan + DeploymentPolicy + routing
-> durable DeploymentJob/RolloutIntent
-> Agent PollJob
-> ProductionAdapter/ReconcileRollout
-> Opsi-owned K3s resources
-> factual readiness/known-good rollback
```

The Agent Git clone/build and arbitrary manifest application path, direct Agent
deployment RPC, service-scoped deployment creation, and generic GitHub push
relay are retired. The transport route retains the historical
`/webhooks/next` name, but `PollJob` carries only canonical deployment or node
lifecycle jobs; it is not a generic webhook relay. GitHub App authorization,
installation/repository ownership, Actions OIDC verification, BuildRecord
admission, topology/policy routing, and immutable deployment are implemented
checkpoints rather than future architecture.

This addendum records implementation state without changing the original
decision rationale. Full K3s acceptance remains an operator-run local workflow;
R5-011 is `PARTIAL`, R5-011.4 is `MANUAL_GATED`, and R5-012/MCP/AI/DNS/TLS/public
endpoint acceptance is not claimed.
