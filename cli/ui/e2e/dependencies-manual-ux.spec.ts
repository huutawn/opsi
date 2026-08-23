import { expect, test, type Route } from "@playwright/test";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

test.beforeEach(async ({ page }) => {
  watchConsoleErrors(page);
});
test.afterEach(async ({ page }) => {
  expectNoConsoleErrors(page);
});

function hash(char: string) {
  return char.repeat(64);
}

function fixtureData() {
  const services = [
    {
      id: "srv-api",
      name: "api",
      type: "application",
      status: "ready",
      source_type: "dockerfile",
      container_port: 8080,
      health_path: "/healthz",
      configuration: {
        schema_version: "opsi.service_configuration/v1" as const,
        revision: 1,
        state_hash: hash("a"),
        environment: [{ name: "LOG_LEVEL", value: "info" }],
        dependencies: [] as any[],
        resource_bindings: [] as any[],
        bindings: [] as any[],
      },
    },
    {
      id: "srv-worker",
      name: "worker",
      type: "application",
      status: "ready",
      source_type: "dockerfile",
      container_port: 9000,
      health_path: "/ready",
      configuration: {
        schema_version: "opsi.service_configuration/v1" as const,
        revision: 1,
        state_hash: hash("b"),
        environment: [{ name: "LOG_LEVEL", value: "info" }],
        dependencies: [] as any[],
        resource_bindings: [] as any[],
        bindings: [] as any[],
      },
    },
  ];

  const resources = [
    {
      id: "res-pg",
      kind: "managed_service" as const,
      type: "postgresql",
      lifecycle: "active",
      name: "Primary Database",
      version: "16",
      replicas: 1,
      cpu_millicores: 500,
      memory_bytes: 512 * 1024 * 1024,
    },
    {
      id: "res-redis",
      kind: "managed_service" as const,
      type: "valkey",
      lifecycle: "active",
      name: "Session Cache",
      version: "7",
      replicas: 1,
      cpu_millicores: 200,
      memory_bytes: 256 * 1024 * 1024,
    },
  ];

  const runtimes = [
    {
      id: "runtime-primary",
      environment_id: "env-1",
      name: "Primary runtime",
      type: "kubernetes",
      status: "ready",
    },
  ];

  const nodes = [
    {
      id: "node-primary",
      runtime_id: "runtime-primary",
      status: "healthy",
      cpu_cores: 4,
      memory_mb: 8192,
    },
  ];

  const agents = [
    {
      id: "agent-primary",
      runtime_id: "runtime-primary",
      status: "active",
      capabilities: { deploy: true },
    },
  ];

  const topology = {
    schema_version: "opsi.topology_plan/v1" as const,
    id: "topology-1",
    project_id: "proj-1",
    revision: 1,
    state_hash: hash("c"),
    plan_hash: hash("d"),
    created_by: "owner",
    applied_by: "owner",
    created_at: "2026-08-08T07:00:00Z",
    applied_at: "2026-08-08T07:00:00Z",
    assignments: [
      {
        service_key: "api",
        environment_id: "env-1",
        runtime_id: "runtime-primary",
        replicas: 1,
        cpu_request_millicores: 100,
        memory_request_bytes: 128 * 1024 * 1024,
        exposure: { mode: "public" as const, hostname: "api.example.com", path: "/" },
      },
    ],
  };

  const installations = [
    {
      installation_id: 1,
      account_login: "org",
      status: "active",
    },
  ];

  const repositories = [
    {
      repository_id: 1,
      installation_id: 1,
      owner_login: "org",
      name: "api",
      full_name: "org/api",
      status: "active",
      claim_status: "active",
    },
  ];

  const bindings = [
    {
      id: "binding-api",
      project_id: "proj-1",
      service_id: "srv-api",
      service_key: "api",
      repository_id: 1,
      installation_id: 1,
      config_path: "opsi.yaml",
      selected_ref: "refs/heads/main",
      application_root: "/",
      build_context: ".",
      build_strategy: "dockerfile",
      dockerfile_path: "Dockerfile",
      status: "active",
    },
  ];

  const builds = [
    {
      schema_version: "opsi.build_record/v1" as const,
      id: "build-api",
      project_id: "proj-1",
      repository_id: 1,
      repository_owner_id: 1,
      active_binding_id: "binding-api",
      service_id: "srv-api",
      service_key: "api",
      created_at: "2026-08-08T07:30:00Z",
      workload: {
        issuer: "github",
        subject: "repo:org/api:ref:refs/heads/main",
        repository_id: 1,
        repository_owner_id: 1,
        ref: "refs/heads/main",
        sha: hash("2"),
        event_name: "push",
        workflow: "build",
        workflow_ref: "org/api/.github/workflows/build.yml@refs/heads/main",
        run_id: 1,
        run_attempt: 1,
      },
      build: {
        config_hash: hash("8"),
        plan_hash: hash("9"),
        platform: "linux/amd64",
        oci_repository: "registry.test/api",
        oci_digest: "sha256:" + hash("1"),
        status: "succeeded",
      },
    },
  ];

  const deployments = [
    {
      schema_version: "opsi.deployment_job/v1" as const,
      id: "deploy-1",
      project_id: "proj-1",
      environment_id: "env-1",
      runtime_id: "runtime-primary",
      node_id: "node-primary",
      service_id: "srv-api",
      status: "succeeded",
      created_at: "2026-08-08T08:00:00Z",
      updated_at: "2026-08-08T08:05:00Z",
    },
  ];

  return {
    project: { id: "proj-1", org_id: "org-1", name: "Checkout Platform", slug: "checkout-platform", status: "ready" },
    services,
    resources,
    runtimes,
    nodes,
    agents,
    topology,
    installations,
    repositories,
    bindings,
    builds,
    policies: [] as any[],
    deployments,
  };
}

async function handleLocalAPI(route: Route, state: ReturnType<typeof fixtureData>, extraHandlers?: (path: string, route: Route) => Promise<boolean>) {
  const url = new URL(route.request().url());
  const path = url.pathname;

  if (extraHandlers) {
    const handled = await extraHandlers(path, route);
    if (handled) return;
  }

  let body: unknown = {};

  if (path === "/api/local/session") {
    body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", project_id: "proj-1", role: "developer" };
  } else if (path === "/api/local/projects") {
    body = { projects: [state.project] };
  } else if (path.endsWith("/readiness")) {
    body = { project_id: "proj-1", status: "ready", can_deploy: true };
  } else if (path.endsWith("/nodes")) {
    body = { nodes: state.nodes };
  } else if (path.endsWith("/services")) {
    body = { services: state.services };
  } else if (path.endsWith("/deployments")) {
    body = { deployments: state.deployments };
  } else if (path.endsWith("/bootstrap-sessions")) {
    body = { sessions: [] };
  } else if (path.endsWith("/audit")) {
    body = { events: [] };
  } else if (path.endsWith("/support")) {
    body = { generated_at: "2026-08-08T08:00:00Z", counts: {}, signals: [] };
  } else if (path.endsWith("/topology/facts")) {
    body = {
      project_id: "proj-1",
      environments: [{ id: "env-1", name: "Production", type: "prod", status: "active" }],
      runtimes: state.runtimes,
      nodes: state.nodes,
      agents: state.agents,
      services: state.services.map((s) => ({ id: s.id, project_id: "proj-1", key: s.name })),
      resources: state.resources,
    };
  } else if (path.endsWith("/topology")) {
    body = state.topology;
  } else if (path.endsWith("/github/installations")) {
    body = { installations: state.installations };
  } else if (path.endsWith("/github/repositories")) {
    body = { repositories: state.repositories };
  } else if (path.endsWith("/github/bindings")) {
    body = { bindings: state.bindings };
  } else if (path.endsWith("/exposures")) {
    body = { exposures: [] };
  } else if (path.includes("/build-jobs")) {
    body = { build_jobs: [] };
  } else if (path.includes("/build-records")) {
    body = { records: state.builds };
  } else if (path.endsWith("/deployment-policies")) {
    body = { policies: state.policies };
  } else if (path.endsWith("/incidents")) {
    body = { source: "agent", payload_policy: "redacted", incidents: [] };
  } else if (path.includes("/configuration/preview")) {
    const draft = route.request().postDataJSON() as any;
    body = {
      configuration: draft.draft || draft,
      generated_environment: [],
      current_revision: 1,
      current_state_hash: hash("a"),
      draft_state_hash: hash("e"),
    };
  } else if (path.includes("/configuration/validate")) {
    body = { valid: true, issues: [] };
  } else if (path.includes("/configuration/diff")) {
    body = { changes: [{ kind: "dependency", action: "add", name: "app_db" }] };
  } else if (path.includes("/configuration/apply")) {
    const req = route.request().postDataJSON() as any;
    const serviceID = path.split("/").at(-3);
    const s = state.services.find((item) => item.id === serviceID || item.name === serviceID);
    if (s) {
      s.configuration = { ...req.draft, revision: (s.configuration?.revision ?? 1) + 1, state_hash: hash("e") };
    }
    body = { configuration: req.draft, reused: false };
  } else if (path.endsWith("/dependencies/review")) {
    body = {
      dependencies: [
        {
          logical_name: "app_db",
          target_kind: "managed_service",
          target_identity: "res-pg",
          target_display_name: "Primary Database",
          protocol: "postgres",
          required: true,
          injection_phase: "runtime",
          binding_action: "reuse",
          status: "ready",
          message: "Matches declared protocol and target identity",
          projections: [
            {
              env_name: "DATABASE_URL",
              symbolic_source: "connection.url",
              injection_phase: "runtime",
              conflict: false,
            },
          ],
        },
      ],
      realized: [],
    };
  } else if (path.endsWith("/dependencies/apply")) {
    body = {
      status: "applied",
      realized: 1,
    };
  } else if (path.endsWith("/deployments/preflight")) {
    body = {
      valid: true,
      status: "PASSED_WITH_WARNINGS",
      preflight_hash: hash("f"),
      checks: [
        {
          id: "chk-1",
          code: "BUILD_FRESHNESS_VERIFIED",
          severity: "PASS",
          scope_kind: "service",
          scope_id: "srv-api",
          message: "Accepted build artifact matches Git commit.",
        },
        {
          id: "chk-2",
          code: "SOURCE_SUSPICIOUS_DEPENDENCY",
          severity: "WARN",
          scope_kind: "service",
          scope_id: "srv-api",
          message: "Found suspicious package dependency in package.json.",
          remediation_code: "REVIEW_CONFIGURATION",
        },
      ],
    };
  } else if (path.endsWith("/deployments/preview")) {
    body = {
      schema_version: "opsi.deployment_preview/v1",
      eligible: true,
      decision_code: "OK",
      message: "Ready to deploy",
      changes: [],
      resolved_at: new Date().toISOString(),
      snapshot: {
        image: { digest: "sha256:" + hash("1") },
        authority: {
          runtime_id: "runtime-primary",
          service_configuration_revision: 1,
          deployment_policy_revision: 1,
          deployment_policy_hash: hash("p"),
        },
        workload: {
          replicas: 1,
          resources: { requests: { cpu: "100m", memory: "128Mi" } },
          readiness_probe: { path: "/healthz" },
          liveness_probe: { path: "/healthz" },
          environment: [{ name: "LOG_LEVEL", value: "info" }],
        },
      },
      preflight: {
        status: "PASS_WITH_WARNINGS",
        preflight_hash: hash("f"),
        authority_fingerprint: hash("p"),
        checks: [
          {
            id: "chk-1",
            code: "BUILD_FRESHNESS_VERIFIED",
            severity: "PASS",
            scope_kind: "service",
            scope_id: "srv-api",
            message: "Accepted build artifact matches Git commit.",
          },
          {
            id: "chk-2",
            code: "SOURCE_SUSPICIOUS_DEPENDENCY",
            severity: "WARN",
            scope_kind: "service",
            scope_id: "srv-api",
            message: "Found suspicious package dependency in package.json.",
            remediation_code: "REVIEW_CONFIGURATION",
          },
        ],
      },
    };
  } else if (path.includes("/verification")) {
    body = { run: null };
  } else if (path.includes("/dependencies/verify")) {
    body = {
      run: {
        schema_version: "opsi.verification/v1",
        id: "ver-1",
        project_id: "proj-1",
        environment_id: "env-1",
        consumer_application_id: "srv-api",
        dependency_logical_name: "app_db",
        deployment_job_id: "deploy-1",
        config_revision: 1,
        staleness_hash: hash("v"),
        overall_status: "VERIFIED",
        triggered_by: "user",
        started_at: new Date().toISOString(),
        completed_at: new Date().toISOString(),
        provider_health: { status: "HEALTHY", provider_kind: "managed_service", provider_id: "res-pg", message: "Managed PostgreSQL runtime is healthy" },
        contract_resolution: { status: "RESOLVED", injection_complete: true, message: "Injected blueprint matches contract" },
        connection: { status: "VERIFIED", protocol: "postgres", latency_ms: 2, message: "TCP handshake on port 5432 succeeded" },
        consumer_health: { status: "HEALTHY", ready_pods: 1, total_pods: 1, message: "Application process has connected" },
        consumer_assertion: { status: "VERIFIED", assertion_path: "/health/dependencies/database", status_code: 200, expected_code: 200, message: "SELECT 1 check passed" },
      },
    };
  } else if (path.includes("/source-risk-report")) {
    body = {
      schema_version: "opsi.source_risk_report/v1",
      project_id: "proj-1",
      application_id: "srv-api",
      commit_sha: hash("2"),
      analysis_status: "complete",
      scanner_version: "1.0.0",
      files_scanned: 12,
      findings: [
        {
          finding_id: "sr-1",
          rule_id: "SOURCE_EMBEDDED_CREDENTIAL_SUSPECTED",
          severity: "WARN",
          confidence: "MEDIUM",
          category: "credential",
          file: "src/db/database.go",
          line: 14,
          safe_evidence: "const dbPass = \"[REDACTED]\";",
        },
      ],
    };
  }

  await route.fulfill({ json: body, status: 200 });
}

test("Test A: Topology PostgreSQL Edge Creation & Configuration with symbolic mapping", async ({ page }) => {
  const state = fixtureData();
  let savedDraft: any = null;

  await page.route("**/api/local/**", async (route) => {
    if (route.request().url().includes("/configuration/apply")) {
      savedDraft = route.request().postDataJSON()?.draft;
    }
    await handleLocalAPI(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await page.locator(".topologyResourceNode").filter({ hasText: "api" }).first().click({ force: true });

  // Click Add Dependency Contract
  await page.getByRole("button", { name: "Add Dependency Contract" }).click();
  await expect(page.getByRole("heading", { name: "Add Dependency Contract" })).toBeVisible();

  // Fill in dependency fields
  await page.getByLabel(/Logical Dependency Name/).fill("app_db");
  await page.getByLabel("Target Resource / Application *").selectOption("res-pg");

  // Select Conventional Preset
  await page.getByRole("radio", { name: "PostgreSQL variables (PGHOST, etc.)" }).check();

  // Click Review Dependency Contract
  await page.getByRole("button", { name: "Review Dependency Contract" }).click();
  await expect(page.getByText("Validation Passed")).toBeVisible();

  // Apply
  await page.getByRole("button", { name: "Apply Dependency Contract" }).click();
  await expect(page.getByRole("heading", { name: "Add Dependency Contract" })).toHaveCount(0);

  expect(savedDraft).not.toBeNull();
  expect(savedDraft.dependencies).toHaveLength(1);
  expect(savedDraft.dependencies[0].logical_name).toBe("app_db");
  expect(savedDraft.dependencies[0].protocol).toBe("postgres");
  expect(savedDraft.dependencies[0].target_identity).toBe("res-pg");
});

test("Test B: Topology Valkey Edge Creation & Configuration", async ({ page }) => {
  const state = fixtureData();
  let savedDraft: any = null;

  await page.route("**/api/local/**", async (route) => {
    if (route.request().url().includes("/configuration/apply")) {
      savedDraft = route.request().postDataJSON()?.draft;
    }
    await handleLocalAPI(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await page.locator(".topologyResourceNode").filter({ hasText: "api" }).first().click({ force: true });

  await page.getByRole("button", { name: "Add Dependency Contract" }).click();
  await page.getByRole("button", { name: "Valkey / Redis" }).click();
  await page.getByLabel(/Logical Dependency Name/).fill("cache");
  await page.getByLabel("Target Resource / Application *").selectOption("res-redis");

  await page.getByRole("button", { name: "Review Dependency Contract" }).click();
  await expect(page.getByText("Validation Passed")).toBeVisible();
  await page.getByRole("button", { name: "Apply Dependency Contract" }).click();
  await expect(page.getByRole("heading", { name: "Add Dependency Contract" })).toHaveCount(0);

  expect(savedDraft).not.toBeNull();
  expect(savedDraft.dependencies[0]?.protocol).toBe("redis");
});

test("Test C: App-to-App Same-Origin Edge Creation", async ({ page }) => {
  const state = fixtureData();
  let savedDraft: any = null;

  await page.route("**/api/local/**", async (route) => {
    if (route.request().url().includes("/configuration/apply")) {
      savedDraft = route.request().postDataJSON()?.draft;
    }
    await handleLocalAPI(route, state);
  });

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await page.locator(".topologyResourceNode").filter({ hasText: "api" }).first().click({ force: true });

  await page.getByRole("button", { name: "Add Dependency Contract" }).click();
  await page.getByRole("button", { name: "App HTTP" }).click();
  await page.getByLabel(/Logical Dependency Name/).fill("worker_api");
  await page.getByLabel("Target Resource / Application *").selectOption("srv-worker");
  await page.getByRole("button", { name: "Browser Request originates in end-user browser" }).click();
  await page.getByRole("textbox", { name: "/api" }).fill("/api/v1");

  await page.getByRole("button", { name: "Review Dependency Contract" }).click();
  await expect(page.getByText("Validation Passed")).toBeVisible();
  await page.getByRole("button", { name: "Apply Dependency Contract" }).click();
  await expect(page.getByRole("heading", { name: "Add Dependency Contract" })).toHaveCount(0);

  expect(savedDraft).not.toBeNull();
  expect(savedDraft.dependencies[0]?.strategy).toBe("same_origin");
});

test("Test F: Strategy Matrix Validation & Rejection", async ({ page }) => {
  const state = fixtureData();
  await page.route("**/api/local/**", (route) => handleLocalAPI(route, state));

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await page.locator(".topologyResourceNode").filter({ hasText: "api" }).first().click({ force: true });
  await page.getByRole("button", { name: "Add Dependency Contract" }).click();

  await page.getByRole("button", { name: "App HTTP" }).click();
  await page.getByLabel(/Logical Dependency Name/).fill("worker_api");
  await page.getByLabel("Target Resource / Application *").selectOption("srv-worker");

  // In browser context, internal_http is disabled
  await page.getByRole("button", { name: "Browser Request originates in end-user browser" }).click();
  await expect(page.getByRole("button", { name: "Internal HTTP Private cluster networking" })).toBeDisabled();

  // In server context, same_origin is disabled
  await page.getByRole("button", { name: "Server Request originates from deployed workload" }).click();
  await expect(page.getByRole("button", { name: "Same origin Relative route (e.g. /api)" })).toBeDisabled();
});

test("Test G & H: Realization Review Modal, Symbolic Projections, & Explicit Apply", async ({ page }) => {
  const state = fixtureData();
  state.services[0].configuration.dependencies = [
    {
      logical_name: "app_db",
      protocol: "postgres",
      target_kind: "managed_service",
      target_identity: "res-pg",
      required: true,
      injection_phase: "runtime",
      injection_mappings: [{ env_name: "DATABASE_URL", symbolic_source: "connection.url" }],
    },
  ];

  let applied = false;
  await page.route("**/api/local/**", async (route) => {
    if (route.request().url().includes("/dependencies/apply")) applied = true;
    await handleLocalAPI(route, state);
  });

  await page.goto("/?project=proj-1&view=services&service=srv-api&tab=dependencies");
  await expect(page.getByText("app_db", { exact: true })).toBeVisible();
  await expect(page.getByText("Needs setup", { exact: true })).toBeVisible();

  // Click Realize
  await page.getByRole("button", { name: "Review Connection" }).click();
  await expect(page.getByRole("heading", { name: "Review Dependency Realization" })).toBeVisible();
  await expect(page.getByText("Primary Database")).toBeVisible();

  // Apply Realization
  await page.getByRole("button", { name: "Apply Realization & Bind" }).click();
  await expect(page.getByRole("heading", { name: "Review Dependency Realization" })).toHaveCount(0);
  expect(applied).toBe(true);
});

test("Test I & J: Unified Deployment Preflight Review with individual Warning Acknowledgement", async ({ page }) => {
  const state = fixtureData();
  await page.route("**/api/local/**", (route) => handleLocalAPI(route, state));

  await page.goto("/?project=proj-1&view=infrastructure&tab=topology");
  await page.getByRole("button", { name: "Review selected" }).click();

  // Verify Preflight Panel is visible
  await expect(page.getByRole("heading", { name: "Deployment Preflight Passed with Warnings" })).toBeVisible();
  await expect(page.getByText("BUILD_FRESHNESS_VERIFIED")).toBeVisible();
  await expect(page.locator("span").filter({ hasText: /^SOURCE_SUSPICIOUS_DEPENDENCY$/ })).toBeVisible();

  // Deploy should be disabled because warning is unacknowledged
  await expect(page.getByRole("button", { name: "Deploy" })).toBeDisabled();

  // Acknowledge warning
  await page.getByRole("checkbox", { name: /I reviewed SOURCE_SUSPICIOUS_DEPENDENCY/ }).check();

  // Deploy should now be enabled
  await expect(page.getByRole("button", { name: "Deploy" })).toBeEnabled();
});

test("Test L & M: 5-Layer Post-Deploy Dependency Verification", async ({ page }) => {
  const state = fixtureData();
  state.services[0].configuration.dependencies = [
    {
      logical_name: "app_db",
      protocol: "postgres",
      target_kind: "managed_service",
      target_identity: "res-pg",
      required: true,
      injection_phase: "runtime",
    },
  ];
  await page.route("**/api/local/**", (route) => handleLocalAPI(route, state));

  await page.goto("/?project=proj-1&view=services&service=srv-api&tab=dependencies");
  await page.getByRole("button", { name: "Verify Dependency" }).click();

  await expect(page.getByText("Layered Post-Deploy Verification")).toBeVisible();
  await expect(page.getByText("1. Upstream Provider Health")).toBeVisible();
  await expect(page.getByText("2. Contract Resolution & Injection")).toBeVisible();
  await expect(page.getByText("3. Protocol Connectivity Probe")).toBeVisible();
  await expect(page.getByText("4. Consumer Workload Readiness")).toBeVisible();
  await expect(page.getByText("5. Application-Level Consumer Assertion")).toBeVisible();
  await expect(page.getByText("VERIFIED").first()).toBeVisible();
});

test("Test N: Static Source Risk Warnings Scan Presentation", async ({ page }) => {
  const state = fixtureData();
  await page.route("**/api/local/**", (route) => handleLocalAPI(route, state));

  await page.goto("/?project=proj-1&view=services&service=srv-api&tab=source");
  await expect(page.getByText("Source Risk Warnings (ADC-05)")).toBeVisible();
  await expect(page.getByText("SOURCE_EMBEDDED_CREDENTIAL_SUSPECTED")).toBeVisible();
  await expect(page.getByText("[REDACTED]")).toBeVisible();
  await expect(page.getByText("src/db/database.go:14")).toBeVisible();
});

test("Proposal review persists a human rejection without configuration or delivery mutation", async ({ page }) => {
  const state = fixtureData();
  let created = 0;
  let rejected = 0;
  let applied = 0;
  const review = {
    id: "review-1", project_id: "proj-1", environment_id: "env-1", application_id: "srv-api", kind: "dependency",
    status: "review_required", proposal_hash: hash("p"), analysis_inputs_hash: hash("i"), normalized_payload: {}, reviewed_payload_hash: hash("e"),
    expected_configuration_revision: 1, expected_configuration_state_hash: hash("a"), created_by: "human-1", created_at: "2026-08-08T08:00:00Z", expires_at: "2026-08-09T08:00:00Z",
  };
  await page.route("**/api/local/**", (route) => handleLocalAPI(route, state, async (path, candidate) => {
    if (path.endsWith("/services/srv-api/proposal-reviews") && candidate.request().method() === "POST") {
      created++;
      await candidate.fulfill({ json: review, status: 201 });
      return true;
    }
    if (path.endsWith("/proposal-reviews/review-1/reject")) {
      rejected++;
      await candidate.fulfill({ json: { ...review, status: "rejected", rejected_by: "human-1", rejected_at: "2026-08-08T08:01:00Z" } });
      return true;
    }
    if (path.endsWith("/proposal-reviews/review-1/apply")) {
      applied++;
      await candidate.fulfill({ json: review });
      return true;
    }
    return false;
  }));

  const proposal = {
    project_id: "proj-1", environment_id: "env-1", application_id: "srv-api",
    provenance: { source_commit: hash("c").slice(0, 40), application_root: "api", analysis_inputs_hash: hash("i") },
    candidate: { logical_name: "database", dependency_kind: "managed_service", target_id: "res-pg", protocol: "postgres", phase: "runtime", required: true, mappings: [{ env_name: "DATABASE_URL", symbolic_source: "connection.url" }] },
    evidence: [{ type: "source", file: "main.go", line: 10, reason: "DATABASE_URL is read" }], confidence: "HIGH",
  };
  await page.goto("/?project=proj-1&view=services&service=srv-api&tab=dependencies");
  await page.getByRole("button", { name: "Review proposal" }).click();
  await page.getByLabel("Proposal review envelope").fill(JSON.stringify(proposal));
  await page.getByRole("dialog", { name: "Review AI proposal" }).getByRole("button", { name: "Review proposal", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Review AI proposal" })).toBeVisible();
  await expect(page.getByText("Human review")).toBeVisible();
  await expect(page.getByText("Current → proposed")).toBeVisible();
  await page.screenshot({ path: "../../.tmp/evidence/mcp04/20260823T121249Z/dependency-review.png" });
  await page.getByRole("dialog", { name: "Review AI proposal" }).getByRole("button", { name: "Reject", exact: true }).click();
  await expect(page.getByText("Dependency proposal rejected. No configuration, build, deployment, or verification state was changed.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Apply Dependency Change" })).toBeDisabled();
  expect(created).toBe(1);
  expect(rejected).toBe(1);
  expect(applied).toBe(0);
});

test("Source proposal review is Copy Patch only and persists no source-write action", async ({ page }) => {
  const state = fixtureData();
  let created = 0;
  const review = {
    id: "review-source-1", project_id: "proj-1", environment_id: "env-1", application_id: "srv-api", kind: "source_patch",
    status: "review_required", proposal_hash: hash("p"), analysis_inputs_hash: hash("i"), normalized_payload: {}, reviewed_payload_hash: hash("e"),
    created_by: "human-1", created_at: "2026-08-08T08:00:00Z", expires_at: "2026-08-09T08:00:00Z",
  };
  await page.route("**/api/local/**", (route) => handleLocalAPI(route, state, async (path, candidate) => {
    if (path.endsWith("/services/srv-api/proposal-reviews") && candidate.request().method() === "POST") {
      created++;
      await candidate.fulfill({ json: review, status: 201 });
      return true;
    }
    return false;
  }));
  const patch = {
    project_id: "proj-1", environment_id: "env-1", application_id: "srv-api",
    provenance: { build_record_id: "build-api", source_commit: hash("c").slice(0, 40), application_root: "api", analysis_inputs_hash: hash("i") },
    rationale: { observed_source: "DATABASE_URL is read", opsi_facts: "dependency missing", inference: "add configuration" },
    files: [{ path: "main.go", base_blob_sha: hash("d").slice(0, 40), unified_diff: "@@ -1 +1 @@\n-old\n+new" }],
  };
  await page.goto("/?project=proj-1&view=services&service=srv-api&tab=source");
  await page.getByRole("button", { name: "Review proposal" }).click();
  await page.getByLabel("Proposal review envelope").fill(JSON.stringify(patch));
  await page.getByRole("dialog", { name: "Review AI proposal" }).getByRole("button", { name: "Review proposal", exact: true }).click();
  await expect(page.getByText("Opsi does not currently modify source repositories. Copy Patch is the only source action.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Copy Patch" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Apply Dependency Change/ })).toHaveCount(0);
  await page.screenshot({ path: "../../.tmp/evidence/mcp04/20260823T121249Z/source-review.png" });
  expect(created).toBe(1);
});
