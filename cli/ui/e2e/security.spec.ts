import { expect, test, type Page, type Route } from "@playwright/test";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Security & Audit Center loads canonical overview, audit explorer, and access identities", async ({ page }) => {
  await mockSecurityAPI(page);
  await page.setViewportSize({ width: 1440, height: 900 });

  // 1. Security Overview route loads directly with deep-link state
  await page.goto("/?project=proj-1&view=security&tab=overview");
  await expect(page.getByRole("heading", { name: "Security Overview", exact: true })).toBeVisible();
  await expect(page.locator(".statusStrip")).toContainText("Loaded Audit Events");
  await expect(page.locator(".statusStrip")).toContainText("Denied Operations");
  await expect(page.locator(".statusStrip")).toContainText("High-Impact Operations");
  await expect(page.getByText("Recent Denied Actions")).toBeVisible();
  await expect(page.getByText("High-Impact Operations").first()).toBeVisible();
  await expect(page.getByText("PostgreSQL Scoped Role Safeguards")).toBeVisible();
  await expect(page.getByText("NOSUPERUSER")).toBeVisible();
  await expect(page.getByText("NOBYPASSRLS")).toBeVisible();
  await expect(page.getByText("Break-Glass & Safety Policy")).toBeVisible();
  await page.screenshot({ fullPage: true, path: "../../.tmp/ui-p09b/security-overview-1440x900.png" });

  // 2. Proves NO synthetic compliance score, security score, risk score, grade, or percentage exists
  await expect(page.locator("body")).not.toContainText(/compliance score/i);
  await expect(page.locator("body")).not.toContainText(/security score/i);
  await expect(page.locator("body")).not.toContainText(/risk score/i);
  await expect(page.locator("body")).not.toContainText(/compliance grade/i);

  // 3. Audit Tab loads and renders factual actor, target, timestamp, and outcome
  await page.getByRole("tab", { name: "Audit", exact: true }).click();
  await expect(page).toHaveURL(/view=security&tab=audit/);
  await expect(page.getByRole("heading", { name: "Audit", exact: true })).toBeVisible();
  await expect(page.getByText("7 loaded event(s)")).toBeVisible();

  // 4. Actor and outcome visibility (Human vs Machine)
  await expect(page.getByRole("button", { name: /Human actor/ }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: /Machine actor/ }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: /cloud worker/ }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: /CUTOVER_FINALIZED/ })).toBeVisible();

  // 5. Requested vs Succeeded remain distinct without conflation
  const requestedBtn = page.getByRole("button", { name: /CUTOVER_FINALIZE_REQUESTED/ });
  await expect(requestedBtn).toBeVisible();
  await expect(requestedBtn.locator(".status")).toHaveText(/requested/i);

  const finalizedBtn = page.getByRole("button", { name: /CUTOVER_FINALIZED/ });
  await expect(finalizedBtn).toBeVisible();
  await expect(finalizedBtn.locator(".status")).toHaveText(/succeeded|success/i);

  // 6. High-impact operations are clearly identifiable with badge
  await expect(page.locator(".auditList").getByText("HIGH IMPACT").first()).toBeVisible();

  // 7. Selected event detail contains safe metadata and absent secret fields
  await requestedBtn.click();
  const drawer = page.locator(".auditDetail");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole("heading", { name: "CUTOVER_FINALIZE_REQUESTED" })).toBeVisible();
  await expect(drawer.getByText("HIGH IMPACT")).toBeVisible();
  await expect(drawer.getByText("Cutover finalization requested (source revocation)")).toBeVisible();
  await expect(drawer.getByText("aud-cutover-req")).toBeVisible();
  await expect(drawer.getByText("app-payment-db")).toBeVisible();
  await expect(drawer.getByText("pvc-uid-999")).toBeVisible();
  await expect(drawer.getByText("digest-safe-hash")).toBeVisible();

  // 8. Secret-like payload fields absent from browser DOM
  await expect(page.locator("body")).not.toContainText("SUPER_SECRET_PAYLOAD_CANARY");
  await expect(page.locator("body")).not.toContainText("postgres://user:secretpassword@");
  await expect(page.locator("body")).not.toContainText("PRIVATE_KEY_CANARY");

  // 9. Denied event represented accurately
  const deniedBtn = page.getByRole("button", { name: /RBAC_DENIED/ });
  await expect(deniedBtn).toBeVisible();
  await expect(deniedBtn.locator(".status")).toHaveText(/denied/i);
  await deniedBtn.click();
  await expect(drawer.getByRole("heading", { name: "RBAC_DENIED" })).toBeVisible();

  // 10. Filtering works factually
  await page.getByLabel("Search audit trail").fill("cutover");
  await expect(page.getByRole("button", { name: /CUTOVER_/ })).toHaveCount(2);
  await page.getByLabel("Search audit trail").fill("");

  await page.getByRole("combobox", { name: "Outcome" }).selectOption("denied");
  await expect(page.getByRole("button", { name: /RBAC_DENIED/ })).toHaveCount(1);
  await page.getByRole("combobox", { name: "Outcome" }).selectOption("");

  // 11. Cross-surface links work
  const serviceEventBtn = page.getByRole("button", { name: /SERVICE_CREATED/ });
  await expect(serviceEventBtn).toBeVisible();
  await serviceEventBtn.click();
  const crossLink = drawer.getByRole("button", { name: /Open Application/i });
  await expect(crossLink).toBeVisible();
  await crossLink.click();
  await expect(page).toHaveURL(/view=deploy/);

  // 12. Access Tab loads in strictly read-only mode
  await page.goto("/?project=proj-1&view=security&tab=access");
  await expect(page.getByRole("heading", { name: "Access & Identities", exact: true })).toBeVisible();
  await expect(page.getByText("Authenticated Session")).toBeVisible();
  await expect(page.getByText("Authority Connections")).toBeVisible();
  await expect(page.getByText("Connected Nodes & Machine Authorities")).toBeVisible();
  await expect(page.getByText("Server Bootstrap Sessions")).toBeVisible();
  await expect(page.getByText("Application Credential Status")).toBeVisible();

  // 13. Strictly NO Reveal / Rotate / Create Secret / TOTP controls exist
  await expect(page.getByRole("button", { name: /Reveal Secret/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Rotate Secret/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Create Secret/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Set up TOTP/i })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: /Operation/i })).toHaveCount(0);
  await expect(page.getByLabel(/TOTP code/i)).toHaveCount(0);
  await expect(page.getByLabel(/OTP code/i)).toHaveCount(0);
  await expect(page.getByLabel(/Secret name/i)).toHaveCount(0);

  // 14. Access tab cross-links work
  await expect(page.getByRole("button", { name: /Open Server/i }).first()).toBeVisible();
  await page.getByRole("button", { name: /Open Server/i }).first().click();
  await expect(page).toHaveURL(/view=observability&tab=servers/);

  // 15. No destructive action buttons duplicated in Security
  await page.goto("/?project=proj-1&view=security&tab=access");
  await expect(page.getByRole("button", { name: /Delete Server/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Destroy Storage/i })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Delete Resource/i })).toHaveCount(0);

  // 16. Responsive viewports without horizontal overflow
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 1024, height: 768 },
    { width: 390, height: 844 },
    { width: 320, height: 800 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/?project=proj-1&view=security&tab=overview");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await page.goto("/?project=proj-1&view=security&tab=audit");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);

    await page.goto("/?project=proj-1&view=security&tab=access");
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  }
});

async function mockSecurityAPI(page: Page) {
  await page.unroute("**/api/local/**").catch(() => undefined);
  await page.route("**/api/local/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const projectID = path.match(/projects\/([^/]+)/)?.[1] ?? "proj-1";

    if (path === "/api/local/session") {
      return json(route, {
        authenticated: true,
        cloud_connected: "ok",
        agent_connected: "ok",
        org_id: "org-security",
        project_id: projectID,
        role: "admin",
        user_id: "security-auditor@opsi.dev",
        capabilities: [],
      });
    }
    if (path === "/api/local/projects") {
      return json(route, {
        projects: [
          { id: "proj-1", org_id: "org-security", name: "Checkout Security Platform", slug: "checkout", status: "ready" },
          { id: "proj-2", org_id: "org-security", name: "Payments Vault", slug: "payments", status: "ready" },
        ],
      });
    }
    if (path.endsWith("/readiness")) return json(route, { project_id: projectID, status: "ready", can_deploy: true });
    if (path.endsWith("/services")) {
      return json(route, {
        services: [
          { id: "svc-web", name: "web", type: "application", status: "ready", source_type: "image", replicas: 2 },
          { id: "svc-api", name: "api", type: "application", status: "ready", source_type: "image", replicas: 2 },
        ],
      });
    }
    if (path.endsWith("/nodes")) {
      return json(route, {
        nodes: [{ id: "node-1", name: "security-node-1", role: "worker", status: "healthy", last_seen_at: "2026-08-01T10:00:00Z" }],
      });
    }
    if (path.endsWith("/deployments")) return json(route, { deployments: [] });
    if (path.endsWith("/bootstrap-sessions")) {
      return json(route, {
        sessions: [
          { id: "boot-1", status: "installing", public_host: "203.0.113.10", role: "worker", attempt_count: 1, max_attempts: 3, created_at: "2026-08-01T08:00:00Z" },
        ],
      });
    }
    if (path.endsWith("/audit")) {
      return json(route, { events: factualAuditFixture });
    }
    if (path.endsWith("/support")) {
      return json(route, {
        generated_at: "2026-08-01T10:00:00Z",
        readiness: { project_id: projectID, status: "ready", can_deploy: true },
        counts: { nodes: 1, healthy_nodes: 1, services: 2, deployment_jobs: 0, failed_deployments: 0, bootstrap_sessions: 1, open_bootstrap_jobs: 1, audit_events: factualAuditFixture.length },
        dashboard: { title: "Security", datasource: "local", refresh: "30s", panels: [] },
        signals: [],
        active_alerts: [],
        configured_alerts: [],
        production_gates: [],
        break_glass_policy: {
          time_limited: true,
          approval_required: true,
          reason_required: true,
          audited: true,
          secret_reveal_by_default: false,
          owner_notification: "required",
        },
        runbooks: [],
      });
    }
    if (path.endsWith("/topology/facts")) {
      return json(route, { project_id: projectID, environments: [], runtimes: [], nodes: [], agents: [], services: [] });
    }
    if (path.endsWith("/topology")) {
      return json(route, { schema_version: "opsi.topology_plan/v1", id: "topo-1", project_id: projectID, revision: 1, state_hash: "state", plan_hash: "plan", created_by: "user", applied_by: "user", created_at: "2026-08-01T10:00:00Z", applied_at: "2026-08-01T10:00:00Z", assignments: [] });
    }
    if (path.endsWith("/build-records")) return json(route, { records: [] });
    if (path.endsWith("/deployment-policies")) return json(route, { policies: [] });
    if (path.endsWith("/telemetry/summary")) {
      return json(route, { project_id: projectID, source: "agent", payload_policy: "redacted", end_unix: 1785290900, health: "healthy" });
    }
    if (path.includes("/telemetry/services/")) {
      return json(route, { project_id: projectID, source: "agent", payload_policy: "redacted", services: [] });
    }
    if (path.endsWith("/incidents")) return json(route, { source: "agent", payload_policy: "redacted", incidents: [] });

    return json(route, {});
  });
}

async function json(route: Route, body: unknown, status = 200) {
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status });
}

const factualAuditFixture = [
  {
    id: "aud-service-create",
    actor_user_id: "alice@opsi.dev",
    actor_type: "human",
    action: "SERVICE_CREATED",
    resource_type: "service",
    resource_id: "svc-web",
    result: "success",
    created_at: "2026-08-01T08:00:00Z",
    metadata_redacted: { request_id: "req-svc-1", service_id: "svc-web" },
  },
  {
    id: "aud-rbac-denied",
    actor_user_id: "dev-user@opsi.dev",
    actor_type: "human",
    action: "RBAC_DENIED",
    resource_type: "bootstrap_session",
    resource_id: "boot-99",
    result: "denied",
    created_at: "2026-08-01T08:30:00Z",
    metadata_redacted: { request_id: "req-rbac-1", required_role: "admin", user_role: "developer" },
  },
  {
    id: "aud-node-remove",
    actor_user_id: "operator@opsi.dev",
    actor_type: "human",
    action: "NODE_LIFECYCLE_REQUESTED",
    resource_type: "node",
    resource_id: "node-legacy",
    result: "success",
    created_at: "2026-08-01T09:00:00Z",
    metadata_redacted: { request_id: "req-node-1", lifecycle_action: "remove", node_ip: "10.0.0.5" },
  },
  {
    id: "aud-machine-deploy",
    actor_type: "agent",
    action: "TOPOLOGY_PLAN_APPLIED",
    resource_type: "topology_plan",
    resource_id: "topo-1",
    result: "success",
    created_at: "2026-08-01T09:15:00Z",
    metadata_redacted: { request_id: "req-topo-1", revision: 1, plan_hash: "plan-hash-123" },
  },
  {
    id: "aud-system-sync",
    actor_type: "system",
    action: "MACHINE_STATE_SYNC",
    resource_type: "node",
    resource_id: "node-1",
    result: "success",
    created_at: "2026-08-01T09:30:00Z",
    metadata_redacted: { request_id: "req-sys-1" },
  },
  {
    id: "aud-cutover-req",
    actor_user_id: "lead-db-admin@opsi.dev",
    actor_type: "human",
    action: "CUTOVER_FINALIZE_REQUESTED",
    resource_type: "cutover_finalization",
    resource_id: "fin-1",
    result: "requested",
    created_at: "2026-08-01T09:45:00Z",
    metadata_redacted: {
      request_id: "req-cutover-1",
      target_binding_id: "app-payment-db",
      pvc_uid: "pvc-uid-999",
      digest: "digest-safe-hash",
      // Sensitive fields that MUST be redacted
      DATABASE_PASSWORD: "SUPER_SECRET_PAYLOAD_CANARY",
      database_url: "postgres://user:secretpassword@db.internal:5432/db",
      private_key: "fake-rsa-private-key-material-for-test",
    },
  },
  {
    id: "aud-cutover-done",
    actor_type: "worker",
    action: "CUTOVER_FINALIZED",
    resource_type: "cutover_finalization",
    resource_id: "fin-1",
    result: "success",
    created_at: "2026-08-01T09:47:00Z",
    metadata_redacted: { request_id: "req-cutover-2", duration_ms: 1250 },
  },
];
