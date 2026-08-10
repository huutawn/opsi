# Opsi UI Rewrite Inventory

This inventory is the boundary for the future visual rewrite. `screen.png` is the visual authority. `code.html` is structure and style reference only.

## Reference Mapping

| Current production owner | Future visual reference | Functional ownership to preserve |
| --- | --- | --- |
| `components/layout/app-shell.tsx`, `components/layout/context-header.tsx`, `components/navigation/*` | Shared shell visible in every reference | Project selection, environment context, breadcrumb, session/source state, URL navigation, focus management |
| `features/infrastructure/infrastructure-view.tsx` `TopologyTab` + `features/infrastructure/topology-canvas.tsx` | `topology_workspace_opsi_dashboard_1`, `topology_workspace_opsi_dashboard_2` | React Flow, `CanvasDraft`, selection URL, placement drag/drop, connections, configuration and topology review/apply |
| `features/infrastructure/infrastructure-view.tsx` `LiveTopology` and `LiveDeploymentBoard` | `topology_workspace_live_mode_opsi_dashboard_corrected` | Factual-only runtime/deployment/exposure state, refresh recovery, rollback eligibility/action |
| `features/infrastructure/infrastructure-view.tsx` `RuntimesTab`, `NodesTab`, `BootstrapTab` | `infrastructure_opsi_dashboard` | Runtime/node/Agent facts, capacity, heartbeat, diagnostics, events, node actions |
| `features/services/services-view.tsx` | `services_catalog_opsi_dashboard` | Factual catalog/detail, health/source semantics, selected-service URL state |
| `features/applications/application-wizard.tsx` | `add_application_opsi_dashboard` | One canonical wizard, GitHub installation/repository/claim/binding, partial binding recovery, Unplaced result |
| `features/delivery/*` and topology `DeploymentReview` | `delivery_deployment_opsi_dashboard` | BuildRecord authority, immutable digest, workload-before-Exposure, recovery, failure, exact rollback |
| `features/observability/*` | `observability_opsi_dashboard` | Shared factual source model, health/metrics/logs/incidents, unavailable and unknown states |
| `features/security/security-view.tsx` | `security_opsi_dashboard` | Explicit secret operations, second factor, bounded reveal TTL, redacted audit evidence |

The non-corrected Live reference is not an implementation target.

## Current UI Inventory

### Shell and navigation

- Entry: `app/page.tsx` -> `components/layout/app-shell.tsx`.
- Route authority: `features/console/navigation.ts`, `features/console/router-map.tsx`, `features/console/console-router.tsx`.
- Context: `ContextHeader`, `Sidebar`, `ProjectSwitcher`, `Tabs`, `ConnectionPopover`.
- Global mutation review and authentication surfaces remain in `AppShell`.

### Topology and infrastructure

- `useInfrastructureData` loads placement facts, topology, policies, GitHub inventory, and builds.
- `TopologyDesignCanvas` owns React Flow presentation and currently co-locates topology/configuration review handlers.
- `InfrastructureView` owns Design/Live switching, server onboarding, bootstrap lifecycle, runtime/node views, and dialog visibility.
- `DeploymentReview` owns multi-service BuildRecord selection and workload/Exposure phase ordering.
- `PlacementDialog` owns reviewed placement and deployment-policy/topology apply ordering.

### Applications and services

- `ServicesView` owns catalog filtering and the factual service drawer.
- `ApplicationWizard` is already the single implementation opened from Services and Topology.
- GitHub repository CD presentation and mutation flows live in `features/github/repository-cd.tsx`.

### Delivery, observability, and security

- Delivery has one route owner with Pipeline, Builds, Deployments, Exposure, and Source tabs.
- Delivery polling is isolated in `features/delivery/polling.ts` and `polling-model.ts`.
- Observability tabs share `useObservabilityData`; no tab owns a separate factual source.
- Security has one route owner for Secrets and Audit. Protected values are removed on TTL, navigation, project change, and auth loss.

### Dialogs, drawers, and inspectors

- Global mutation review: `MutationDialog`.
- Create project: `ProjectsView` native dialog.
- Add Application: `ApplicationWizard` native dialog.
- Connect Server: `BootstrapDialog`.
- Plan placement: `PlacementDialog`.
- Configure Exposure: `ExposureConfigure` dialog.
- Service detail: `ServiceDetail` drawer.
- Protected secret/TOTP result: `ProtectedResult` dialog.
- Topology resource/connection inspector: `TopologyInspector` and `ConnectionInspector` asides.

## Replacement Classification

### Presentation that may be replaced or deleted

- `app/globals.css` legacy visual rules after each screen is migrated.
- JSX layout/chrome in `components/layout`, `components/navigation`, and `components/ui/primitives.tsx`.
- Page shells, cards, tables, tabs, node markup, inspectors, dialogs, drawers, and visual helpers inside feature components.
- Visual-only Playwright selectors and screenshots after equivalent functional assertions are retained.

### Functional and state logic that must remain authoritative

- `hooks/use-console-state.ts` and `hooks/console-state-support.ts`.
- `features/infrastructure/data.ts`, `features/delivery/data.ts`, `features/observability/data.ts`.
- `lib/presentation/project.ts`, `lib/presentation/infrastructure/model.ts`, `lib/presentation/delivery/model.ts`.
- `features/delivery/polling.ts` and `polling-model.ts`.
- React Flow movement/connection behavior and `CanvasDraft` compilation in `topology-canvas.tsx` and the infrastructure model.
- Application source-claim/binding recovery handlers currently co-located in `application-wizard.tsx`.

### API and contracts that must remain unchanged

- `lib/api/local-client.ts`.
- `lib/contracts/registry.ts`.
- Local API request/response semantics, Cloud/Agent authority, Go contracts, and generated contract references.

### Review, idempotency, conflict, and recovery logic that must remain

- Global mutation review idempotency key lifecycle in `useConsoleState`.
- Topology preview/validate/diff/apply and revision/state-hash conflict handling.
- Service configuration preview/validate/diff/apply and stale review invalidation.
- Deployment review fingerprints, independent submission keys, retry-only-missing phases, and workload-before-Exposure ordering.
- Bootstrap command default, event progress, retry, refresh recovery, and credential clearing.
- Delivery polling, deployment refresh restoration, rollback exact known-good, and sensitive-value cleanup.
- URL parsing, deep-link restoration, browser history, project/environment isolation, and stale request suppression.

## Playwright Coverage Boundary

- `foundation.spec.ts`: workspace/project navigation, context restoration, accessibility, Connect Server, refresh and credential cleanup.
- `application.spec.ts`: installation, repository conflict/claim, application creation, partial binding recovery.
- `fe03.spec.ts`: Design/Live, draft editing/reset/apply/conflict, connections, onboarding, bootstrap polling, Observability facts.
- `deployment-review.spec.ts`: multi-service review, stale authority, workload/Exposure ordering, failure, rollback recovery.
- `delivery.spec.ts`: Delivery URL state, pipeline facts, failure/rollback/exposure/source, responsive overflow.
- `fe04.spec.ts`: Secrets, protected values, Audit, Settings, unavailable factual states.
- `manual-parity.spec.ts`: Local backend parity boundary.

These tests are functional assets. Future visual work may update selectors and screenshots but must not weaken the asserted behavior.

## P01 Foundation Boundary

P01 adds shared tokens and primitive styling only. It does not change route ownership, page composition, React Flow behavior, workflows, or API/state semantics.
