# Opsi Enforced Repair Rules

Load this file before implementing or reviewing Opsi repair work.

## Prime Directive

Fix architecture drift before adding features.

Opsi is local-first:

```text
Browser UI -> CLI local backend -> Agent gRPC -> local runtime
```

Cloud is identity, membership, registration, relay, OTP, notification, and deployment job envelopes. Cloud is neither the runtime control plane nor an AI runtime/provider.

Roadmap v3 assigns future user-owned AI integration to a CLI-side bridge. That MCP bridge is not implemented yet.

Agent no longer contains an HTTP AI analyzer, fallback RCA, provider/model metadata, AI network call, or incident-owned Kubernetes mutation executor. The active incident contract exposes only list/get/resolve and preserves deterministic sanitized context, authorization and audit. Analyze/approve RPCs and user-facing surfaces are removed; historical RCA/mitigation columns remain storage-only compatibility data.

IncidentEvidence v1, Safe ActionPlane, human approval grants, typed action
executors, and managed gateway rendering are not implemented. Future AI cannot
connect directly to Agent; MCP has no execute/approve tool; approval occurs in a
separate trusted human channel; Agent deterministic policy remains authoritative.

## Hard Rules

1. Browser production workflows must not call Cloud directly.
2. Browser must never receive or store long-lived PATs.
3. Agent owns deployment execution, secrets, telemetry, incidents, runtime audit, and local sync buffer.
4. Cloud must not store raw logs, raw metrics, app secrets, kubeconfig, Docker layers, source code, or long-lived runtime payloads.
5. Deployment jobs must include complete versioned service-specific `DeploymentIntent`.
6. Do not queue image-source deployment unless Agent supports image deployment.
7. Future user-owned AI output is advisory and cannot authorize execution; the CLI-side MCP bridge is not implemented yet.
8. No fake-success UI. Disabled actions must be honest and explain the missing backend capability.
9. Build/test must work from clean source; checked-in binaries cannot prove correctness.
10. Docs must reflect implemented state, not desired state.
11. Source packages must come from Git-aware tracked plus untracked/non-ignored files, and release output must be recreated before approved artifacts are copied.
12. Local config, credentials, private-key material, runtime certificate directories, databases, logs, and generated output must not enter source or release artifacts.

## Ordered Work

- V3-008 is the M0 documentation gate after the Phase 1 deletions.
- After M0 review, Phase 2 starts at V3-009 with the Bootstrap Worker poll/lease
  daemon. Do not claim V3-009 is in progress before that review.
- IncidentEvidence, Safe ActionPlane, CLI MCP, and production acceptance remain
  later ordered phases. Do not skip ahead.

## Required Files To Read

Before repair work, read:

- `docs/opsi_srs.md`
- `docs/architecture.md`
- `docs/current_state.md`
- `docs/status_matrix.md`
- `docs/opsi_roadmap_v3/12_EXECUTION_BACKLOG.md`
- the relevant roadmap v3 phase document;
- `docs/architecture_decisions/ADR-003-user-owned-ai-action-boundary.md`
- `.agents/current.md`

## Review Checklist

Reject the change if any answer is `yes`:

- Did browser code import or instantiate a Cloud client for core workflows?
- Did Cloud gain ownership of runtime execution or raw operational data?
- Can a user queue a deployment that Agent is known to reject?
- Does a UI action report success without backend execution?
- Does any response expose PAT/OTP/TOTP secret/app secret/kubeconfig/raw log?
- Does Agent execute free-form AI advice?
- Does the change require a checked-in binary or generated output to pass?
- Did behavior change without `docs/current_state.md` or `docs/status_matrix.md` updates?

## Grep Gates

Run these before completion:

```bash
rg -n "CloudRegistryClient|registry-client|NEXT_PUBLIC_CLOUD|cloudURL|127\.0\.0\.1:9800|localhost:9800" cli/ui
rg -n "image source deploy is not supported|IMAGE_DEPLOY_NOT_SUPPORTED|source_type.*image|source.*image" agent cloud cli contracts docs
rg -n "raw logs|raw metrics|kubeconfig|app secret|private key|TOTP secret|OTP code|AI provider API key" cloud agent cli docs .agents
find . -type f \( -path './bin/*' -o -path './release/*' -o -name 'opsi-agent' -o -name 'opsi-cloud' -o -name 'opsi' -o -name '*.db' -o -name 'tsconfig.tsbuildinfo' \) -print
```

Explain every allowed match in the PR/change summary.
