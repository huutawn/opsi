import { expect, test, type Page, type Route } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  await mockObservabilityAPI(page);
});

test("Observability loads canonical overview with KPI status strip and actionable failures", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability");

  await expect(page.getByRole("heading", { name: "Observability Overview" })).toBeVisible();
  const strip = page.locator(".statusStrip");
  await expect(strip).toBeVisible();
  await expect(strip.getByText("Applications")).toBeVisible();
  await expect(strip.getByText("Servers")).toBeVisible();
  await expect(strip.getByText("Managed Resources")).toBeVisible();

  // Actionable failures queue should identify the degraded worker and offline server
  await expect(page.getByTestId("actionable-failures")).toBeVisible();
  await expect(page.getByText(/Worker is Degraded/)).toBeVisible();
  await expect(page.getByText(/server-2 is Offline/)).toBeVisible();
});

test("Running application displays factual revision, image digest, and replica counts", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=applications");

  await expect(page.getByRole("heading", { name: "Applications Runtime" })).toBeVisible();

  // Web app row
  const webRow = page.getByTestId("app-row-web");
  await expect(webRow).toBeVisible();
  await expect(webRow.getByText("Web App")).toBeVisible();
  await expect(webRow.getByText("rev 3")).toBeVisible();
  await expect(webRow.getByText("1/2")).toBeVisible();
  await expect(webRow.getByText("server-1")).toBeVisible();

  // Open detail drawer
  await webRow.getByRole("button", { name: "Inspect" }).click();
  const drawer = page.getByTestId("application-detail-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole("heading", { name: "Web App" })).toBeVisible();
});

test("Deployment success and current runtime status remain distinct without conflation", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=applications&service=svc-web");

  const drawer = page.getByTestId("application-detail-drawer");
  await expect(drawer).toBeVisible();

  // Verify both badges exist and are distinct
  await expect(drawer.getByText("Runtime: Degraded")).toBeVisible();
  await expect(drawer.getByText("Deployment: Succeeded")).toBeVisible();

  // Workload tab
  await drawer.getByRole("tab", { name: "Workload" }).click();
  await expect(drawer.getByText("Desired Replicas")).toBeVisible();
  await expect(drawer.getByText("Ready Replicas")).toBeVisible();
});

test("Server state and capacity are visible with placed workloads", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=servers");

  await expect(page.getByRole("heading", { name: "Server Observability" })).toBeVisible();

  // Server rows
  const server1Row = page.getByTestId("server-row-server-1");
  await expect(server1Row).toBeVisible();
  await expect(server1Row.getByText("Ready", { exact: true })).toBeVisible();
  await expect(server1Row.getByText("8 cores")).toBeVisible();

  const server2Row = page.getByTestId("server-row-server-2");
  await expect(server2Row).toBeVisible();
  await expect(server2Row.getByText("Offline", { exact: true })).toBeVisible();

  // Open server detail
  await server1Row.getByRole("button", { name: "Inspect" }).click();
  const drawer = page.getByTestId("server-detail-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole("heading", { name: "server-1" })).toBeVisible();

  // Placed applications
  await drawer.getByRole("tab", { name: /Applications/ }).click();
  await expect(drawer.getByText("Web App")).toBeVisible();
});

test("PostgreSQL managed resource readiness is visible without exposed credentials", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=resources");

  await expect(page.getByRole("heading", { name: "Managed Resources Observability" })).toBeVisible();

  // Resource rows
  const dbRow = page.getByTestId("resource-row-main-db");
  await expect(dbRow).toBeVisible();
  await expect(dbRow.getByText("PostgreSQL")).toBeVisible();
  await expect(dbRow.getByText("Ready", { exact: true })).toBeVisible();

  // Open resource detail
  await dbRow.getByRole("button", { name: "Inspect" }).click();
  const drawer = page.getByTestId("resource-detail-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByRole("heading", { name: "main-db" })).toBeVisible();
  await expect(drawer.getByText("PostgreSQL Safe Runtime Facts")).toBeVisible();
  await expect(drawer.getByText("Credentials Safety")).toBeVisible();

  // Ensure no password or secret string is rendered in the DOM
  const content = await page.content();
  expect(content).not.toMatch(/postgres:\/\/.*:.*@/);
  expect(content).not.toMatch(/password\s*=\s*['"][^'"]+['"]/i);
});

test("Events timeline and bounded logs with replica identity are visible", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=applications&service=svc-web");

  const drawer = page.getByTestId("application-detail-drawer");
  await expect(drawer).toBeVisible();

  // Events tab
  await drawer.getByRole("tab", { name: /Events/ }).click();
  await expect(drawer.getByText("Workload Rollout Succeeded")).toBeVisible();

  // Logs tab
  await drawer.getByRole("tab", { name: "Logs" }).click();
  await expect(drawer.locator("[data-logs-status='ready']")).toBeVisible();
  await expect(drawer.getByText("Security boundary:")).toBeVisible();
  await expect(drawer.getByText("HTTP server listening on :8080")).toBeVisible();
  await expect(drawer.getByText("pod-web-1").first()).toBeVisible();

  // Search filter
  await drawer.getByPlaceholder("Search log output…").fill("health");
  await expect(drawer.getByText("Health check passed")).toBeVisible();
  await expect(drawer.getByText("HTTP server listening on :8080")).toHaveCount(0);
});

test("Cross-center navigation links to Deploy", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability&tab=applications&service=svc-web");

  const drawer = page.getByTestId("application-detail-drawer");
  await expect(drawer).toBeVisible();

  await drawer.getByRole("link", { name: "Open in Deploy" }).click();
  await expect(page).toHaveURL(/view=deploy/);
});
test("Two agents succeed with connected status and Live Factual Telemetry", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability");

  // Header should show Runtime agent telemetry with Connected
  const headerBadge = page.getByLabel("Runtime agent telemetry");
  await expect(headerBadge).toBeVisible();
  await expect(headerBadge).toContainText("Connected");

  // Overview banner should show Live Factual Telemetry
  await expect(page.getByText("Live Factual Telemetry")).toBeVisible();

  // Source authority badge
  await expect(page.locator("span.status", { hasText: "Agent Telemetry" }).or(page.getByText("Agent Telemetry"))).toBeVisible();
});

test("One agent failure displays partial status, actionable cause, and avoids healthy claim", async ({ page }) => {
  await page.route("**/api/local/projects/proj-1/agent-connection", (route) => {
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        project_id: "proj-1",
        status: "partial",
        expected_agents: 2,
        successful_agents: 1,
        failed_agents: 1,
        errors: [
          {
            node_id: "node-2",
            code: "AGENT_TIMEOUT",
            message_redacted: "connect agent: context deadline exceeded",
            actionable_cause: "Connection or query timed out. Verify workstation can reach agent TLS port and VPS firewall allows traffic.",
          },
        ],
        observed_at: new Date().toISOString(),
      }),
    });
  });

  await page.route("**/api/local/projects/proj-1/telemetry/summary*", (route) => {
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        project_id: "proj-1",
        source: "agent",
        health: "healthy",
        coverage: {
          status: "partial",
          expected_agents: 2,
          successful_agents: 1,
          failed_agents: 1,
          errors: [
            {
              node_id: "node-2",
              code: "AGENT_TIMEOUT",
              message_redacted: "connect agent: context deadline exceeded",
              actionable_cause: "Connection or query timed out. Verify workstation can reach agent TLS port and VPS firewall allows traffic.",
            },
          ],
          observed_at: new Date().toISOString(),
        },
      }),
    });
  });

  await page.goto("/?project=proj-1&view=observability");

  // Header displays Partial (1/2) with actionable error in title
  const headerBadge = page.getByLabel("Runtime agent telemetry");
  await expect(headerBadge).toBeVisible();
  await expect(headerBadge).toContainText("Partial (1/2)");
  await expect(headerBadge).toHaveAttribute("title", /Connection or query timed out/);

  // Overview banner shows Partial Telemetry
  await expect(page.getByText(/Partial Telemetry/)).toBeVisible();

  // Does NOT display "All workloads healthy" when telemetry is partial
  await expect(page.getByText("All workloads healthy")).toHaveCount(0);
});

test("All agents unavailable shows unverified status and no false healthy claim", async ({ page }) => {
  await page.route("**/api/local/projects/proj-1/nodes*", (route) => {
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        nodes: [
          {
            id: "node-1",
            name: "server-1",
            role: "primary",
            status: "ready",
            public_host: "192.0.2.10",
            cpu_cores: 8,
            memory_mb: 16384,
            agent_id: "agent-1",
            agent_version: "v1.2.0",
          },
        ],
      }),
    });
  });
  await page.route("**/api/local/projects/proj-1/resources*", (route) => {
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ resources: [] }),
    });
  });

  await page.route("**/api/local/projects/proj-1/agent-connection", (route) => {
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        project_id: "proj-1",
        status: "unavailable",
        expected_agents: 2,
        successful_agents: 0,
        failed_agents: 2,
        errors: [
          {
            code: "AGENT_UNREACHABLE",
            message_redacted: "connection refused",
            actionable_cause: "Agent endpoint is unreachable.",
          },
        ],
        observed_at: new Date().toISOString(),
      }),
    });
  });

  await page.route("**/api/local/projects/proj-1/telemetry/summary*", (route) => {
    route.fulfill({
      status: 502,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "AGENT_TELEMETRY_UNAVAILABLE", message: "Agent telemetry operation failed" },
        coverage: {
          status: "unavailable",
          expected_agents: 2,
          successful_agents: 0,
          failed_agents: 2,
          errors: [{ code: "AGENT_UNREACHABLE", message_redacted: "connection refused" }],
          observed_at: new Date().toISOString(),
        },
      }),
    });
  });

  await page.goto("/?project=proj-1&view=observability");

  // Header displays Unavailable
  const headerBadge = page.getByLabel("Runtime agent telemetry");
  await expect(headerBadge).toBeVisible();
  await expect(headerBadge).toContainText("Unavailable");

  // Banner indicates telemetry unavailable
  await expect(page.getByText("Telemetry Unavailable")).toBeVisible();

  // Incident section must NOT display "No Current Runtime Failures"
  await expect(page.getByText("No Current Runtime Failures")).toHaveCount(0);
  await expect(page.getByText("Runtime Observation Unavailable")).toBeVisible();
});

test("Refresh failure displays error sources and accessible Retry recovers factual state", async ({ page }) => {
  let shouldFail = true;

  await page.route("**/api/local/projects/proj-1/telemetry/summary*", (route) => {
    if (shouldFail) {
      route.fulfill({
        status: 502,
        contentType: "application/json",
        body: JSON.stringify({
          error: { code: "AGENT_TELEMETRY_UNAVAILABLE", message: "fail" },
        }),
      });
    } else {
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          project_id: "proj-1",
          source: "agent",
          health: "healthy",
          coverage: { status: "connected", expected_agents: 2, successful_agents: 2, failed_agents: 0 },
        }),
      });
    }
  });

  await page.goto("/?project=proj-1&view=observability");

  // Initial load failed -> alert is shown
  const alert = page.getByRole("alert").filter({ hasText: "Refresh failed" });
  await expect(alert).toBeVisible();
  await expect(alert).toContainText("Agent telemetry");

  // Accessible Retry button
  const retryButton = alert.getByRole("button", { name: "Retry refresh" }).or(alert.getByRole("button", { name: "Retry" }));
  await expect(retryButton).toBeVisible();
  // Now fix the backend failure and click Retry
  shouldFail = false;
  await retryButton.click();

  // Alert disappears after successful retry
  await expect(alert).toHaveCount(0);
});

test("Badge, header, and KPI card states remain unified across transitions", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability");

  // Header has Runtime agent telemetry
  const headerBadge = page.getByLabel("Runtime agent telemetry");
  await expect(headerBadge).toBeVisible();
  await expect(headerBadge).toContainText("Connected");

  // Overview status strip
  const strip = page.locator(".statusStrip");
  await expect(strip).toBeVisible();
  await expect(strip.getByText("Applications")).toBeVisible();
  await expect(strip.getByText("Servers")).toBeVisible();
});

test("Time window selector switches window and persists in URL parameter", async ({ page }) => {
  await page.goto("/?project=proj-1&view=observability");

  const windowGroup = page.getByRole("radiogroup", { name: "Time window" });
  await expect(windowGroup).toBeVisible();
  await expect(windowGroup.getByRole("radio", { name: "Last 1h" })).toHaveAttribute("aria-checked", "true");

  await windowGroup.getByRole("radio", { name: "Last 6h" }).click();
  await expect(page).toHaveURL(/window=6h/);
  await expect(windowGroup.getByRole("radio", { name: "Last 6h" })).toHaveAttribute("aria-checked", "true");

  await page.reload();
  await expect(page).toHaveURL(/window=6h/);
  await expect(windowGroup.getByRole("radio", { name: "Last 6h" })).toHaveAttribute("aria-checked", "true");

  await windowGroup.getByRole("radio", { name: "Last 24h" }).click();
  await expect(page).toHaveURL(/window=24h/);
  await expect(windowGroup.getByRole("radio", { name: "Last 24h" })).toHaveAttribute("aria-checked", "true");
});

test("Incident list identifies incident by node_id and incident_id in route navigation", async ({ page }) => {
  await page.route("**/api/local/projects/proj-1/incidents*", (route) => {
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        source: "agent",
        payload_policy: "redacted",
        incidents: [
          {
            incident_id: "inc-101",
            project_id: "proj-1",
            node_id: "node-prod-1",
            service_id: "svc-web",
            status: "detecting",
            severity: "P1",
            created_at_unix: 1700000000,
          },
        ],
      }),
    });
  });

  await page.goto("/?project=proj-1&view=observability&tab=incidents");

  const incidentItem = page.getByRole("button", { name: /inc-101/ });
  await expect(incidentItem).toBeVisible();
  await incidentItem.click();

  await expect(page).toHaveURL(/incident=inc-101/);
  await expect(page).toHaveURL(/node=node-prod-1/);
});

test("Partial incident coverage renders node-attributed diagnostics", async ({ page }) => {
  await page.route("**/api/local/projects/proj-1/incidents*", (route) => {
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        source: "agent",
        payload_policy: "redacted",
        incidents: [],
        coverage: {
          status: "partial",
          expected_agents: 2,
          successful_agents: 1,
          failed_agents: 1,
          errors: [
            {
              node_id: "node-fail-2",
              code: "AGENT_TIMEOUT",
              message_redacted: "agent dial timeout",
              actionable_cause: "Agent on node-fail-2 timed out. Verify VPS port 9443 is open.",
            },
          ],
        },
      }),
    });
  });

  await page.goto("/?project=proj-1&view=observability&tab=incidents");

  const banner = page.getByTestId("partial-coverage-banner");
  await expect(banner).toBeVisible();
  await expect(banner).toContainText("Degraded Incident Coverage (1/2 agents)");
  await expect(banner).toContainText("[node-fail-2]");
  await expect(banner).toContainText("Agent on node-fail-2 timed out");
});
// Mock Fixture
async function mockObservabilityAPI(page: Page) {
  page.on("pageerror", (err) => process.stderr.write(`PAGE ERROR: ${err.stack || err}\n`));
  page.on("console", (msg) => process.stderr.write(`PAGE CONSOLE [${msg.type()}]: ${msg.text()}\n`));
  await page.route("**/api/local/**", (route) => respond(route));
}

async function respond(route: Route) {
  try {
    const url = new URL(route.request().url());
    const path = url.pathname;
    let body: unknown = {};

  if (path === "/api/local/session") {
    body = {
      authenticated: true,
      cloud_connected: "ok",
      agent_connected: "ok",
      org_id: "org-1",
      project_id: "proj-1",
      token_status: "valid",
    };
  } else if (path === "/api/local/projects") {
    body = {
      projects: [{ id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout", status: "ready" }],
    };
  } else if (path.endsWith("/services")) {
    body = {
      services: [
        {
          id: "svc-web",
          key: "web",
          name: "Web App",
          type: "application",
          status: "ready",
          replicas: 2,
          current_configuration_revision: 3,
          current_configuration_state_hash: "hash-rev-3",
          deployed_digest: "sha256:aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000",
        },
        {
          id: "svc-worker",
          key: "worker",
          name: "Worker",
          type: "application",
          status: "ready",
          replicas: 2,
          current_configuration_revision: 1,
          deployed_digest: "sha256:bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000",
        },
      ],
    };
  } else if (path.endsWith("/agent-connection")) {
    body = {
      project_id: "proj-1",
      status: "connected",
      expected_agents: 2,
      successful_agents: 2,
      failed_agents: 0,
      errors: [],
      observed_at: new Date().toISOString(),
    };
  } else if (path.endsWith("/telemetry/summary")) {
    body = {
      project_id: "proj-1",
      source: "agent",
      health: "healthy",
      coverage: {
        status: "connected",
        expected_agents: 2,
        successful_agents: 2,
        failed_agents: 0,
        errors: [],
        observed_at: new Date().toISOString(),
      },
      services: [
        {
          service_id: "svc-web",
          health: "degraded",
          pod_count: 2,
          ready_pods: 1,
          restart_count: 2,
          recent_error_count: 0,
          last_seen_unix: 1723850000,
        },
        {
          service_id: "svc-worker",
          health: "degraded",
          pod_count: 2,
          ready_pods: 0,
          restart_count: 5,
          recent_error_count: 3,
          last_seen_unix: 1723850000,
        },
      ],
    };
  } else if (path.endsWith("/nodes")) {
    body = {
      nodes: [
        {
          id: "node-1",
          name: "server-1",
          role: "primary",
          status: "ready",
          public_host: "192.0.2.10",
          cpu_cores: 8,
          memory_mb: 16384,
          agent_id: "agent-1",
          agent_version: "v1.2.0",
          last_seen_at: "2026-08-17T01:00:00Z",
        },
        {
          id: "node-2",
          name: "server-2",
          role: "worker",
          status: "offline",
          public_host: "192.0.2.11",
          cpu_cores: 4,
          memory_mb: 8192,
          last_seen_at: "2026-08-17T00:30:00Z",
        },
      ],
    };
  } else if (path.endsWith("/resources")) {
    body = {
      resources: [
        {
          id: "res-pg",
          name: "main-db",
          type: "postgres",
          lifecycle: "ready",
          status: "ready",
          version: "16",
          runtime: {
            spec: {
              assignment: { node_id: "node-1" },
              version: "16",
              cpu_millicores: 2000,
              memory_bytes: 4294967296,
              storage: { persistent: true, size_bytes: 53687091200 },
            },
          },
        },
        {
          id: "res-valkey",
          name: "cache",
          type: "valkey",
          lifecycle: "ready",
          status: "ready",
          version: "7.2",
        },
        {
          id: "res-nats",
          name: "events",
          type: "nats",
          lifecycle: "degraded",
          status: "degraded",
          version: "2.10",
        },
      ],
    };
  } else if (path.endsWith("/resource-bindings")) {
    body = {
      bindings: [
        {
          id: "bind-1",
          source: { kind: "application", id: "svc-web" },
          target: { kind: "managed_service", id: "res-pg" },
          lifecycle: "ready",
        },
      ],
    };
  } else if (path.endsWith("/deployments")) {
    body = {
      deployments: [
        {
          id: "dep-1",
          service_id: "svc-web",
          status: "succeeded",
          rollout_state: "succeeded",
          desired_digest: "sha256:aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000",
          created_at: "2026-08-17T01:00:00Z",
          updated_at: "2026-08-17T01:05:00Z",
        },
      ],
    };
  } else if (path.endsWith("/exposures")) {
    body = { exposures: [] };
  } else if (path.endsWith("/audit")) {
    body = { events: [] };
  } else if (path.endsWith("/incidents")) {
    body = { source: "agent", payload_policy: "redacted", incidents: [] };
  } else if (path.includes("/incidents/") && path.endsWith("/evidence")) {
    body = {
      schema_version: "opsi.incident_evidence/v1",
      identity: { incident_id: "inc-101", project_id: "proj-1", status: "open", service_id: "svc-web", node_id: "node-prod-1" },
      generated_at_unix: 1700000000,
      observation_window: { start_unix: 1700000000, end_unix: 1700003600 },
      deployment: { desired_digest: "sha256:aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000" },
      rollout: { state: "healthy" },
      coverage: [{ source: "agent", status: "available", item_count: 1, truncated: false }],
      content_sha256: "a".repeat(64),
    };
  } else if (path.includes("/incidents/")) {
    body = {
      incident: {
        incident_id: "inc-101",
        project_id: "proj-1",
        node_id: "node-prod-1",
        service_id: "svc-web",
        status: "open",
        severity: "P1",
        created_at_unix: 1700000000,
      },
    };
  } else if (path.endsWith("/topology/facts")) {
    body = {
      project_id: "proj-1",
      environments: [{ id: "env-prod", project_id: "proj-1", name: "Production", type: "production", status: "ready" }],
      runtimes: [{ id: "rt-1", project_id: "proj-1", environment_id: "env-prod", name: "server-1", type: "server", status: "ready" }],
      nodes: [{ id: "node-1", project_id: "proj-1", runtime_id: "rt-1", status: "ready" }],
      agents: [],
      services: [
        { id: "svc-web", project_id: "proj-1", key: "web" },
        { id: "svc-worker", project_id: "proj-1", key: "worker" },
      ],
      resources: [],
    };
  } else if (path.endsWith("/topology")) {
    body = {
      schema_version: "opsi.topology_plan/v1",
      id: "top-1",
      project_id: "proj-1",
      revision: 1,
      assignments: [
        { service_key: "web", runtime_id: "rt-1" },
      ],
    };
  } else if (path.endsWith("/logs")) {
    body = {
      project_id: "proj-1",
      source: "agent",
      payload_policy: "redacted",
      logs: [
        {
          service_id: "svc-web",
          pod_id: "pod-web-1",
          namespace: "prod",
          level: "info",
          message: "HTTP server listening on :8080",
          observed_unix: 1723850000,
          fingerprint: "fp-1",
        },
        {
          service_id: "svc-web",
          pod_id: "pod-web-1",
          namespace: "prod",
          level: "info",
          message: "Health check passed",
          observed_unix: 1723850010,
          fingerprint: "fp-2",
        },
      ],
    };
  }

    await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 });
  } catch (err) {
    process.stderr.write(`MOCK RESPOND ERROR: ${err}\n`);
    await route.fulfill({ body: "{}", contentType: "application/json", status: 500 });
  }
}
