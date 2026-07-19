# Opsi Roadmap v5.1 — Production, Manual-first CLI/UI, Generic MCP sau cùng

Ngày cập nhật: 2026-07-16
Trạng thái: roadmap thực thi canonical duy nhất; thay thế
`docs/opsi_roadmap_v4.md` và bản nháp v5 gồm 37 prompt.

Tài liệu này quy định thứ tự thực thi. Trạng thái triển khai và bằng chứng vẫn
thuộc `docs/current_state.md` và `docs/status_matrix.md`; nội dung roadmap không
tự chứng minh capability đã tồn tại.

## 1. Quyết định thứ tự mới

Opsi phải hoàn thiện sản phẩm thủ công trước khi kết nối AI agent:

```text
Core contracts và backend
-> CLI command thủ công
-> CLI local HTTP API
-> Local UI tương tác đầy đủ
-> manual E2E chạy hai lần
-> instruction pack
-> MCP read/planning
-> MCP preflight/challenge
-> generic AI client acceptance
```

MCP không được chứa business logic riêng. Nó chỉ chuyển đổi MCP tool call sang các application service đã được CLI và UI dùng, test và chứng minh trước đó. Nếu một chức năng chưa làm được thủ công qua CLI/UI thì không được expose chức năng đó qua MCP.

## 2. Tổng số prompt mới

Roadmap được rút từ 37 xuống **24 prompt**. Một prompt có scope rộng hơn bản cũ nhưng vẫn giới hạn trong một capability slice có thể review được.

| Phase | Mục tiêu | Prompt |
|---|---|---:|
| A | Security và live baseline | R5-001…R5-005 |
| B | Repository, trusted build và artifact | R5-006…R5-008 |
| C | Manual deployment trên CLI/UI | R5-009…R5-012 |
| D | Hoàn thiện manual CLI/UI và safe operations | R5-013…R5-017 |
| E | Chỉ sau manual gate: instruction pack và MCP | R5-018…R5-020 |
| F | Production hardening, release và DR | R5-021…R5-024 |
| **Tổng** |  | **24** |

## 3. Khi nào cần VPS

| Prompt | Hạ tầng cần có | Tôi phải nhắc user |
|---|---|---|
| R5-003 | VPS Cloud hiện tại | Không cần VPS Agent mới |
| R5-004 | **Một VPS sạch khác để cài K3s + Agent** | **Có — lần đầu cần VPS Agent riêng** |
| R5-010…R5-012 | VPS Agent từ R5-004 | Có, nếu VPS đó chưa còn sạch/usable |
| R5-015…R5-017 | VPS Agent có workload thật | Có |
| R5-017 | Tối thiểu một VPS Agent; hai VPS Agent nếu đóng gate multi-VPS | Có |
| R5-020…R5-023 | Có thể tái sử dụng staging Agent VPS | Có trước destructive/fault test |
| R5-024 | Một VPS control-plane thay thế và hai VPS Agent cho final multi-VPS proof | **Có — cần chuẩn bị thêm hạ tầng DR** |

VPS control-plane hiện tại do operator chỉ định không được dùng làm clean Agent VPS để giả lập separation proof. Host/IP/key chỉ được truyền qua local environment hoặc secure operator config.

## 4. Rule chung bắt buộc

1. Chỉ một active prompt tại một thời điểm.
2. Mỗi task đọc `.agents/rules.md`, `.agents/rules_enforced.md`, `.agents/current.md` và task file; không preload toàn bộ SRS/roadmap cũ.
3. Không đọc/in `.env`, PEM/key, kubeconfig, DB dump, runtime log hoặc secret value.
4. Bỏ qua `.git`, `node_modules`, `.next`, `bin`, `dist`, `release`, `go.sum`, `package-lock.json` và generated files trừ khi task cần trực tiếp.
5. Mỗi feature slice phải đi theo thứ tự:
   - contract/schema;
   - backend/application service;
   - CLI command manual;
   - local HTTP API;
   - UI state/action/progress/error;
   - focused tests;
   - module regression;
   - docs/status/evidence.
6. UI production workflow chỉ gọi `/api/local/...`; browser không giữ PAT hoặc gọi Cloud/Agent trực tiếp.
7. CLI, Local API, UI và sau này MCP phải dùng cùng application service, validator và DTO. Không copy business logic giữa các adapter.
8. Không fake success. Button chưa có backend phải disabled và nói rõ lý do.
9. Manual flow là acceptance source. MCP không được bắt đầu trước khi R5-017 pass.
10. Live checkpoint không có VPS/GitHub thì ghi `BLOCKED` hoặc `DEFERRED`, không thay bằng mock.

## 5. Execution prompts

### R5-001 — Credential incident và source-package hygiene

**Mục tiêu:** đóng lỗ hổng archive chứa `.env`, private key và runtime secrets trước mọi feature.

**Quy trình thực hiện:**

1. Đọc source-package policy và xác định archive chuẩn được tạo bằng command nào.
2. Chỉ kiểm tra path, file type, permission và secret marker; không đọc/in value.
3. Harden tree/archive/release validation để reject `.env`, secret/cert directory, private-key marker, runtime config, DB/log/generated output, traversal và escaping symlink.
4. Sửa `.gitignore` để canonical roadmap/ADR/runbook được track, không dùng blanket ignore khó kiểm soát.
5. Thêm positive/negative fixtures hoàn toàn giả.
6. Viết runbook rotate GitHub App key/client secret/webhook secret, PostgreSQL password, Worker token, bootstrap key, alert token, SMTP credential và PAT nếu có.
7. Nếu operator chưa rotate, kết quả phải là `PARTIAL / OPERATOR_REQUIRED`.

**Read/modify scope:** `.gitignore`, `Makefile`, `scripts/source-package.sh`, hygiene tests, `.agents/*`, `docs/security_story.md`, `docs/current_state.md`, `docs/status_matrix.md`, thêm `docs/runbooks/credential-incident.md`.

**Avoid:** nội dung runtime secret, business code Agent/CLI/Cloud/UI, GitHub API, SSH VPS, Git history rewrite.

**Gate:** canonical package sạch pass; mọi negative fixture fail; no-secret scan pass; rotation chưa làm được ghi đúng blocker.

### R5-002 — Production-like control-plane profile và Full (strict)

**Mục tiêu:** tách deployment staging/production-like khỏi dev Compose đang dùng HTTP/Flexible.

**Quy trình thực hiện:**

1. Giữ `deploy/dev-control-plane` cho development; tạo profile staging riêng.
2. Cấu hình origin TLS bằng certificate/key mount read-only hoặc cơ chế tương đương, tuyệt đối không commit key.
3. Bật production validation, Agent signature requirement, non-root/read-only/cap-drop, health checks và bounded logs.
4. Chặn `/internal/*`, `/api/internal/*`, `/metrics` ở public proxy.
5. Viết migration runbook Flexible -> Full (strict), rollback và external verification.
6. Thêm validator cho HTTPS public base URL, GitHub callback host, missing secret mount và insecure flags.
7. Không tự đổi Cloudflare/firewall nếu chưa có operator authority.

**Read/modify scope:** `deploy/dev-control-plane/*` ngoại trừ secret values, thêm `deploy/staging-control-plane/*`, `cloud/Dockerfile`, Cloud config validation, deployment validators, Makefile, TLS runbook/status docs.

**Avoid:** Agent deployment engine, OIDC/BuildRecord, CLI/UI feature, runtime `.env`/keys.

**Gate:** static/config tests chứng minh fail-closed; live Full (strict) vẫn `UNPROVEN` đến R5-003.

### R5-003 — Cloud VPS live smoke, restart và persistence

**Hạ tầng:** dùng VPS Cloud hiện tại; chưa cần VPS Agent khác.

**Mục tiêu:** xác minh deployment thật thay vì suy ra từ Compose file.

**Quy trình thực hiện:**

1. Nhận SSH target/key từ operator hoặc environment, không hardcode vào source.
2. Read-only inventory: OS, Docker/Compose, image digest, port binding, health, volume, production boolean và backup readiness; redact toàn bộ value nhạy cảm.
3. Xác minh edge HTTPS, origin certificate và Cloudflare Full (strict).
4. Theo runbook, restart độc lập proxy, Worker, Cloud và PostgreSQL.
5. Xác minh Cloud reconnect, data/IDs/PAT hash còn tồn tại và down/up giữ volume.
6. Probe public/internal routes và scan logs/evidence cho secret canary/marker.
7. Ghi exact revision/image digest/timestamp vào evidence.

**Read/modify scope:** control-plane runbooks/verifier, staging profile, backup scripts, evidence/status docs. Runtime defect chỉ sửa bằng sub-scope rõ ràng nếu test tái hiện được.

**Avoid:** DB row dump, env/key value, destructive volume reset, target Agent VPS, feature refactor.

**Gate:** restart/persistence/TLS matrix thật pass; nếu vẫn dev/Flexible thì trạng thái `PARTIAL`.

### R5-004 — Clean VPS bootstrap, K3s và Agent

**CẦN USER CHUẨN BỊ:** **một VPS Ubuntu sạch khác** để làm target Agent. Tới prompt này phải dừng và nhắc user cung cấp VPS/SSH access; không dùng Cloud VPS làm target.

**Mục tiêu:** chứng minh Bootstrap Worker cài được K3s + Agent an toàn và resume được.

**Quy trình thực hiện:**

1. Chạy preflight VPS: OS/arch/disk/port/sudo/host key, không thay đổi host nếu chưa pass.
2. Tạo bootstrap session từ manual CLI hoặc Local UI path hiện có.
3. Worker lease job, strict host-key verify, tải installer K3s pinned/checksummed.
4. Cài Agent artifact đã verify, production auth/TLS, one-time registration.
5. Fault test: restart Worker giữa step, partial install retry, target reboot và duplicate delivery.
6. Negative test: changed SSH host key, incompatible K3s, bad checksum, expired registration token.
7. CLI phải resolve endpoint và kết nối trực tiếp Agent qua TLS/pinning; UI hiển thị progress/failure qua Local API.

**Read/modify scope:** Bootstrap Worker/registry/checkpoint, Agent entrypoint/packaging/TLS, CLI bootstrap/status/local API, UI node/bootstrap view, E2E script/runbook/tests/status.

**Avoid:** GitHub CD, MCP/AI, arbitrary remote shell, `StrictHostKeyChecking=no`, secret argv.

**Gate:** clean VPS + restart/reboot/changed-key evidence; CLI và UI thấy cùng trạng thái thật.

### R5-005 — Live GitHub App auth, installation và repository binding

**Mục tiêu:** biến P07–P10 từ local-test claim thành manual workflow usable qua CLI/UI.

**Quy trình thực hiện:**

1. Dùng public HTTPS callback và GitHub App test installation.
2. Chứng minh browser login PKCE/state và prelinked numeric GitHub identity.
3. Claim installation, đồng bộ repository inventory, nhận webhook install/repository/rename/remove và durable dedupe.
4. Claim repository và bind nhiều service key trong cùng project.
5. Hoàn thiện manual CLI commands: list installations/repos, claim/release repo, list/create/remove binding, `opsi init` dry-run/apply/idempotent.
6. Hoàn thiện Local API/UI cho cùng flow: install/connect, inventory, claim conflict, binding state và error recovery.
7. Negative test wrong user, reused grant/state, repository ngoài installation, duplicate delivery và removed repository.

**Read/modify scope:** Cloud `github_*` auth/webhook/registry/migrations, CLI init/cloud client/commands, Local API, UI GitHub/repository/service screens, focused/live tests/runbook/status.

**Avoid:** OIDC BuildRecord, Agent deploy, MCP, token/key logging, direct browser-to-Cloud client.

**Gate:** CLI và UI đều hoàn thành manual GitHub App/repository binding; live evidence không chứa token.

**Trạng thái acceptance 2026-07-18:** `OPERATOR_REQUIRED`. Browser login/callback,
installation/repository claim, hai service binding, `opsi init`, CLI/Local API
parity và durable replay của delivery `installation_repositories: added` đã pass
live. GitHub App delivery API chưa có delivery `removed` hoặc `repository` cho
fixture; chưa có account GitHub thứ hai cho live wrong-user negative. Các bằng
chứng live này thuộc canonical procedure ở trên nên chặn `DONE`; không thay bằng
mock. R5-006 được thực hiện riêng và không thay đổi trạng thái deferred này của
R5-005.

### R5-006 — Monorepo config v2, changed-service resolver và workflow generation

**Mục tiêu:** một repository chứa nhiều service và chỉ build service bị ảnh hưởng, nhưng fail safe thành full build khi diff không đáng tin.

**Quy trình thực hiện:**

1. Freeze `.opsi/opsi-cd.yaml` v2: services, context, Dockerfile, platform, watch/shared paths, dependencies, production/preview intent; không chứa project/VPS/Cloud URL/secret.
2. Migrate v1 -> v2; `opsi init` add/update một service mà không xóa service khác.
3. Implement resolver nhận event/base/head và `git diff --name-status -z`.
4. Xử lý push, PR, merge, rename, delete, initial build, shallow/missing base, shared path và dependency closure.
5. Unknown base => full build, không trả empty build.
6. CLI có `opsi cd plan --base ... --head ... --json` và human-readable reason per service.
7. Local API/UI có preview affected services trước khi commit workflow/config change.
8. Generate secure workflow matrix với minimal permissions, timeout/concurrency và full-SHA pinned actions; fork không có push/deploy authority.

**Read/modify scope:** CLI repository/init/new CD planning packages, workflow renderer/tests, Local API/UI repository setup view, versioned contract docs/status.

**Avoid:** Cloud OIDC endpoint, Agent, arbitrary shell policy, user source upload, committed secrets.

**Gate:** multi-service/migration/diff/security golden tests; CLI/UI preview cùng một plan hash.

**Trạng thái acceptance 2026-07-18:** `DONE / FUNCTIONAL_ACCEPTANCE_PASS` cho
manual local scope. Config v2 migration/init, bounded changed-service plans,
CLI/Local API plan-hash parity, deterministic secure workflow YAML, UI preview/
apply/error states, and disposable Git acceptance (api, worker, shared path,
dependency closure, rename, missing base, and empty diff) pass. R5-007 OIDC,
BuildRecord, GHCR push, and deployment remain unstarted; live GitHub runner proof
is intentionally deferred to R5-008.

**Focused follow-up 2026-07-19:** R5-006 remains `DONE`. The R5-007 entry
review made Local apply require a bounded safe idempotency key and the exact
server-generated preview hash bound to the canonical request, current/rendered
managed-file hashes, and ordered actions. Exact retries reuse the stored result;
conflicting keys and stale filesystem state fail typed without writing.

### R5-007 — GitHub Actions OIDC verifier và BuildRecord v1

**Mục tiêu:** Cloud xác thực GitHub workload và lưu artifact metadata, chưa tự deploy.

**Quy trình thực hiện:**

1. Tạo isolated OIDC verifier: fixed HTTPS issuer/JWKS, bounded cache/refresh/timeout/size, no redirect downgrade.
2. Verify signature, algorithm, issuer, exact audience, exp/nbf và clock skew.
3. Bind repository_id, owner_id, ref, sha, event, run_id/attempt, workflow và job_workflow_ref.
4. Define BuildRecord v1 có service key, config hash, platform, OCI repository/digest và optional provenance digest.
5. Mọi body field security-relevant phải match verified claim hoặc stored binding/policy.
6. Persist idempotently theo repository/run/attempt/service; conflict khác payload fail closed.
7. Thêm manual CLI/UI read-only BuildRecord list/detail để operator kiểm tra; chưa có deploy button.
8. Không lưu JWT, source, build context hoặc raw build logs.

**Read/modify scope:** Cloud auth/config/server/registry/Postgres migrations, GitHub binding, contracts, CLI Cloud client/read commands, Local API/UI build records, tests/docs/status.

**Avoid:** Agent deployment, DeploymentPolicy auto-route, MCP, mutable tag authority.

**Gate:** claim/body/replay/idempotency/Postgres restart/no-log-token tests; CLI/UI hiển thị cùng sanitized record.

**Trạng thái acceptance 2026-07-19:** `DONE / LOCAL_FUNCTIONAL_ACCEPTANCE_PASS /
LIVE_EVIDENCE_DEFERRED`. Local signed OIDC fixtures, pinned production config,
claim and stored-binding authorization, exact/conflicting replay, append-only
PostgreSQL restart/concurrency evidence, CLI/Local API/UI read parity, and
source/config validators pass. GitHub-hosted runner, GHCR, live token, and
registry correlation remain explicitly assigned to R5-008.

### R5-008 — Live GitHub runner và public GHCR proof

**Mục tiêu:** chứng minh R5-006/R5-007 bằng token và registry thật.

**Quy trình thực hiện:**

1. Chạy generated workflow trên GitHub-hosted runner với monorepo ít nhất hai service.
2. Thay đổi một service và chứng minh matrix chỉ build service đó.
3. Push public GHCR bằng `GITHUB_TOKEN`, lấy immutable digest.
4. Submit OIDC-authenticated BuildRecord và so sánh claim/body/registry digest.
5. CLI/UI manual kiểm tra record, reason và source revision.
6. Negative jobs: wrong audience/workflow/ref/SHA/service, replay và tag-only.
7. Evidence chỉ chứa run IDs, hashes/digests và fields đã redact; không chứa JWT/token.

**Read/modify scope:** generated workflow, OIDC/BuildRecord focused defects, live verifier/runbook/evidence/status, CLI/UI display defects.

**Avoid:** Agent/K3s deploy, private registry, PR preview deploy, AI/MCP.

**Gate:** real runner -> GHCR digest -> BuildRecord pass; changed-service behavior có evidence thật.

### R5-009 — TopologyPlan, DeploymentPolicy và manual placement CLI/UI

**Mục tiêu:** user cấu hình/phân bổ service trên một hoặc nhiều VPS thủ công trước khi trusted CD route job.

**Quy trình thực hiện:**

1. Chốt MVP: mỗi VPS là một single-node K3s runtime; multi-node K3s/HA để post-v1.
2. Define `TopologyPlan v1`: service -> environment/runtime, resources, exposure intent và rationale metadata.
3. Deterministic validator dùng runtime/node health, capacity, heartbeat freshness, reserved headroom và current assignment.
4. Unknown/stale capacity fail closed hoặc yêu cầu explicit override theo policy; không cho AI/human reason thay đổi policy.
5. Define `DeploymentPolicy`: repository/service/workflow/event/ref/environment/runtime/registry allowlist.
6. Target routing phải deterministic. Với runtime single-node có 0 hoặc >1 eligible deploy Agent, fail closed thay vì chọn map iteration.
7. Hoàn thiện manual CLI: topology plan/validate/diff/apply/get; policy create/diff/apply/disable/list.
8. Hoàn thiện Local API/UI wizard: chọn runtime, xem capacity, cảnh báo oversubscription, review diff và confirm apply.
9. Audit/idempotency/RBAC/Postgres durability cho mọi mutation.

**Read/modify scope:** Cloud runtime/node/service/deployment/GitHub binding models, migrations/API, CLI commands/client/local API, UI topology/policy screens, contracts/tests/docs/status.

**Avoid:** Agent runtime mutation, MCP, AI scoring, Kubernetes scheduler clone, multi-node K3s, arbitrary policy expressions.

**Gate:** CLI/UI manual placement và policy parity; cross-project/stale/oversubscription/ambiguous Agent/replay tests.

### R5-010 — Digest-only Agent deployment và Opsi workload renderer

**CẦN VPS:** dùng VPS Agent đã bootstrap ở R5-004. Nếu VPS đã bị thay đổi ngoài kiểm soát, nhắc user reset hoặc cấp VPS Agent sạch.

**Mục tiêu:** Agent pull OCI digest và render workload Opsi-owned, không clone/build source cho production.

**Quy trình thực hiện:**

1. Freeze image deployment intent: repository, full sha256 digest, platform, BuildRecord, policy/routing identity và typed WorkloadSpec.
2. Production path reject tag-only, malformed digest, mixed Git/image và repository mismatch.
3. Agent image adapter pull/resolve digest qua bounded internal argv/API; no clone/no Dockerfile build.
4. Registry credential interface hỗ trợ anonymous public GHCR trước, scoped private credential sau; không secret argv/log/audit.
5. Deterministic renderer tạo Opsi-owned Deployment + ClusterIP Service với stable labels/spec hash/field manager.
6. Named application container; không wildcard replace sidecar.
7. Support replicas, port, probes, resources, termination grace và secret references by ID; reject privileged/hostPath/hostNetwork/raw YAML.
8. Manual CLI deploy-by-BuildRecord với dry-run/diff/progress/cancel-status nếu safe.
9. Local API/UI chọn accepted BuildRecord, review target/spec rồi stream progress và typed failure.

**Read/modify scope:** deployment contracts Cloud/Agent/CLI, Agent cloudrunner/deploy/renderer/config/redaction, CLI deploy/local API, UI deployments/services, focused/live tests/docs/status.

**Avoid:** Traefik exposure, AI/MCP, arbitrary manifest compatibility, registry admin credentials, source build on VPS.

**Gate:** real public GHCR digest chạy trên K3s; CLI/UI cùng job/progress/result; no-clone/no-secret/sidecar/ownership negatives pass.

### R5-011 — ExposureSpec, readiness, reconciliation và rollback

**CẦN VPS:** VPS Agent có workload thật từ R5-010.

**Mục tiêu:** service được expose qua Traefik, readiness chính xác và rollback về known-good digest.

**Quy trình thực hiện:**

1. Define `ExposureSpec v1`: hostname, normalized path, service port, TLS mode/reference.
2. Chọn một canonical Traefik resource; không hỗ trợ song song nhiều renderer ở MVP.
3. Detect hostname/path conflict và ownership trước apply; reject arbitrary middleware/annotation/Nginx config.
4. Implement rollout state machine: applying, waiting, succeeded, failed, rolling_back, rolled_back, rollback_failed.
5. Chỉ ghi last-known-good sau verified readiness.
6. Agent restart giữa transition phải reconcile local WAL state; Cloud metadata không override runtime truth.
7. Manual CLI exposure create/diff/apply/status và deployment rollback/status/history.
8. Local API/UI hiển thị external readiness, conflict, previous/current digest, rollback confirmation và final outcome.
9. Test healthy A -> broken B -> restore A, no-known-good và rollback failure.

**Read/modify scope:** Agent renderer/deploy/store/rollback/gateway, Cloud result metadata, contracts, CLI/local API, UI exposure/deployment history, E2E/tests/docs/status.

**Avoid:** cert-manager/Cloudflare API provisioning, MCP, arbitrary YAML, raw logs to Cloud.

**Gate:** external endpoint thật trở lại A sau B fail; CLI/UI/audit/K3s state đồng nhất.

### R5-012 — Main CD và PR preview manual acceptance

**CẦN VPS:** VPS Agent staging; cần test repository GitHub thật.

**Mục tiêu:** hoàn thành trusted CD main branch và preview PR, vẫn có manual visibility/control qua CLI/UI.

**Quy trình thực hiện:**

1. Main flow: push -> changed services -> GHCR -> OIDC BuildRecord -> DeploymentPolicy -> durable job -> Agent -> K3s -> endpoint.
2. Duplicate BuildRecord/job phải reuse idempotently; Cloud/Agent restart giữa flow phải recover.
3. PR preview chỉ cho same-repo khi policy bật; fork fail closed.
4. Không dùng `pull_request_target`, production secret hoặc production namespace cho untrusted code.
5. Preview có isolated namespace/environment, quota, unique hostname, TTL và cleanup on close/expiry.
6. Manual CLI: CD status/history/retry-safe action, preview list/detail/cleanup request.
7. Local API/UI: deployment timeline, affected service reason, preview URL/TTL, failure/retry và cleanup progress.
8. Broken main rollout rollback; PR promotion không được reuse PR approval làm production authority.

**Read/modify scope:** workflow/OIDC/BuildRecord/policy/routing/Agent deploy/gateway, CLI/local API/UI CD screens, E2E/runbook/evidence/status.

**Avoid:** MCP/AI, private registry, production customer environment, weakening fork policy.

**Gate:** real main CD + same-repo preview + TTL cleanup + fork negative; CLI/UI quan sát/điều khiển đúng permitted manual operations.

### R5-013 — Hoàn thiện toàn bộ manual CLI

**Mục tiêu:** CLI là canonical manual interface đầy đủ, không chỉ tập hợp các lệnh rời rạc.

**Quy trình thực hiện:**

1. Lập capability matrix từ backend/API hiện có, không dựa vào README claims.
2. Hoàn thiện command groups:
   - auth/session/PAT lifecycle;
   - organizations/projects/members/RBAC;
   - runtimes/nodes/bootstrap/diagnostics;
   - GitHub installations/repositories/bindings/init/CD config;
   - services/dependencies/topology/policy;
   - secrets metadata/create/rotate/reveal có OTP;
   - BuildRecords/deployments/exposure/rollback/history;
   - telemetry/logs/incidents/audit.
3. Chuẩn hóa project selection, `--json`, exit codes, request/idempotency IDs, timeouts, progress và error `next_action`.
4. Long-lived PAT chỉ ở OS keychain; secret input qua stdin/TTY/file descriptor, không flag/argv.
5. Mọi mutation dùng application service và auth origin do trusted adapter set; loại bỏ caller-controlled `--triggered-by` authority.
6. Không tạo fake commands cho backend chưa có; gap phải được ghi rõ hoặc hoàn thiện trong scope liên quan.
7. Viết command-level integration tests với fake Cloud/Agent và manual smoke script.

**Read/modify scope:** toàn bộ `cli/cmd` và `cli/internal/{commands,cloudclient,agentclient,config,keychain}`, contracts cần thiết, CLI docs/tests/status; backend chỉ sửa focused gap đã chứng minh.

**Avoid:** UI implementation, MCP package, provider AI, unrelated backend refactor, secret flags.

**Gate:** capability matrix zero unexplained manual CLI gap; help/JSON/error/progress/secret-negative tests pass.

### R5-014 — Hoàn thiện Local API và UI manual parity

**Mục tiêu:** user hoàn thành toàn bộ workflow bằng UI qua CLI local backend, không cần terminal ngoại trừ cài/start CLI.

**Quy trình thực hiện:**

1. Lấy capability matrix đã pass ở R5-013 và map từng command/application service sang `/api/local/...`.
2. Browser không instantiate Cloud/Agent client và không nhận long-lived PAT.
3. Hoàn thiện navigation/workflows:
   - login/session/project switch;
   - project/member/RBAC;
   - VPS bootstrap/node health;
   - GitHub App/repository/service binding;
   - service/topology/policy;
   - secret lifecycle với OTP;
   - builds/deployments/exposure/rollback;
   - logs/telemetry/incidents/audit.
4. Mọi mutation có loading/progress/success/failure/retry state; success chỉ từ backend result thật.
5. Confirm screen hiển thị exact project/target/diff/risk; stale state buộc refresh/review lại.
6. Xử lý session expiry, Cloud outage, Agent offline, partial job và reconnect stream.
7. Accessibility cơ bản: keyboard/focus/labels/error association; không để disabled action im lặng.
8. Component/integration/E2E browser tests chạy với local backend fakes và real manual staging smoke.

**Read/modify scope:** `cli/internal/commands/start.go` hoặc local application router, `cli/ui` source/tests, shared DTO/contracts, UI docs/status; backend gap chỉ sửa focused.

**Avoid:** direct browser Cloud/Agent calls, PAT/localStorage, MCP, UI rewrite không liên quan, package-lock thủ công.

**Gate:** UI/CLI parity matrix pass; canonical manual flow chạy bằng UI; no fake success/no PAT/browser direct-client tests.

### R5-015 — IncidentEvidence và manual observability CLI/UI

**CẦN VPS:** Agent VPS có workload có thể gây lỗi có kiểm soát.

**Mục tiêu:** user chẩn đoán incident thủ công đầy đủ trước khi AI đọc evidence.

**Quy trình thực hiện:**

1. Define deterministic bounded `IncidentEvidence v1` gồm deployment diff, causal timeline, K8s events/readiness/restarts, redacted log fingerprints/excerpts, coverage/truncation/unavailable marker và hashes.
2. Tag workload-controlled text là untrusted/prompt-injection; không thêm LLM conclusion.
3. Agent build/store evidence local; Cloud chỉ giữ metadata/hash nếu cần.
4. CLI: telemetry summary, redacted logs query, incident list/get/evidence/resolve, deployment diff và audit correlation.
5. Local API/UI: topology/health/timeline/evidence tabs, coverage/truncation, source facts và resolve flow.
6. Induce failing workload thật; verify evidence useful, bounded, stable hash và no-secret.
7. Cloud non-persistence check cho raw logs/evidence/source.

**Read/modify scope:** Agent incident/telemetry/deploy/evidence, contracts, CLI commands/client/local API, UI observability/incidents, tests/E2E/docs/status.

**Avoid:** action execution, MCP, model SDK, raw unbounded logs, Cloud evidence payload storage.

**Gate:** human dùng CLI và UI chẩn đoán/resolve incident không cần AI; real failure evidence pass.

### R5-016 — Safe ActionPlane và out-of-band approval manual CLI/UI

**CẦN VPS:** Agent VPS để chạy typed actions và negative safety tests.

**Mục tiêu:** con người thực hiện safe runtime action hoàn chỉnh trước khi MCP được phép request action.

**Quy trình thực hiện:**

1. Freeze ActionPlan/Preflight/ApprovalChallenge/ApprovalGrant/ActionResult v1.
2. Origin do trusted adapter gán, không nhận từ flag/body tùy ý.
3. Catalog hẹp: restart, scale, deploy/rollback, gateway reconcile, incident resolve.
4. Cấm arbitrary shell/kubectl/SQL, secret reveal, DB mutation, host/K3s destructive action.
5. Agent deterministic risk/current-state hash/preconditions/locks/expiry/nonce/replay policy.
6. Device registration/signing; private key trong OS secure store. Approval diễn ra ở interactive CLI hoặc Local UI, không phải prompt mà automation có thể tự trả lời.
7. Typed executors build args internally, recheck state, timeout/output bound, post-check, rollback result và audit.
8. Migrate mọi manual CLI/UI mutation tương ứng qua same ActionPlane; không còn raw CLI bypass.
9. Test stale/replay/wrong device/project/state, concurrency, R4, post-check failure và revoked device.

**Read/modify scope:** action contracts/Agent auth/audit/deploy/incident, Cloud device public metadata nếu cần, CLI keychain/commands/local API, UI approval/actions, tests/E2E/docs/status.

**Avoid:** MCP, ApprovalGrant trong browser storage/log, generic shell, AI-specific executor.

**Gate:** human-only CLI và UI flow preflight -> separate approve -> execute -> audit pass; no legacy mutation bypass.

### R5-017 — Full manual E2E chạy hai lần — MCP BLOCKING GATE

**CẦN VPS:** tối thiểu một Agent VPS. Để tuyên bố hỗ trợ multi-VPS production path, cần **hai Agent VPS** trong cùng project nhưng hai single-node runtimes độc lập.

**Mục tiêu:** chứng minh sản phẩm đầy đủ không cần MCP/AI.

**Quy trình thực hiện:**

1. Từ clean/reset documented state: control plane -> owner/project -> bootstrap VPS -> GitHub App -> monorepo/service bindings.
2. Tạo topology/policy thủ công; phân bổ hai service lên runtime mong muốn.
3. Push code và chạy changed-service trusted CD tới GHCR digest/K3s/external endpoint.
4. Thực hiện cùng capability qua CLI rồi UI ở các đoạn được matrix chỉ định.
5. Tạo/rotate/reveal secret đúng OTP policy; verify no-secret argv/log/evidence.
6. Gây broken rollout, rollback known-good, tạo incident/evidence.
7. Human preflight/approve/execute typed action và xem audit.
8. Restart Cloud/Worker/Agent, duplicate request, Cloud outage và recovery.
9. Negative suite: cross-project, fork, stale/replay grant, R4, changed SSH key, ambiguous Agent, stale capacity và fake success.
10. Chạy toàn bộ hai lần không hidden manual correction.

**Read/modify scope:** E2E harness/runbooks/evidence/status; mở package cụ thể chỉ khi có failure tái hiện và ghi sub-scope.

**Avoid:** bất kỳ `mcp` package/tool/client nào, AI instruction pack, weakening gate, mock thay live VPS/GitHub.

**Gate bắt buộc:** hai lần manual pass, CLI/UI parity pass, zero open P0 manual capability. **Nếu R5-017 chưa pass thì không được bắt đầu R5-018.**

### R5-018 — Generic AI instruction pack và privacy policy

**Entry gate:** R5-017 đã `PASS` với evidence. Đây là prompt đầu tiên thuộc AI integration phase nhưng chưa khởi chạy MCP.

**Mục tiêu:** chuẩn hóa cách Codex/Claude/generic agent làm việc trong repository mà không đưa provider SDK vào Opsi.

**Quy trình thực hiện:**

1. Tạo canonical policy dưới `.opsi/ai/` và adapter nhỏ cho `AGENTS.md`, `CLAUDE.md` hoặc generic client.
2. `opsi ai init --client ...` có dry-run/diff/managed markers/conflict detection/atomic rollback; không overwrite instruction user.
3. Rules nói rõ AI đọc source trực tiếp trên máy user; Opsi không upload source sang Cloud/Agent.
4. Deny đọc `.env`, key, kubeconfig, credential path và raw runtime secrets.
5. Deny raw SSH/kubectl/docker/database/direct Agent credential; mọi remote operation dùng Opsi typed flow.
6. Chỉ dẫn AI tạo/validate local TopologyPlan, không tự apply.
7. Instruction không được coi là security enforcement; backend ActionPlane vẫn authoritative.
8. Test install/update/uninstall cho Codex/Claude/generic và bảo toàn existing content.

**Read/modify scope:** CLI repository file writer/new ai-init package, templates, root/UI instruction pointers, docs/tests/status.

**Avoid:** Cloud/Agent model integration, provider SDK/API key, MCP server, source scanning/upload.

**Gate:** ba adapter dùng cùng canonical rules; no-secret/conflict/rollback tests pass.

### R5-019 — MCP read-only và topology planning

**Entry gate:** manual application services từ R5-013…R5-017 được reuse, không viết lại nghiệp vụ.

**Mục tiêu:** AI agent đọc facts và tạo local draft plan, chưa request/execute runtime mutation.

**Quy trình thực hiện:**

1. Implement `opsi mcp serve` qua stdio với protocol lifecycle, explicit project selection và bounded structured errors.
2. MCP adapter gọi cùng application services của CLI/local API.
3. Read tools: project/runtime/node capacity, services/bindings/topology/policy, builds/deployments/exposure, telemetry summary, redacted logs, incidents/evidence, audit/action catalog read-only.
4. Planning tools chỉ create/validate/diff local TopologyPlan/Service config draft; không apply Cloud mutation.
5. Không expose PAT, Agent token, device key, secret value, ApprovalGrant hoặc source content.
6. Audit tool metadata/project/result class, không lưu prompt/conversation.
7. Bound item count/bytes/time/rate; evidence text được đánh dấu untrusted.
8. Conformance tests stdout protocol-only, stderr sanitized, invalid project/session và secret canary.

**Read/modify scope:** CLI MCP adapter/new command, existing application services/DTO, instruction docs/tests/status. Chỉ sửa core application service khi parity bug được chứng minh.

**Avoid:** execute/approve/challenge tools, direct Cloud/Agent client mới dành riêng MCP, model SDK, source upload.

**Gate:** một generic MCP client đọc đầy đủ manual facts và tạo valid local draft; zero mutation tool.

### R5-020 — MCP preflight/challenge và two-client acceptance

**CẦN VPS:** tái sử dụng Agent VPS staging để thực thi action thật sau khi human approve.

**Mục tiêu:** Codex, Claude hoặc generic clients đề xuất action nhưng không thể approve/execute.

**Quy trình thực hiện:**

1. Expose action catalog, preflight, approval-challenge request và action status.
2. Không expose execute, approve, apply-topology hoặc ApprovalGrant ở tool schema/output/error.
3. Bind session/project/device context và enforce payload/time/rate bounds.
4. Với client thứ nhất: đọc IncidentEvidence -> tạo ActionPlan -> preflight -> request challenge.
5. User approve qua interactive CLI hoặc Local UI độc lập; Agent execute; MCP chỉ poll typed result.
6. Lặp lại với client thứ hai để chứng minh vendor neutrality.
7. Injection tests: log/evidence yêu cầu đọc secret, chạy shell, tự approve, đổi project/target hoặc bỏ qua policy.
8. So sánh action hash/current-state hash/audit giữa MCP, CLI/UI approval và Agent result.

**Read/modify scope:** MCP adapter, ActionPlane application services, client setup/conformance harness, focused tests/evidence/docs/status.

**Avoid:** provider SDK coupling, prompt storage, execute/approve tool, grant leakage, direct Agent credential.

**Gate:** hai real clients pass; human channel separation và injection/R4/no-secret tests pass.

### R5-021 — Production identity, secret lifecycle và runtime isolation

**CẦN VPS:** staging Cloud + Agent VPS; một số rotation/isolation tests gây gián đoạn ngắn.

**Mục tiêu:** harden identity/transport/tenant boundary sau khi product flow đã ổn định.

**Quy trình thực hiện:**

1. TLS 1.3/project-scoped mTLS hoặc equivalent, pinning, issuance/rotation/revocation và wrong-host/project negatives.
2. PAT/device/GitHub App/webhook/bootstrap/registry credential lifecycle với owner/TTL/rotation/runbook.
3. Step-up cho high-risk approval; revoked device/key fail closed.
4. Namespace/labels/ResourceQuota/LimitRange/default-deny NetworkPolicy và minimal service account.
5. Isolate project secrets, registry pull credentials, hostname/topology/evidence/action access.
6. Dedicated Agent OS user, least-privilege systemd/files/directories/capabilities và bounded disk/log/process.
7. Threat model nói rõ single-node K3s không chống được malicious root workload như hard multi-tenancy.
8. CLI/UI admin surfaces cho credential/device status/revoke/rotation guidance, không hiển thị secret.

**Read/modify scope:** Agent TLS/auth/packaging/renderer/secrets, Cloud auth/device/config/RBAC, CLI/UI admin security, tests/runbooks/status/evidence.

**Avoid:** marketing claim hard multi-tenancy, secret output, automatic external credential rotation không authority, feature expansion.

**Gate:** rotation/revocation/cross-project/network/least-privilege live evidence; no-secret scans pass.

### R5-022 — Supply chain và versioned release

**CẦN VPS:** cần clean install smoke; có thể tái sử dụng/reset một Agent VPS hoặc cấp VPS tạm.

**Mục tiêu:** tạo exact release artifacts có thể cài và verify, không phụ thuộc working tree.

**Quy trình thực hiện:**

1. Pin toolchain/base image/GitHub actions bằng digest hoặc full SHA.
2. Tạo checksums, SBOM, provenance và signatures cho CLI/Cloud/Worker/Agent/image.
3. Bootstrap Worker verify Agent identity/signature trước install; Agent verify application image signature/provenance theo production policy.
4. Vulnerability scan với severity policy và documented exception expiry.
5. Build signed packages/images, release notes, canonical config examples và compatibility matrix CLI/Cloud/Worker/Agent/contracts/DB.
6. Release directory recreate từ empty; final source/release hygiene scan.
7. CLI/UI hiển thị component version/compatibility/update readiness, không tự upgrade mù.
8. Clean install smoke từ artifacts only.

**Read/modify scope:** Makefile/Dockerfile/release/source hygiene/Agent installer/image adapter/packaging, version status APIs/CLI/UI, workflows/tests/docs/evidence.

**Avoid:** committed binaries/signing key, mutable tag trust, custom cryptography, live production upgrade.

**Gate:** tampered/unsigned/wrong identity negatives; clean artifact-only install pass.

### R5-023 — Private registry, PostgreSQL backup và N-1 upgrade

**CẦN VPS:** staging Cloud và Agent VPS. Phải backup trước destructive migration/upgrade test.

**Mục tiêu:** chứng minh private delivery, data durability và upgrade/recovery path.

**Quy trình thực hiện:**

1. Private OCI: separate runner push/Agent pull principals, minimal scope, rotation/revocation, no plaintext Cloud metadata.
2. Full (strict) external control-plane/application TLS, origin bypass protection, certificate renewal/failure tests.
3. Define PostgreSQL/non-DB key backup scope, encrypted/integrity-checked artifact, retention và restore verifier.
4. Install N-1 release, tạo representative project/repository/topology/build/deployment/audit state.
5. Upgrade theo compatibility order; test mixed-version window, migrations, job/lease recovery và Agent reconciliation.
6. Rollback binary/config khi schema cho phép; nếu migration không reversible, dùng forward-fix hoặc verified restore, không giả downgrade.
7. CLI/UI upgrade/backup health surfaces hiển thị status thật và operator action.
8. Đo downtime, RPO/RTO sơ bộ và data integrity.

**Read/modify scope:** staging deployment, registry auth, Cloud migrations/Postgres, backup scripts, release upgrade mechanism, CLI/UI operations status, tests/runbooks/evidence.

**Avoid:** production data, broad registry admin token, DB dump secret, claim application PV backup khi chưa có.

**Gate:** private digest deploy, backup/restore và N-1 -> N/declared recovery pass.

### R5-024 — Replacement-control-plane DR và final production acceptance

**CẦN USER CHUẨN BỊ:** một VPS control-plane thay thế; để đóng multi-VPS gate cần hai Agent VPS. Không chạy fault injection trên production customer environment.

**Mục tiêu:** mất control-plane host vẫn recover được và toàn bộ acceptance chạy hai lần.

**Quy trình thực hiện:**

1. Simulate loss của staging control-plane host.
2. Provision replacement từ exact signed release; restore PostgreSQL và required key/identity material.
3. Re-establish Full (strict), GitHub App/PAT/device behavior, Worker và public/internal route policy.
4. Agents reconnect; runtimes reconcile; queued/in-flight work xử lý theo documented semantics; audit chain giữ integrity.
5. Chạy final scenario: install -> multi-VPS bootstrap -> GitHub App -> monorepo changed-service CD -> private digest -> managed HTTPS -> manual CLI/UI -> IncidentEvidence -> ActionPlane -> MCP two-client -> rotation -> fault -> backup/upgrade/restore/DR.
6. Chạy hai lần với clean/reset documented state và exact artifact versions.
7. Review status matrix; bất kỳ P0 `PARTIAL/UNPROVEN/MANUAL_GATED/BLOCKED` đều cấm nhãn production-ready.
8. Publish known exclusions, measured RTO/RPO và operational ownership.

**Read/modify scope:** final runbooks/E2E/DR/release/evidence/status; source package chỉ mở khi failure cụ thể cần root cause và phải ghi sub-scope.

**Avoid:** synthetic proof, xóa failed evidence, hidden manual correction, weakening gate, production marketing trước pass thứ hai.

**Gate:** replacement control plane pass; final acceptance hai lần; zero open P0; reviewed redacted artifact.

## 6. Milestone gates

| Milestone | Điều kiện |
|---|---|
| BASELINE_SECURE | R5-001…R5-005 pass hoặc checkpoint blocker được ghi trung thực |
| TRUSTED_BUILD | R5-006…R5-008 pass với GitHub runner thật |
| MANUAL_DELIVERY | R5-009…R5-012 pass trên Agent VPS |
| MANUAL_PRODUCT | R5-013…R5-017 pass; CLI/UI manual E2E hai lần |
| MCP_ALLOWED | Chỉ được mở sau `MANUAL_PRODUCT` |
| GENERIC_AI | R5-018…R5-020 pass với hai MCP clients |
| RELEASE_CANDIDATE | R5-021…R5-023 pass |
| PRODUCTION_READY | R5-024 pass hai lần và zero open P0 |

## 7. Quy tắc nhắc VPS cho các prompt tiếp theo

Khi user yêu cầu “prompt tiếp theo”, trước khi đưa prompt tôi phải kiểm tra bảng hạ tầng:

- Nếu prompt là R5-004: nhắc ngay cần một VPS Agent sạch khác Cloud VPS.
- Nếu prompt là R5-010…R5-017 hoặc R5-020…R5-023: xác nhận Agent VPS còn usable và snapshot/reset state phù hợp.
- Nếu prompt là R5-024: nhắc cần replacement control-plane VPS và hai Agent VPS để đóng multi-VPS/DR acceptance.

Không yêu cầu user cung cấp secret trong chat. Chỉ hướng dẫn đặt SSH target/key bằng local environment hoặc secure operator config.
