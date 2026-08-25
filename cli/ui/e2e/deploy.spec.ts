import { expect, test, type Page, type Route } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

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

test("missing target shows Connect server before any mutation is running", async ({ page }) => {
	const run = deploymentRun("awaiting_input");
	(run.plan.issues as Array<Record<string, unknown>>).push({ code: "TARGET_SERVER_REQUIRED", message: "No Ready project server is available.", resolution: "Connect a server.", blocking: true });
	await mockDeployAPI(page, () => run, () => run);

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("button", { name: "Connect server" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Working…" })).toHaveCount(0);
});

test("truncated analysis submits a canonical scope on the same run", async ({ page }) => {
	let run = deploymentRun("awaiting_input");
	(run.plan.issues as Array<Record<string, unknown>>).push({ code: "ANALYSIS_TRUNCATED", message: "Analysis reached the file limit.", resolution: "Refine analysis.", blocking: true });
	let submitted: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, (body, name) => {
		if (name === "analyze") submitted = body;
		run = deploymentRun("awaiting_approval");
		return run;
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByLabel("Application roots").fill("tcip-fake\nbe\nbe");
	await page.getByLabel("Exclude paths").fill("docs/archive");
	await page.getByRole("button", { name: "Analyze exact commit with scope" }).click();
	await expect.poll(() => submitted).toEqual({ scope: { application_roots: ["be", "tcip-fake"], exclude_paths: ["docs/archive"] } });
});

test("export is previewed before an operator creates a pull request", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	let exports = 0;
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		onExport: () => { exports += 1; },
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByRole("button", { name: "Export configuration" }).click();
	const dialog = page.getByRole("dialog", { name: "Review configuration export" });
	await expect(dialog).toBeVisible();
	await expect(dialog.getByText("will not be merged automatically")).toBeVisible();
	await dialog.getByRole("button", { name: "Create pull request" }).click();
	await expect.poll(() => exports).toBe(1);
	await expect(page.getByRole("link", { name: "Open pull request #9" })).toBeVisible();
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

test("available repository is claimed and analyzed without a page reload", async ({ page }) => {
	let run: ReturnType<typeof deploymentRun> | null = null;
	let claimed = false;
	let createCount = 0;
	await mockDeployAPI(page, () => run, () => run ?? deploymentRun("awaiting_approval"), "owner", undefined, {
		repository: () => sourceRepository(claimed ? "active" : "available"),
		onClaim: () => { claimed = true; },
		onCreate: () => { createCount += 1; run = deploymentRun("awaiting_approval"); return run; },
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByLabel("Repository", { exact: true }).selectOption("7");
	await page.getByLabel("Branch or ref").fill("developer");
	await page.getByLabel("Hostname (optional)").fill("identity.example.test");
	await expect(page).toHaveURL(/source_repository=7/);
	await page.reload();
	await expect(page.getByLabel("Repository", { exact: true })).toHaveValue("7");
	await expect(page.getByLabel("Branch or ref")).toHaveValue("developer");
	await expect(page.getByLabel("Hostname (optional)")).toHaveValue("identity.example.test");
	await page.getByRole("button", { name: "Claim & analyze repository" }).click();
	await expect(page.getByRole("heading", { name: "Review plan" })).toBeVisible();
	expect(claimed).toBe(true);
	expect(createCount).toBe(1);
	expect(await page.evaluate(() => window.sessionStorage.getItem("opsi:deploy-source:proj-1"))).toBeNull();
});

test("main ref with absent optional detections opens review without crashing", async ({ page }) => {
	let run: ReturnType<typeof deploymentRun> | null = null;
	await mockDeployAPI(page, () => run, () => run ?? deploymentRun("awaiting_approval"), "owner", undefined, {
		repository: () => sourceRepository("active"),
		onCreate: () => {
			run = deploymentRun("awaiting_approval");
			run.plan.source.selected_ref = "main";
			const nullablePlan = run.plan as unknown as { bindings: null; secrets: null };
			nullablePlan.bindings = null;
			nullablePlan.secrets = null;
			return run;
		},
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByLabel("Repository", { exact: true }).selectOption("7");
	await expect(page.getByLabel("Branch or ref")).toHaveValue("main");
	await page.getByRole("button", { name: "Analyze repository" }).click();
	await expect(page.getByRole("heading", { name: "Review plan" })).toBeVisible();
	await expect(page).toHaveURL("/?project=proj-1&view=deploy");
});

test("repository conflict cannot enter analysis", async ({ page }) => {
	await mockDeployAPI(page, () => null, () => deploymentRun("awaiting_approval"), "owner", undefined, {
		repository: () => sourceRepository("conflict"),
	});

	await page.goto("/?project=proj-1&view=deploy");
	const conflict = page.getByRole("option", { name: /claimed by another project/ });
	await expect(conflict).toBeDisabled();
	await expect(page.getByRole("button", { name: "Analyze repository" })).toBeDisabled();
});

test("claim auth failure keeps Source visible and offers sign-in", async ({ page }) => {
	let loginStarts = 0;
	await mockDeployAPI(page, () => null, () => deploymentRun("awaiting_approval"), "owner", undefined, {
		repository: () => sourceRepository("available"),
		claimFailure: true,
		onLoginStart: () => { loginStarts += 1; },
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByLabel("Repository", { exact: true }).selectOption("7");
	expectHTTPFailure(page, { method: "POST", path: "/api/local/projects/proj-1/github/repositories/7/claim", status: 401 });
	await page.getByRole("button", { name: "Claim & analyze repository" }).click();
	await expect(page.getByRole("heading", { name: "Choose a repository to deploy" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Sign in again" })).toBeVisible();
	await expect(page.getByText("Cloud rejected the saved credential", { exact: true })).toBeVisible();
	const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
	expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
	await page.getByRole("button", { name: "Sign in again" }).click();
	await expect.poll(() => loginStarts).toBe(1);
	await expect(page).toHaveURL(/project=proj-1.*view=deploy/);
	await expect(page.getByLabel("Repository", { exact: true })).toHaveValue("7");
});

test("project selection enters Deploy without reload or a stuck signing state", async ({ page }) => {
	const selection = { authenticated: false, selections: 0 };
	await mockDeployAPI(page, () => null, () => deploymentRun("awaiting_approval"), "owner", undefined, undefined, selection);

	await page.goto("/?auth=select_project&selection_id=selection-1");
	await expect(page.getByRole("heading", { name: "Choose a Project" })).toBeVisible();
	await page.getByRole("button", { name: "Continue with Selected Project" }).click();
	await expect(page.getByRole("heading", { name: "Deploy", exact: true })).toBeVisible();
	await expect(page).toHaveURL(/project=proj-1.*view=deploy/);
	await expect(page.getByText("Signing in…")).toHaveCount(0);
	expect(selection.selections).toBe(1);
});

type SourceBehavior = {
	repository: () => ReturnType<typeof sourceRepository>;
	onClaim?: () => void;
	onCreate?: () => ReturnType<typeof deploymentRun>;
	claimFailure?: boolean;
	onLoginStart?: () => void;
	onExport?: () => void;
};

type AuthSelectionBehavior = { authenticated: boolean; selections: number };

async function mockDeployAPI(page: Page, current: () => ReturnType<typeof deploymentRun> | null, action: (body: Record<string, unknown>, name?: string) => ReturnType<typeof deploymentRun>, role = "owner", secretAction?: (body: Record<string, unknown>) => Record<string, unknown>, source?: SourceBehavior, authSelection?: AuthSelectionBehavior) {
	await page.route("**/api/local/**", async (route) => {
		const request = route.request();
		const path = new URL(request.url()).pathname;
		let body: unknown = {};
		if (/\/workload-secrets$/.test(path) && request.method() === "PUT" && secretAction) body = { workload_secret: secretAction(request.postDataJSON()), reused: false };
		else if (path === "/api/local/session/login/start" && request.method() === "POST" && source) { source.onLoginStart?.(); const login = request.postDataJSON(); const query = new URLSearchParams(String(login.return_query || "")); query.set("project", "proj-1"); query.set("view", "deploy"); query.set("auth", "ok"); body = { auth_url: "/?" + query.toString(), status: "pending" }; }
		else if (path === "/api/local/session/selection" && authSelection) body = { selection_id: "selection-1", projects: [{ id: "proj-1", name: "dada", role }] };
		else if (path === "/api/local/session/select-project" && request.method() === "POST" && authSelection) { authSelection.authenticated = true; authSelection.selections += 1; body = { authenticated: true, session: { user_id: "user-1", org_id: "org-1", project_id: "proj-1", role } }; }
		else if (/\/github\/repositories\/7\/claim$/.test(path) && request.method() === "POST" && source?.claimFailure) return fulfill(route, { error: { code: "CLOUD_AUTH_REQUIRED", message: "Cloud rejected the saved credential", next_action: "Sign in again." } }, 401);
		else if (/\/github\/repositories\/7\/claim$/.test(path) && request.method() === "POST" && source) { source.onClaim?.(); body = { repository_id: 7, project_id: "proj-1", status: "active" }; }
		else if (path.endsWith("/repository-export/preview") && request.method() === "POST") body = { run_id: "run-1", run_revision: 3, plan_hash: hash("a"), source_sha: hash("d").slice(0, 40), repository_id: 7, target_branch: "main", path: ".opsi/opsi-cd.yaml", yaml: "version: 2\n", diff: "+version: 2\n", preview_hash: hash("e"), export_enabled: true };
		else if (path.endsWith("/repository-export") && request.method() === "POST") { source?.onExport?.(); body = { repository_export: { branch: "opsi/export-run", commit_sha: hash("f").slice(0, 40), pull_request_number: 9, pull_request_url: "https://github.test/pr/9", reused: false } }; }
		else if (path.endsWith("/deployment-runs") && request.method() === "POST" && source?.onCreate) body = { deployment_run: source.onCreate(), reused: false };
		else if (path === "/api/local/session") body = authSelection && !authSelection.authenticated ? { authenticated: false, cloud_connected: "ok", agent_connected: "ok", token_status: "missing", local_session: "local-session" } : { authenticated: true, cloud_connected: "ok", agent_connected: "ok", token_status: "valid", local_session: "local-session", user_id: "user-1", org_id: "org-1", project_id: "proj-1", role, capabilities: [] };
		else if (path === "/api/local/session/project") body = { status: "selected", project_id: "proj-1" };
		else if (path === "/api/local/projects") body = { projects: [{ id: "proj-1", org_id: "org-1", name: "Identity", slug: "identity", status: "ready" }] };
		else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
		else if (path.endsWith("/github/installations/discover")) body = { installations: [] };
		else if (path.endsWith("/github/installations")) body = { installations: [{ installation_id: 9, account_login: "acme", account_type: "Organization", status: "active" }] };
		else if (path.endsWith("/github/repositories")) body = { repositories: [source?.repository() ?? sourceRepository("active")] };
		else if (path.endsWith("/deployment-runs")) body = { deployment_runs: current() ? [current()] : [] };
		else if (/\/deployment-runs\/run-1\/events$/.test(path)) { const run = current(); body = { events: run ? [{ id: "event-1", project_id: "proj-1", run_id: "run-1", state: run.state, level: "info", message: run.state === "provisioning" ? "Plan approved; provisioning started." : "Repository analysis is ready for review.", created_at: "2026-08-24T00:00:00Z" }] : [] }; }
		else if (/\/deployment-runs\/run-1\/result$/.test(path)) { const run = current(); body = { run_id: "run-1", state: run?.state ?? "awaiting_input", source_sha: hash("d").slice(0, 40), applications: [], verifications: [], capacity: [] }; }
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

async function fulfill(route: Route, body: unknown, status = 200) {
	await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status });
}

function sourceRepository(claimStatus: "active" | "available" | "conflict") {
	return { repository_id: 7, installation_id: 9, full_name: "acme/identity-service", default_branch: "main", status: "active", claim_status: claimStatus, archived: false, disabled: false };
}

function placementFacts() {
	return { project_id: "proj-1", environments: [{ id: "env-1", project_id: "proj-1", name: "Production", type: "prod", status: "active" }], runtimes: [{ id: "runtime-1", project_id: "proj-1", environment_id: "env-1", name: "Primary", type: "k3s", status: "ready" }], nodes: [], agents: [], services: [] };
}

function deploymentRun(state: "awaiting_input" | "awaiting_approval" | "awaiting_warning_ack" | "provisioning" | "building" | "deploying" | "failed" | "stale" | "cancelled") {
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
			issues: [], analysis_scope: { application_roots: [], exclude_paths: [] }, analysis_scope_hash: hash("s"), evidence_coverage: { candidates_found: 6, candidates_selected: 6, files_inspected: 6, bytes_inspected: 2048 }, target: { environment_id: "env-1", runtime_id: "runtime-1", hostname: "identity.apps.example.test", exposure: "public", cpu_milli: 250, memory_bytes: 268435456 }, authority_revisions: { source_commit_sha: hash("d").slice(0, 40) }, failure_policy: { fail_fast: true, rollback_known_good: true, retain_persistent_data: true, max_attempts: 3 },
		},
		analysis: { authority: "compose", issues: [], files_inspected: 6, bytes_inspected: 2048, truncated: false }, authority_refs: { checkpoints: [] }, preflight_hash: state === "awaiting_warning_ack" ? hash("p") : undefined, failure: state === "failed" ? { step: "building", code: "BUILD_AUTHORITY_UNAVAILABLE", message: "Build authority unavailable.", next_action: "Retry the failed step.", retryable: true } : state === "stale" ? { step: "preflighting", code: "DEPLOYMENT_PLAN_STALE", message: "An authority changed.", next_action: "Analyze and review again.", retryable: false } : undefined, attempt: state === "awaiting_approval" ? 0 : 1, revision: state === "awaiting_approval" ? 3 : 4, created_at: "2026-08-24T00:00:00Z", updated_at: "2026-08-24T00:00:00Z",
	};
}
