# Cloud Webhook Relay Contract v1

Cloud owns short-lived GitHub webhook relay metadata only. Payload TTL is 24 hours maximum. Cloud must not persist raw operational logs, metrics, Kubernetes secrets, kubeconfig, or application secrets.

## Receive GitHub Webhook

```http
POST /v1/webhooks/github
X-GitHub-Event: push
X-Hub-Signature-256: sha256=<hex-hmac>
X-GitHub-Delivery: <delivery-id>
Content-Type: application/json
```

Cloud verifies repository/project/service routing policy, stores the body and signature envelope with a 24 hour TTL, and returns quickly. Cloud maps repo and branch to `project_id` + `service_id`; Agent verifies the signature and enforces the project/service scope before deployment.

Success:

```http
202 Accepted
```

## Agent Long Poll

```http
GET /v1/agents/{agent_id}/webhooks/next?project_id=proj_123&wait=30s
Authorization: Bearer <agent-token>
```

Success with event:

```json
{
  "project_id": "proj_123",
  "service_id": "svc_api",
  "service_name": "api",
  "service_type": "backend",
  "repo_url": "https://github.com/acme/api.git",
  "ref": "refs/heads/main",
  "after": "abcdef1234567890",
  "branch": "main",
  "triggered_by": "github:webhook",
  "body": "{...original GitHub push body...}",
  "signature": "sha256=<hex-hmac>"
}
```

No pending event:

```http
204 No Content
```

Agent verifies `signature` locally with its configured `deployment.webhook_secret`, then deploys only inside the provided `project_id` and `service_id` scope.
