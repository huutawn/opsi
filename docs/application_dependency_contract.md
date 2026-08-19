# Application Dependency Contract

## Architecture Decision

We chose **Option A: Extend the existing ServiceConfiguration authority**.

### Reason
The existing `ServiceConfiguration` authority already owns runtime configuration (`Environment`, `Bindings`, `PublicRoute`, `ResourceBindings`), has robust revision/review/apply semantics, and is correctly scoped to the consumer Application (Project + Service + Environment). 
Extending it follows the preferred rule to NOT create competing configuration authorities. Application dependencies are conceptually part of the service's configuration. To respect bounded contexts, structural validation is performed during Review, while deep target existence resolution for `managed_resource` (which belongs to the `resource` module) will be enforced at Preflight/Compilation (ADC-04). For ADC-01, we ensure the structural constraints and logical requirements are durably persisted in the unified configuration.

## Model
The canonical model supports:
- Consumer: ServiceRecord (Project/Environment scoped)
- Target Kind: `application` or `managed_resource`
- Injection Phase: `runtime` or `build`
- Injection Mappings: strict symbolic mappings.

ADC-01 performs ZERO runtime mutation and acts purely as the durable intent authority for future preflight and MCP phases.
