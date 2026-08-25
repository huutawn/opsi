# Application Dependency Contract

## Architecture Decision

We chose **Option A: Extend the existing ServiceConfiguration authority**.

### Reason
The existing `ServiceConfiguration` authority already owns runtime configuration (`Environment`, `Bindings`, `PublicRoute`, `ResourceBindings`), has robust revision/review/apply semantics, and is correctly scoped to the consumer Application (Project + Service + Environment). 
Extending it follows the preferred rule to NOT create competing configuration authorities. Application dependencies are conceptually part of the service's configuration. Dependency target references are factually validated against canonical target authority during Review and revalidated at Apply where necessary.

## Model
The canonical model supports:
- Consumer: ServiceRecord (Project/Environment scoped)
- Target Kind: `application` or `managed_resource`
- Injection Phase: `runtime` or `build`
- Injection Mappings: strict symbolic mappings with an optional safe connection template.

## ADC-02: Managed Resource Dependency Realization (PostgreSQL + Valkey)

ADC-02 bridges declared application dependency intent with runtime environment and secret realization.

### Realization Pipeline
1. **Dependency Intent**: An Application declares a `managed_resource` dependency (`postgres`, `redis`, or `nats`) with symbolic injection mappings. Atomic sources include `resource.host`, `resource.port`, `credential.username`, `credential.password`, and `credential.database`. Compound sources are dialect-specific: `connection.postgres.uri`, `connection.postgres.npgsql`, `connection.postgres.jdbc`, `connection.postgres.pdo_dsn`, `connection.redis.uri`, `connection.redis.stackexchange`, and `connection.nats.uri`.
2. **Review / Apply**:
   - `POST /api/projects/{project_id}/services/{service_id}/dependencies/review`: Zero-mutation plan inspecting target readiness, identifying existing bindings for reuse or planned creation, and projecting environment descriptors without credential leakage.
   - `POST /api/projects/{project_id}/services/{service_id}/dependencies/apply`: Idempotently creates or reuses canonical `ResourceBinding` via `resource.Service`, linking `ResourceBindings` into the `ServiceConfiguration`.
3. **Symbolic Source Resolution**:
   - `resource.host`, `resource.port`, `credential.database` project into non-secret `deploymentv1.EnvironmentVariable`.
   - `credential.username`, `credential.password`, and compound sources containing credentials project into `deploymentv1.SecretReference` referencing the binding credential.
   - `connection.template` accepts at most 1 KiB of literals and whitelisted placeholders. Credential placeholders require `url_userinfo`, `url_query`, or `kv_quote`; expressions, environment expansion, command substitution, and literal credentials are rejected.
4. **Workload Secret Delivery & Injection**:
   - The deployment compiler resolves secret materials via `ResolveSecretMaterials` without leaking credentials into `WorkloadSpec` or diffs.
   - The Agent materializes Kubernetes Secrets (`opsi-<serviceKey>-<runtimeID>-binding-<secretID>`) with keys matching the mapped environment variable names.
   - The workload Pod receives values via `valueFrom.secretKeyRef`.
5. **Safety Invariants**:
   - Active binding target replacement requires explicit migration (`DEPENDENCY_BINDING_REPLACEMENT_REQUIRES_EXPLICIT_MIGRATION`).
   - Managed resource build-phase injection is explicitly rejected (`DEPENDENCY_BUILD_PHASE_UNSUPPORTED`).
   - Platform reserved environment variables (`PORT`, `HOSTNAME`, `OPSI_*`, `KUBERNETES_*`) and manual/generated conflicts fail closed with `DEPENDENCY_ENV_CONFLICT`.
   - Modifying dependencies creates a new `ServiceConfiguration` revision and compiles a new `DeploymentJob` using the immutable `BuildRecord` (zero image rebuilds).

### Compatibility

`connection.url` and `resource.<name>.connection_string` remain accepted for immutable and existing configurations with their historical URI semantics. New analysis and export use the protocol-specific source names above, and review reports the legacy aliases as deprecated. The source and optional template are part of the canonical configuration hash, so editing either invalidates prior approval.

## ADC-03: App→App HTTP Networking & Direct Resolution

ADC-03 provides canonical App→App dependencies across three distinct communication patterns:
1. `same_origin`: Browser-accessible frontend to backend API proxying under a unified origin.
2. `internal_http`: Server-to-server HTTP communication via internal cluster DNS (`http://<target-service>.<namespace>.svc.cluster.local:<port>`).
3. `public_http`: Publicly routed external endpoint referencing target service's public route.

## ADC-04: Unified Deployment Dependency Preflight (Deterministic Safety Gate)

ADC-04 introduces ONE unified, deterministic deployment safety gate executed during Review and strictly enforced at Apply:

### Result Levels
- `PASS`: All preflight checks passed with zero BLOCKers and zero WARNs.
- `PASS_WITH_WARNINGS`: Zero BLOCKers and one or more WARNs (e.g. optional dependency unavailable). Requires exact warning ID acknowledgements (`WarningAcknowledgements: ["chk:..."]`) to proceed to Apply.
- `BLOCKED`: One or more BLOCKers present (e.g. missing build record, stale build, offline server, unready required resource, missing binding, target drift, route conflict). Cannot be acknowledged; fail-closed.

### Preflight Checks & Actionable Remediations
- `BUILD_RECORD_MISSING` / `BUILD_RECORD_NOT_ACCEPTED` -> `CREATE_BUILD`
- `BUILD_DEPENDENCY_STALE` -> `REBUILD_REQUIRED`
- `PLACEMENT_MISSING` -> `PLAN_PLACEMENT`
- `RUNTIME_NOT_READY` / `AGENT_OFFLINE` -> `WAIT_FOR_SERVER`
- `DEPENDENCY_REQUIRED_UNRESOLVED` -> `WAIT_FOR_RESOURCE`
- `DEPENDENCY_REALIZATION_MISSING` -> `REALIZE_DEPENDENCY`
- `DEPENDENCY_BINDING_STALE` -> `EXPLICIT_MIGRATION_REQUIRED`
- `DEPENDENCY_INTERNAL_TARGET_UNAVAILABLE` -> `INCLUDE_DEPENDENCY_TARGET`
- `DEPENDENCY_PUBLIC_ENDPOINT_MISSING` -> `CONFIGURE_EXPOSURE`
- `DEPENDENCY_ROUTE_CONFLICT` -> `RESOLVE_ROUTE_CONFLICT`
- `DEPENDENCY_PROJECTION_INVALID` -> `REVIEW_CONFIGURATION`

### Multi-Service Batch & First-Deployment Support
When deploying multi-service sets (e.g., `web` + `api`), `request.DeploymentBatch` declares services being deployed together. An `internal_http` or `same_origin` target in the same batch satisfies preflight prerequisites without requiring existing running pods, enabling first deployments to succeed deterministically.

### Stale Review & Dynamic Revalidation on Apply
At Apply time, the preflight engine re-evaluates all checks against live state. If server health degrades, resources fail, builds become stale, or warnings change, the request fails closed with `PREFLIGHT_BLOCKED`, `PREFLIGHT_WARNING_UNACKNOWLEDGED`, or `PREFLIGHT_REVIEW_STALE`.
