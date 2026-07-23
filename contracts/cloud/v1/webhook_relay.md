# Cloud Webhook Relay Contract v1

Cloud retains historical relay metadata for restore/read compatibility and owns
the canonical Agent PollJob transport. It must not persist raw operational
logs, metrics, Kubernetes secrets, kubeconfig, or application secrets.

## Retired Generic Relay

The generic GitHub push relay and its route-scoped signature envelope are
retired. Historical `relay_jobs` and `relay_events` rows remain readable for
restore/audit purposes, but runtime code never enqueues, claims, or delivers
them.

## Agent PollJob

```http
GET /v1/agents/{agent_id}/webhooks/next?project_id=proj_123&wait=30s
Authorization: Bearer <agent-token>
```

Success with event is a canonical immutable deployment lease containing a
versioned `AgentCommand` with the accepted image digest, workload, authority
snapshot, lease token, and rollout intent when applicable.

No pending event:

```http
204 No Content
```

The Agent verifies the command schema, target identity, lease token, and digest
before invoking `ProductionAdapter` or `ReconcileRollout`. Build, Git, and
arbitrary manifest inputs are never accepted by this transport.
