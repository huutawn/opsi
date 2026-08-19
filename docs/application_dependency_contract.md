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
- Injection Mappings: strict symbolic mappings.

## ADC-02: Managed Resource Dependency Realization (PostgreSQL + Valkey)

ADC-02 bridges declared application dependency intent with runtime environment and secret realization.

### Realization Pipeline
1. **Dependency Intent**: An Application declares a `managed_resource` dependency (`postgres` or `redis`) with symbolic injection mappings (`resource.host`, `resource.port`, `credential.username`, `credential.password`, `credential.database`, `connection.url`).
2. **Review / Apply**:
   - `POST /api/projects/{project_id}/services/{service_id}/dependencies/review`: Zero-mutation plan inspecting target readiness, identifying existing bindings for reuse or planned creation, and projecting environment descriptors without credential leakage.
   - `POST /api/projects/{project_id}/services/{service_id}/dependencies/apply`: Idempotently creates or reuses canonical `ResourceBinding` via `resource.Service`, linking `ResourceBindings` into the `ServiceConfiguration`.
3. **Symbolic Source Resolution**:
   - `resource.host`, `resource.port`, `credential.database` project into non-secret `deploymentv1.EnvironmentVariable`.
   - `credential.username`, `credential.password`, `connection.url` project into `deploymentv1.SecretReference` referencing the binding credential.
4. **Workload Secret Delivery & Injection**:
   - The deployment compiler resolves secret materials via `ResolveSecretMaterials` without leaking credentials into `WorkloadSpec` or diffs.
   - The Agent materializes Kubernetes Secrets (`opsi-<serviceKey>-<runtimeID>-binding-<secretID>`) with keys matching the mapped environment variable names.
   - The workload Pod receives values via `valueFrom.secretKeyRef`.
5. **Safety Invariants**:
   - Active binding target replacement requires explicit migration (`DEPENDENCY_BINDING_REPLACEMENT_REQUIRES_EXPLICIT_MIGRATION`).
   - Managed resource build-phase injection is explicitly rejected (`DEPENDENCY_BUILD_PHASE_UNSUPPORTED`).
   - Platform reserved environment variables (`PORT`, `HOSTNAME`, `OPSI_*`, `KUBERNETES_*`) and manual/generated conflicts fail closed with `DEPENDENCY_ENV_CONFLICT`.
   - Modifying dependencies creates a new `ServiceConfiguration` revision and compiles a new `DeploymentJob` using the immutable `BuildRecord` (zero image rebuilds).
