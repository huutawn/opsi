# Opsi Dashboard capability inventory

The project workspace has one operational deployment path: `features/deploy`.
It owns repository selection, bounded analysis, target bootstrap/selection, plan
review, approval, execution timeline, warning acknowledgement, recovery actions,
and the read-only result projection.

## Routed capabilities

- Deploy: the only repository-to-running mutation workflow.
- Observability: factual health, telemetry, logs, incidents, applications,
  servers, and managed-resource status.
- Security: audit, identities, access, and redacted secret metadata.
- Settings: local Dashboard and integration configuration.

Legacy Services, Infrastructure, Topology, and Delivery URLs normalize to Deploy.
Their former mutation components, polling model, repository editor, application
wizard, and Playwright suites have been removed. Backend registry, topology,
build, deployment, resource, verification, and log APIs remain canonical
authorities used by the deployment controller and read-only projections.

## Deploy ownership

- `deploy-view.tsx`: six-stage orchestration and single primary action.
- `plan-review.tsx`: the editable v2 plan, evidence, secret references, and
  blocking issue remediation.
- `target-bootstrap.tsx`: target bootstrap in the Deploy capability.
- `deployment-polling.ts`: reconnectable factual run polling only.
- `run-timeline.tsx`: event state and factual recovery actions.

Secret plaintext is transient input state only. The UI clears it before the
request is awaited; rendered state and the deployment plan contain metadata,
opaque reference, and revision only.
