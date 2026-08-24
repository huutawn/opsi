import { expect, test, type Page, type Route } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

const hash = (value: string) => value.repeat(64).slice(0, 64);

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("Deploy replaces legacy action URLs and approves the exact reviewed plan", async ({ page }) => {
	let run = deploymentRun("awaiting_approval");
	let approvalBody: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, (body) => {
		approvalBody = body;
		run = deploymentRun("provisioning");
		return run;
	});

	await page.goto("/?project=proj-1&view=delivery&tab=pipeline");
	await expect(page).toHaveURL(/view=deploy/);
	await expect(page.getByRole("heading", { name: "Deploy", exact: true })).toBeVisible();
	await expect(page.getByRole("heading", { name: "Review plan" })).toBeVisible();
	await expect(page.getByText("identity-api", { exact: true }).first()).toBeVisible();
	await expect(page.getByText("identity-web", { exact: true }).first()).toBeVisible();
	await expect(page.getByText("Generated and securely stored", { exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toBeEnabled();

	const approve = page.getByRole("button", { name: "Approve & Deploy" });
	await approve.focus();
	await expect(approve).toBeFocused();
	await page.keyboard.press("Enter");
	await expect.poll(() => approvalBody).toEqual({ plan_hash: hash("a") });
	await expect(page.getByText("provisioning", { exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Cancel run" })).toBeVisible();

	await page.reload();
	await expect(page.getByText("Opsi is provisioning. This run continues after refresh or restart.")).toBeVisible();
	for (const width of [320, 768, 1024, 1440]) {
		await page.setViewportSize({ width, height: 844 });
		expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
	}
});

test("viewer can inspect the reviewed plan but sees no mutation control", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run, "viewer");

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Review plan" })).toBeVisible();
	await expect(page.getByText("Your role has read-only access to this run.")).toBeVisible();
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toHaveCount(0);
	await expect(page.getByLabel("Canonical key").first()).toBeDisabled();
});

test("review plan has no WCAG 2.1 A or AA axe violations", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run);

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Review plan" })).toBeVisible();
	const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
	expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
});

test("warning acknowledgement submits the exact preflight hash", async ({ page }) => {
	let run = deploymentRun("awaiting_warning_ack");
	let acknowledgement: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, (body) => {
		acknowledgement = body;
		run = deploymentRun("deploying");
		return run;
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByRole("button", { name: "Acknowledge & Continue" }).click();
	await expect.poll(() => acknowledgement).toEqual({ preflight_hash: hash("p") });
	await expect(page.getByRole("button", { name: "Cancel run" })).toBeVisible();
});

test("external secret input is submitted once and removed from rendered state", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	run.plan.secrets = [{ name: "oauth-client", application_key: "identity-api", environment_name: "OAuth__ClientSecret", generated: false, secret_ref: "", revision: 0, display: "External", confidence: "high", reason: "Configuration", evidence: [] }];
	let secretPayload: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, () => run, "owner", (body) => {
		secretPayload = body;
		return { id: "secret-1", reference: "workload-secret://secret-1", project_id: "proj-1", service_id: "planned:identity-api", logical_name: "oauth-client", revision: 1, status: "ready", updated_at: "2026-08-24T00:00:00Z" };
	});

	await page.goto("/?project=proj-1&view=deploy");
	const input = page.getByLabel("Value for oauth-client");
	await input.fill("one-time-browser-secret");
	await page.getByRole("button", { name: "Store securely" }).click();
	await expect.poll(() => secretPayload).toEqual({ logical_name: "oauth-client", value: "one-time-browser-secret" });
	await expect(input).toHaveValue("");
	await expect(page.getByText("Reference revision 1")).toBeVisible();
	expect(await page.locator("body").innerText()).not.toContain("one-time-browser-secret");
});

test("stale review, safe retry, and cancellation each expose one factual action", async ({ page }) => {
	let run = deploymentRun("stale");
	const actions: string[] = [];
	await mockDeployAPI(page, () => run, (_body, name) => {
		actions.push(name || "");
		if (name === "analyze") run = deploymentRun("awaiting_approval");
		if (name === "retry") run = deploymentRun("building");
		if (name === "cancel") run = deploymentRun("cancelled");
		return run;
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByRole("button", { name: "Analyze again" }).click();
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toBeVisible();
	run = deploymentRun("failed");
	await page.reload();
	await page.getByRole("button", { name: "Retry failed step" }).click();
	await expect(page.getByRole("button", { name: "Cancel run" })).toBeVisible();
	await page.getByRole("button", { name: "Cancel run" }).click();
	await expect(page.getByText("cancelled", { exact: true })).toBeVisible();
	expect(actions).toEqual(["analyze", "retry", "cancel"]);
});

async function mockDeployAPI(page: Page, current: () => ReturnType<typeof deploymentRun>, action: (body: Record<string, unknown>, name?: string) => ReturnType<typeof deploymentRun>, role = "owner", secretAction?: (body: Record<string, unknown>) => Record<string, unknown>) {
	await page.route("**/api/local/**", async (route) => {
		const request = route.request();
		const path = new URL(request.url()).pathname;
		let body: unknown = {};
		if (/\/workload-secrets$/.test(path) && request.method() === "PUT" && secretAction) body = { workload_secret: secretAction(request.postDataJSON()), reused: false };
		else if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", org_id: "org-1", project_id: "proj-1", role, capabilities: [] };
		else if (path === "/api/local/session/project") body = { status: "selected", project_id: "proj-1" };
		else if (path === "/api/local/projects") body = { projects: [{ id: "proj-1", org_id: "org-1", name: "Identity", slug: "identity", status: "ready" }] };
		else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
		else if (path.endsWith("/github/repositories")) body = { repositories: [{ repository_id: 7, installation_id: 9, full_name: "acme/identity-service", default_branch: "main", status: "active", claim_status: "active", archived: false, disabled: false }] };
		else if (path.endsWith("/deployment-runs")) body = { deployment_runs: [current()] };
		else if (/\/deployment-runs\/run-1\/events$/.test(path)) body = { events: [{ id: "event-1", project_id: "proj-1", run_id: "run-1", state: current().state, level: "info", message: current().state === "provisioning" ? "Plan approved; provisioning started." : "Repository analysis is ready for review.", created_at: "2026-08-24T00:00:00Z" }] };
		else if (/\/deployment-runs\/run-1\/result$/.test(path)) body = { run_id: "run-1", state: current().state, source_sha: hash("d").slice(0, 40), applications: [], verifications: [], capacity: [] };
		else if (/\/deployment-runs\/run-1\/(approve|acknowledge|analyze|retry|cancel)$/.test(path)) body = action(request.postDataJSON(), path.split("/").at(-1));
		else if (/\/deployment-runs\/run-1$/.test(path)) body = current();
		else if (path.endsWith("/topology/facts")) body = placementFacts();
		else if (path.endsWith("/topology")) body = { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 1, state_hash: hash("b"), plan_hash: hash("c"), assignments: [] };
		else if (path.endsWith("/nodes")) body = { nodes: [] };
		else if (path.endsWith("/services")) body = { services: [] };
		else if (path.endsWith("/deployments")) body = { deployments: [] };
		else if (path.endsWith("/bootstrap-sessions")) body = { sessions: [] };
		else if (path.endsWith("/build-records")) body = { records: [] };
		else if (path.endsWith("/audit")) body = { events: [] };
		else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: [] };
		else if (path.endsWith("/support")) body = { generated_at: "2026-08-24T00:00:00Z", counts: {}, signals: [] };
		await fulfill(route, body);
	});
}

async function fulfill(route: Route, body: unknown) {
	await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 });
}

function placementFacts() {
	return { project_id: "proj-1", environments: [{ id: "env-1", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-1", project_id: "proj-1", environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }], nodes: [], agents: [], services: [] };
}

function deploymentRun(state: "awaiting_approval" | "awaiting_warning_ack" | "provisioning" | "building" | "deploying" | "failed" | "stale" | "cancelled") {
	return {
		schema_version: "opsi.deployment_run/v2", id: "run-1", project_id: "proj-1", created_by: "user-1", state,
		plan: {
			schema_version: "opsi.deployment_plan/v2", hash: hash("a"),
			source: { repository_id: 7, installation_id: 9, repository: "acme/identity-service", selected_ref: "main", commit_sha: hash("d").slice(0, 40) },
			applications: [
				{ source_key: "api", key: "identity-api", name: "identity-api", root: "be", port: 8080, build: { context: "be", dockerfile_path: "be/Dockerfile", strategy: "dockerfile", platform: "linux/amd64" }, confidence: "high", reason: "Dockerfile", evidence: [] },
				{ source_key: "web", key: "identity-web", name: "identity-web", root: "tcip-fake", port: 3000, build: { context: "tcip-fake", dockerfile_path: "tcip-fake/Dockerfile", strategy: "dockerfile", platform: "linux/amd64" }, confidence: "high", reason: "Dockerfile", evidence: [] },
			],
			resources: [{ logical_name: "postgres", type: "postgres", managed: true, required: true, recommendation: "Managed PostgreSQL", confidence: "high", evidence: [] }, { logical_name: "valkey", type: "redis", managed: true, required: true, recommendation: "Managed Valkey", confidence: "high", evidence: [] }],
			dependencies: [{ from: "identity-api", to: "postgres", protocol: "postgres", required: true, confidence: "high", reason: "Compose", evidence: [], injections: [{ environment_name: "ConnectionStrings__Database", symbolic_source: "resource.postgres.connection_string" }] }, { from: "identity-web", to: "identity-api", protocol: "http", strategy: "same_origin", path: "/api", required: true, verification: { type: "consumer_http", path: "/health", expected_status: 200 }, confidence: "high", reason: "Route", evidence: [] }],
			bindings: [{ from: "identity-web", to: "identity-api", kind: "browser_http", path: "/api", confidence: "high", reason: "Route", evidence: [] }],
			secrets: [{ name: "jwt-signing-key", application_key: "identity-api", environment_name: "Jwt__SigningKey", generated: true, secret_ref: "generated://jwt-signing-key", revision: 0, display: "Generated and securely stored", confidence: "high", reason: "Configuration", evidence: [] }],
			issues: [], target: { environment_id: "env-1", runtime_id: "runtime-1", hostname: "identity.apps.example.test", exposure: "public", cpu_milli: 250, memory_bytes: 268435456 }, authority_revisions: { source_commit_sha: hash("d").slice(0, 40) }, failure_policy: { fail_fast: true, rollback_known_good: true, retain_persistent_data: true, max_attempts: 3 },
		},
		analysis: { authority: "compose", issues: [], files_inspected: 6, bytes_inspected: 2048, truncated: false }, authority_refs: { checkpoints: [] }, preflight_hash: state === "awaiting_warning_ack" ? hash("p") : undefined, failure: state === "failed" ? { step: "building", code: "BUILD_AUTHORITY_UNAVAILABLE", message: "Build authority unavailable.", next_action: "Retry the failed step.", retryable: true } : state === "stale" ? { step: "preflighting", code: "DEPLOYMENT_PLAN_STALE", message: "An authority changed.", next_action: "Analyze and review again.", retryable: false } : undefined, attempt: state === "awaiting_approval" ? 0 : 1, revision: state === "awaiting_approval" ? 3 : 4, created_at: "2026-08-24T00:00:00Z", updated_at: "2026-08-24T00:00:00Z",
	};
}
