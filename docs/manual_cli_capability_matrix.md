# Manual CLI Capability Matrix

This matrix is based on the existing Cloud handlers, Agent RPC contracts, and
the CLI clients in this revision. `SUPPORTED` means a real command reaches the
listed API; `BACKEND_GAP` means no factual API exists; `OUT_OF_SCOPE` is not
implemented by the manual CLI contract.

| Capability | Canonical API/RPC | CLI command | Kind | Role | Human/JSON | Timeout | Idempotency | Evidence | R5-014 UI flow | Status |
|---|---|---|---|---|---|---|---|---|---|---|
| PAT keychain login | OS keychain | `opsi login --pat-file` | mutation | local user | receipt/receipt | n/a | replace | login tests | session login | SUPPORTED |
| PAT verify/rotate/revoke and session | `/v1/auth/pat/{verify,rotate,revoke}` | `opsi auth verify|rotate|revoke` | read/mutation | project owner | JSON/JSON | 30s | request header; backend replay not guaranteed | auth/client tests | session/token controls | SUPPORTED |
| Organization list | none | none | read | n/a | n/a | n/a | n/a | backend gap record | organization picker | BACKEND_GAP |
| Project list/create | `/api/orgs/{org}/projects` | `opsi project list|create` | read/mutation | owner/admin for create | JSON/JSON | 30s | required create key | project command/client tests | project console | SUPPORTED |
| Members and RBAC | none | none | read/mutation | n/a | n/a | n/a | n/a | backend gap record | members/RBAC | BACKEND_GAP |
| Runtime inventory | `/api/projects/{project}/topology/facts` | `opsi runtime list|get`, `opsi topology facts` | read | project viewer | JSON/JSON | 30s | n/a | topology/client tests | topology runtime view | SUPPORTED |
| Node inventory/diagnostics | `/api/projects/{project}/nodes[/{node}]` | `opsi node list|get` | read | project viewer | JSON/JSON | 30s | n/a | node/client tests | nodes view | SUPPORTED |
| Node offline/drain/remove | `/nodes/{id}/{offline,drain,remove}` | `opsi node offline|drain|remove` | mutation | owner/admin | JSON/JSON | 30s | required key | node/client tests | node lifecycle actions | SUPPORTED |
| Bootstrap create/status/events/retry | `/api/projects/{project}/bootstrap-sessions` | `opsi server bootstrap|status|events|retry` | mutation/read | owner/admin | JSON/JSON | 30s | required mutation key | server/client tests | bootstrap timeline | SUPPORTED |
| GitHub installation inventory/claim | `/v1/projects/{project}/github/installations` and claim routes | `opsi github installation ...` | read/mutation | project member/owner | JSON/JSON | 30s | fixed claim keys | github tests | GitHub installation flow | SUPPORTED |
| Repository inventory/claim/binding | `/v1/projects/{project}/github/repositories` and bindings | `opsi github repository|binding ...` | read/mutation | owner/admin for mutation | JSON/JSON | 30s | fixed keys | github/init tests | repository binding flow | SUPPORTED |
| Repository initialization/CD config | local repository plus binding APIs | `opsi init`, `opsi cd ...` | mutation/read | project developer | plan/JSON | bounded command timeout | preview hash/idempotent binding | init/CD tests | repository setup/CD | SUPPORTED |
| Services/catalog | Agent service RPCs and Cloud service list | `opsi service ...` | read/mutation | project operator | JSON/JSON | 30s | Agent mutation RPCs have no idempotency field | service tests | services view | SUPPORTED |
| Topology/dependencies/policy | `/topology/*`, `/deployment-policies/*` | `opsi topology ...`, `opsi policy ...` | read/mutation | owner/admin | JSON/JSON | 30s | required mutation keys | placement tests | topology/policy wizard | SUPPORTED |
| Secret metadata | none | none | read | n/a | n/a | n/a | n/a | backend gap record | secret metadata view | BACKEND_GAP |
| Secret create/rotate/reveal | Agent Secret RPCs | `opsi secret create|rotate|reveal` | mutation | authenticated project operator | sanitized JSON receipt/sanitized JSON receipt | 30s | Agent contract has no idempotency field | secret tests; protected output tests | secret controls | SUPPORTED |
| TOTP setup | Agent `SetupTOTP` RPC | `opsi secret setup-totp` | mutation | authenticated project operator | sanitized JSON receipt/sanitized JSON receipt | 30s | Agent contract has no idempotency field | secret canary tests | TOTP setup | SUPPORTED |
| BuildRecord list/detail | `/api/projects/{project}/build-records` | `opsi build-record list|get` | read | project viewer | human/JSON | 30s | n/a | build-record tests | build history | SUPPORTED |
| Deployment preview/apply/status/events/rollback | `/api/projects/{project}/deployments/*` | `opsi deploy ...` | read/mutation | owner/admin/developer | human/JSON | bounded per command | required keys | deploy tests | deployment view | SUPPORTED |
| Exposure preview/apply/history | `/api/projects/{project}/exposures/*` | `opsi exposure ...` | read/mutation | owner/admin/developer | human/JSON | 30s | required keys | exposure tests | exposure view | SUPPORTED |
| Telemetry/logs | Agent `TelemetryQuery` RPC | `opsi telemetry query`, `opsi sync` | read | authenticated project operator | human/JSON | 30s | cursor/state | telemetry/sync tests | telemetry/logs | SUPPORTED |
| Incidents and evidence | Agent Incident RPCs including `GetIncidentEvidence` | `opsi incident list|get|evidence|resolve` | read/mutation | authenticated project operator | JSON/JSON | 30s | Agent resolve RPC has no idempotency field | incident/evidence/auth/TLS tests | incidents; evidence UI deferred to R5-017 | SUPPORTED / EVIDENCE_SOURCE_PASS |
| Action approval devices | Cloud ActionDevice APIs | `opsi action device register|list|revoke` | mutation/read | owner/developer; viewer list only | sanitized JSON | 30s | required registration identity; idempotent revoke | Cloud registry/PostgreSQL/Linux Secret Service tests; typed local-cleanup retry receipt; Darwin cross-build/fail-closed source test | UI deferred to R5-017 | SOURCE_PASS_LINUX / CLI_HYGIENE_FIXED / BACKEND_GAP_DARWIN_ACTIONPLANE |
| Safe runtime actions | Agent ActionService RPCs | `opsi action catalog|preflight|approve|execute|status` | read/mutation | owner/developer | sanitized JSON + exact TTY phrase | challenge max 5m; plan max 10m; bounded recovery post-check | atomic reservation, nonce consume, exact terminal replay, unresolved lock retention | recovery classification/starvation/reporting tests; zero recovery executor calls; labeled control-free approval target | UI deferred to R5-017 | SOURCE_FIXED_LINUX / BACKEND_GAP_DARWIN_ACTIONPLANE |
| Audit | `/api/projects/{project}/audit` | `opsi audit list` | read | project viewer | JSON/JSON | 30s | n/a | audit/client tests | audit view | SUPPORTED |
| Configuration/version/install | CLI YAML, keychain, release artifacts | `--config`, `opsi version`, installer | local/read | local user | human/JSON | n/a | atomic files | config/version/installer tests | settings/about | SUPPORTED |

All networked commands use the selected config snapshot, bounded contexts, and
the configured Cloud/Agent authority. Unsupported capabilities are not
represented as fake commands.
