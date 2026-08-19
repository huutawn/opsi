# Application Dependency Contract

## Architecture Decision

We chose **Option A: Extend the existing ServiceConfiguration authority**.

### Reason
The existing `ServiceConfiguration` authority already owns runtime configuration (`Environment`, `Bindings`, `PublicRoute`, `ResourceBindings`), has robust revision/review/apply semantics, and is correctly scoped to the consumer Application (Project + Service + Environment). 
Extending it follows the preferred rule to NOT create competing configuration authorities. Application dependencies are conceptually part of the service's configuration. Dependency target references are factually validated against canonical target authority during Review and revalidated at Apply where necessary. ADC-04 will validate deployability/readiness/resolution, NOT basic identity existence/ownership.

## Model
The canonical model supports:
- Consumer: ServiceRecord (Project/Environment scoped)
- Target Kind: `application` or `managed_resource`
- Injection Phase: `runtime` or `build`
- Injection Mappings: strict symbolic mappings.

ADC-01 performs ZERO runtime mutation and acts purely as the durable intent authority for future preflight and MCP phases.
