import { chromium } from '@playwright/test';
import fs from 'fs';
import path from 'path';

(async () => {
  const browser = await chromium.launch();
  const outDir = path.join(process.cwd(), '../../.tmp/ui-v2/actual');
  if (!fs.existsSync(outDir)) {
    fs.mkdirSync(outDir, { recursive: true });
  }

  // 1. Screenshot Login Page
  {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();

    await page.route('**/api/local/session*', async route => {
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          authenticated: false,
          cloud_connected: "ok",
          agent_connected: "ok"
        })
      });
    });

    await page.route('**/api/local/**', async route => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
    });

    await page.goto('http://localhost:3001');
    await page.waitForTimeout(1000);
    await page.screenshot({ path: path.join(outDir, 'login.png') });
    console.log("Captured login.png");
    await context.close();
  }

  // 2. Screenshot Authenticated Views
  {
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
    const page = await context.newPage();

    page.on('console', msg => {
      if (msg.type() === 'error') console.log('BROWSER ERROR:', msg.text());
    });
    page.on('pageerror', err => console.log('PAGE EXCEPTION:', err.message));

    const mockProject = {
      id: "core-infra",
      name: "Core-Infra",
      slug: "core-infra",
      role: "admin",
      environments: [
        { id: "env-prod", name: "Production", status: "active" },
        { id: "env-staging", name: "Staging", status: "active" },
      ]
    };

    const mockNodes = [
      { id: "node-1", name: "node-wrk-01", status: "healthy", role: "worker", ip: "10.42.1.15", region: "us-east-1a", cpu_cores: 8, memory_bytes: 32 * 1024 * 1024 * 1024, last_seen_at: new Date().toISOString() },
      { id: "node-2", name: "node-data-01", status: "healthy", role: "worker", ip: "10.42.1.18", region: "us-east-1a", cpu_cores: 8, memory_bytes: 32 * 1024 * 1024 * 1024, last_seen_at: new Date().toISOString() },
      { id: "node-3", name: "node-edge-01", status: "healthy", role: "worker", ip: "10.42.1.22", region: "us-east-1b", cpu_cores: 4, memory_bytes: 16 * 1024 * 1024 * 1024, last_seen_at: new Date().toISOString() },
    ];

    const mockServices = [
      { id: "srv-1", name: "api-gateway-core", type: "application", status: "active", repo_url: "https://github.com/org/api-gateway", branch: "main", created_at: new Date().toISOString() },
      { id: "srv-2", name: "redis-cache-cluster", type: "application", status: "active", repo_url: "https://github.com/org/redis-cache", branch: "main", created_at: new Date().toISOString() },
      { id: "srv-3", name: "frontend-web-app", type: "application", status: "active", repo_url: "https://github.com/org/frontend-web", branch: "main", created_at: new Date().toISOString() },
    ];

    const mockBindings = [
      { id: "bind-1", service_id: "srv-1", repository_id: 101, service_key: "api-gateway-core", config_path: "opsi.yaml", selected_ref: "main", status: "active", build_strategy: "dockerfile", dockerfile_path: "Dockerfile", application_root: "/", build_context: "." },
      { id: "bind-2", service_id: "srv-2", repository_id: 102, service_key: "redis-cache-cluster", config_path: "opsi.yaml", selected_ref: "main", status: "active", build_strategy: "dockerfile", dockerfile_path: "Dockerfile", application_root: "/", build_context: "." },
      { id: "bind-3", service_id: "srv-3", repository_id: 103, service_key: "frontend-web-app", config_path: "opsi.yaml", selected_ref: "main", status: "active", build_strategy: "buildpack", application_root: "/", build_context: "." },
    ];

    const mockRepositories = [
      { repository_id: 101, full_name: "opsi-org/api-gateway-core", default_branch: "main", private: true },
      { repository_id: 102, full_name: "opsi-org/redis-cache-cluster", default_branch: "main", private: true },
      { repository_id: 103, full_name: "opsi-org/frontend-web-app", default_branch: "main", private: true },
    ];

    const mockTopology = {
      revision: 1,
      assignments: [
        { service_key: "api-gateway-core", environment_id: "env-prod", runtime_id: "rt-1", replicas: 2 },
        { service_key: "redis-cache-cluster", environment_id: "env-prod", runtime_id: "rt-1", replicas: 1 },
      ],
      nodes: mockNodes,
    };

    const mockPlacement = {
      project_id: "core-infra",
      environments: [
        { id: "env-prod", name: "Production", status: "active" },
        { id: "env-staging", name: "Staging", status: "active" },
      ],
      runtimes: [
        { id: "rt-1", name: "k3s-cluster-01", type: "kubernetes", environment_id: "env-prod", status: "healthy" }
      ],
      nodes: mockNodes,
      agents: [
        { node_id: "node-1", status: "connected", version: "2.4.1" },
        { node_id: "node-2", status: "connected", version: "2.4.1" },
        { node_id: "node-3", status: "connected", version: "2.4.1" },
      ],
      unplaced_services: ["frontend-web-app"]
    };

    const mockBuildRecords = [
      { id: "br-1", service_id: "srv-1", service_key: "api-gateway-core", workload: { sha: "a8f9c2184d0b11e9", image_digest: "sha256:8f4c2b9a7d3e1f0c" }, build: { build_job_id: "bj-1", status: "succeeded", sha: "a8f9c2184d0b11e9", image_digest: "sha256:8f4c2b9a7d3e1f0c" }, created_at: new Date().toISOString() },
      { id: "br-2", service_id: "srv-2", service_key: "redis-cache-cluster", workload: { sha: "c9d8e7f6a5b4c3d2", image_digest: "sha256:1a2b3c4d5e6f7a8b" }, build: { build_job_id: "bj-2", status: "succeeded", sha: "c9d8e7f6a5b4c3d2", image_digest: "sha256:1a2b3c4d5e6f7a8b" }, created_at: new Date().toISOString() },
      { id: "br-3", service_id: "srv-3", service_key: "frontend-web-app", workload: { sha: "b2x7k9e4d5f6a1c8", image_digest: "sha256:9f8e7d6c5b4a3a2b" }, build: { build_job_id: "bj-3", status: "succeeded", sha: "b2x7k9e4d5f6a1c8", image_digest: "sha256:9f8e7d6c5b4a3a2b" }, created_at: new Date().toISOString() },
    ];

    const mockBuildJobs = [
      { id: "bj-1", application_id: "srv-1", status: "succeeded", source: { resolved_commit_sha: "a8f9c2184d0b11e9" }, build_record_id: "br-1", created_at: new Date().toISOString() },
    ];

    const mockDeployments = [
      { id: "dep-1", service_id: "srv-1", status: "succeeded", rollout_state: "healthy", current_digest: "sha256:8f4c2b9a7d3e1f0c", runtime_id: "rt-1", updated_at: new Date().toISOString() },
      { id: "dep-2", service_id: "srv-2", status: "failed", rollout_state: "degraded", failure_message_redacted: "CrashLoopBackOff: back-off 5m0s restarting failed container", current_digest: "sha256:1a2b3c4d5e6f7a8b", runtime_id: "rt-1", updated_at: new Date().toISOString() },
      { id: "dep-3", service_id: "srv-3", status: "applying", rollout_state: "in_progress", current_digest: "sha256:9f8e7d6c5b4a3a2b", runtime_id: "rt-1", updated_at: new Date().toISOString() },
    ];

    await page.route('**/api/local/**', async route => {
      const url = route.request().url();
      const pathname = new URL(url).pathname;

      if (pathname === '/api/local/session') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({
            authenticated: true,
            cloud_connected: "ok",
            agent_connected: "ok",
            org_id: "opsi-org",
            project_id: "core-infra",
            role: "admin"
          })
        });
      } else if (pathname === '/api/local/projects') {
        await route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ projects: [mockProject] })
        });
      } else if (pathname === '/api/local/settings') {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ version: "2.4.1", cloud_configured: true, agent_configured: true }) });
      } else if (pathname.includes('/readiness')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ status: "ready" }) });
      } else if (pathname.includes('/nodes')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ nodes: mockNodes }) });
      } else if (pathname.includes('/services')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ services: mockServices }) });
      } else if (pathname.includes('/events')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [] }) });
      } else if (pathname.includes('/deployments')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deployments: mockDeployments }) });
      } else if (pathname.includes('/bootstrap-sessions')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sessions: [] }) });
      } else if (pathname.includes('/audit')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ events: [] }) });
      } else if (pathname.includes('/support')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ cluster_id: "cls-01" }) });
      } else if (pathname.includes('/topology/facts')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockPlacement) });
      } else if (pathname.includes('/topology')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(mockTopology) });
      } else if (pathname.includes('/build-records')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ records: mockBuildRecords }) });
      } else if (pathname.includes('/build-jobs')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ build_jobs: mockBuildJobs }) });
      } else if (pathname.includes('/github/bindings')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ bindings: mockBindings }) });
      } else if (pathname.includes('/github/repositories')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ repositories: mockRepositories }) });
      } else if (pathname.includes('/github/installations')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ installations: [{ installation_id: 1, account_login: "opsi-org" }] }) });
      } else if (pathname.includes('/exposures')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ exposures: [] }) });
      } else if (pathname.includes('/incidents')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ incidents: [] }) });
      } else if (pathname.includes('/telemetry')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ services: [] }) });
      } else if (pathname.includes('/resources')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ resources: [] }) });
      } else if (pathname.includes('/deployment-policies')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ policies: [] }) });
      } else {
        console.log("Unhandled API route:", pathname);
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) });
      }
    });

    // 2a. Capture Services View
    console.log("Navigating to Services View...");
    await page.goto('http://localhost:3001/?project=core-infra&view=services&environment=env-prod');
    await page.waitForTimeout(2000);
    await page.screenshot({ path: path.join(outDir, 'services.png') });
    console.log("Captured services.png");

    // 2b. Capture Infrastructure View
    console.log("Navigating to Infrastructure View...");
    await page.goto('http://localhost:3001/?project=core-infra&view=infrastructure&environment=env-prod');
    await page.waitForTimeout(2000);
    await page.screenshot({ path: path.join(outDir, 'infrastructure.png') });
    console.log("Captured infrastructure.png");

    // 2c. Capture Delivery View
    console.log("Navigating to Delivery View...");
    await page.goto('http://localhost:3001/?project=core-infra&view=delivery&tab=deployments&environment=env-prod');
    await page.waitForTimeout(2000);
    await page.screenshot({ path: path.join(outDir, 'delivery.png') });
    console.log("Captured delivery.png");

    // 2d. Capture Observability View
    console.log("Navigating to Observability View...");
    await page.goto('http://localhost:3001/?project=core-infra&view=observability&tab=overview&environment=env-prod');
    await page.waitForTimeout(2000);
    await page.screenshot({ path: path.join(outDir, 'observability.png') });
    console.log("Captured observability.png");

    // 2e. Capture Security View
    console.log("Navigating to Security View...");
    await page.goto('http://localhost:3001/?project=core-infra&view=security&tab=overview&environment=env-prod');
    await page.waitForTimeout(2000);
    await page.screenshot({ path: path.join(outDir, 'security.png') });
    console.log("Captured security.png");

    await context.close();
  }

  await browser.close();
  console.log("All screenshots captured successfully!");
})();
