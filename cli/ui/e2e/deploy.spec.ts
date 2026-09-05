import { expect, test, type Page, type Route } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { expectHTTPFailure, expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";
import type { DeploymentRun } from "@/lib/contracts/registry";

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
	await expect(page.getByRole("heading", { name: "Ready to deploy" })).toBeVisible();
	await expect(page.getByText("identity-api:8080 · identity-web:3000", { exact: true })).toBeVisible();
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

test("dirty resource edits must be saved before approval can deploy", async ({ page }) => {
	let run = deploymentRun("awaiting_approval");
	let approvals = 0;
	let savedPlan: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, (_body, name) => {
		if (name === "approve") approvals += 1;
		run = deploymentRun("provisioning");
		return run;
	}, "owner", undefined, {
		repository: () => sourceRepository("active"),
		onPlanUpdate: (body) => {
			savedPlan = body.plan as Record<string, unknown>;
			run = { ...run, plan: body.plan as typeof run.plan, revision: run.revision + 1 };
			return run;
		},
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();
	const cpuLimits = page.getByLabel("CPU limit (m)");
	await cpuLimits.nth(0).fill("500");
	await cpuLimits.nth(1).fill("300");
	await expect(page.getByRole("button", { name: "Save changes before approval" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toHaveCount(0);

	await page.getByRole("button", { name: "Save changes before approval" }).click();
	await expect.poll(() => savedPlan).toBeDefined();
	const applications = savedPlan?.applications as Array<{ capacity?: { cpu_limit_milli?: number } }>;
	expect(applications[0].capacity?.cpu_limit_milli).toBe(500);
	expect(applications[1].capacity?.cpu_limit_milli).toBe(300);
	expect(approvals).toBe(0);

	await page.getByRole("button", { name: "Approve & Deploy" }).click();
	expect(approvals).toBe(1);
});

test("viewer can inspect the reviewed plan but sees no mutation control", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run, "viewer");

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Ready to deploy" })).toBeVisible();
	await expect(page.getByText("Your role has read-only access to this run.")).toBeVisible();
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toHaveCount(0);
	await page.getByText("View or edit full detected configuration").click();
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

test("SSH bootstrap reports live progress and resumes analysis when the runtime is ready", async ({ page }) => {
	let run = deploymentRun("awaiting_input");
	(run.plan.issues as Array<Record<string, unknown>>).push({ code: "TARGET_SERVER_REQUIRED", message: "No Ready project server is available.", resolution: "Connect a server.", blocking: true });
	let bootstrapStatus = "installing_agent";
	let runtimeStatus = "provisioning";
	await mockDeployAPI(page, () => run, (_body, name) => {
		if (name === "analyze") run = deploymentRun("awaiting_approval");
		return run;
	}, "owner", undefined, {
		repository: () => sourceRepository("active"),
		bootstrapSession: () => ({ id: "boot-1", status: bootstrapStatus, public_host: "103.252.137.163", role: "first_server", auth_method: "private_key", attempt_count: 1, max_attempts: 3, created_at: "2026-08-25T00:00:00Z" }),
		bootstrapEvents: () => [{ id: `event-${bootstrapStatus}`, step: bootstrapStatus, message_redacted: bootstrapStatus === "completed" ? "bootstrap completed after verified Agent heartbeat" : "staging verified Opsi Agent release", progress_percent: bootstrapStatus === "completed" ? 100 : 65, created_at: "2026-08-25T00:00:01Z" }],
		placement: () => ({ ...placementFacts(), runtimes: [{ ...placementFacts().runtimes[0], status: runtimeStatus }] }),
	});

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Connecting 103.252.137.163" })).toBeVisible();
	await expect(page.getByRole("progressbar", { name: "Server bootstrap progress" })).toHaveAttribute("value", "65");
	await expect(page.getByRole("button", { name: "Connecting server…" })).toBeDisabled();

	bootstrapStatus = "completed";
	runtimeStatus = "ready";
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toBeVisible({ timeout: 8_000 });
});
test("SSH host-key probe displays TOFU indicator and pins identity on first connection", async ({ page }) => {
	const run = deploymentRun("awaiting_input");
	(run.plan.issues as Array<Record<string, unknown>>).push({ code: "TARGET_SERVER_REQUIRED", message: "No Ready project server is available.", resolution: "Connect a server.", blocking: true });
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		placement: () => ({ ...placementFacts(), runtimes: [{ ...placementFacts().runtimes[0], status: "provisioning" }] }),
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByRole("button", { name: "Connect server" }).click();
	await expect(page.getByRole("heading", { name: "Connect Server" })).toBeVisible();

	// Open Advanced details and select SSH Password method
	await page.getByText("Advanced: Bootstrap over SSH").click();
	await page.getByRole("radio", { name: "SSH Password" }).click();
	await page.getByRole("textbox", { name: "Server IP or hostname" }).fill("203.0.113.10");
	await page.getByRole("textbox", { name: "Server IP or hostname" }).blur();
	// Wait for TOFU indicator
	await expect(page.getByText("First Connection (TOFU)")).toBeVisible();
	await expect(page.getByText("SHA256:4t7EExampleMockFingerprintAAAA")).toBeVisible();
	await expect(page.getByRole("button", { name: "Connect server over SSH" })).toBeEnabled();
});

test("waiting_host_key_confirmation displays alert and resumes via rotation dialog", async ({ page }) => {
	const run = deploymentRun("awaiting_input");
	run.plan.issues.push({ code: "TARGET_SERVER_REQUIRED", message: "No Ready project server is available.", resolution: "Connect a server.", blocking: true });
	let bootstrapStatus = "waiting_host_key_confirmation";
	let resumed = false;

	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		bootstrapSession: () => ({ id: "boot-1", status: bootstrapStatus, public_host: "103.252.137.163", role: "first_server", auth_method: "password", attempt_count: 1, max_attempts: 3, created_at: "2026-08-25T00:00:00Z" }),
		bootstrapEvents: () => [{ id: "evt-mismatch", step: "SSH_HOST_KEY_MISMATCH", message_redacted: "SSH host-key mismatch detected", progress_percent: 25, created_at: "2026-08-25T00:00:01Z" }],
		placement: () => ({ ...placementFacts(), runtimes: [{ ...placementFacts().runtimes[0], status: "provisioning" }] }),
		onResume: () => {
			resumed = true;
			bootstrapStatus = "pending";
		},
	});

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("button", { name: "Review & Resume" })).toBeVisible();

	// Click Review & Resume
	await page.getByRole("button", { name: "Review & Resume" }).click();
	const rotationDialog = page.getByRole("dialog");
	await expect(rotationDialog.getByRole("heading", { name: "Confirm Host Key & Resume" })).toBeVisible();

	// Check verification consent inside dialog
	await rotationDialog.getByRole("checkbox").check();

	// Fill new password
	await rotationDialog.getByPlaceholder("Enter password").fill("new-secret-password");

	// Click Confirm & Resume Session
	await rotationDialog.getByRole("button", { name: "Confirm & Resume Session" }).click();

	await expect(rotationDialog).not.toBeVisible();
	expect(resumed).toBe(true);
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
	await page.getByText("View or edit full detected configuration").click();
	await page.getByRole("button", { name: "Export configuration" }).click();
	const dialog = page.getByRole("dialog", { name: "Review configuration export" });
	await expect(dialog).toBeVisible();
	await expect(dialog.getByText("will not be merged automatically")).toBeVisible();
	await dialog.getByRole("button", { name: "Create pull request" }).click();
	await expect.poll(() => exports).toBe(1);
	await expect(page.getByRole("link", { name: "Open pull request #9" })).toBeVisible();
});
test("Resource allocation proposal popup appears, applies draft plan with burst limits, or closes", async ({ page }) => {
	let run = deploymentRun("awaiting_approval");
	let updatedPlanPayload: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		onPlanUpdate: (body) => {
			updatedPlanPayload = body;
			run = { ...run, plan: (body.plan as typeof run.plan), revision: run.revision + 1 };
			return run;
		},
	});

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Ready to deploy" })).toBeVisible();

	// 1. Open proposal dialog via "Resource proposal" action in review
	await page.getByText("View or edit full detected configuration").click();
	const proposalButton = page.getByRole("button", { name: "Resource proposal" });
	await expect(proposalButton).toBeVisible();
	await proposalButton.click();

	const dialog = page.getByRole("dialog", { name: "Resource allocation proposal" });
	await expect(dialog).toBeVisible();
	await expect(dialog.getByText("Real capacity")).toBeVisible();
	await expect(dialog.getByText("Available for apps")).toBeVisible();
	await expect(dialog.getByText("Burst enabled").first()).toBeVisible();

	// 2. Click Apply to draft
	await dialog.getByRole("button", { name: "Apply to draft" }).click();
	await expect(dialog).toBeHidden();
	await expect.poll(() => updatedPlanPayload).toBeDefined();

	// 3. Re-open via "Resource proposal" button in PlanReview
	await proposalButton.click();
	await expect(dialog).toBeVisible();

	// 4. Close dialog without changes
	await dialog.getByRole("button", { name: "Close", exact: true }).click();
	await expect(dialog).toBeHidden();

	// 5. Approve & Deploy is available
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toBeEnabled();
});
test("low capacity or ineligible recommendation disables Apply button and displays warning", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		recommendation: () => ({
			eligible: false,
			reason: "Available capacity cannot satisfy the minimum required 100m CPU and 128 MiB RAM per replica.",
			warnings: ["Insufficient headroom for safe baseline allocation."],
			target_capacity: { cpu_millicores: 1000, memory_bytes: 1024 * 1024 * 1024, source: "agent_observed", heartbeat_age_seconds: 5, heartbeat_fresh: true },
			reserved_budget: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
			managed_budget: { cpu_millicores: 500, memory_bytes: 512 * 1024 * 1024 },
			available_budget: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
			remaining_budget: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
			budget_projection: {
				real_capacity: { cpu_millicores: 1000, memory_bytes: 1024 * 1024 * 1024 },
				system_reserve: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
				existing_workloads: { cpu_millicores: 0, memory_bytes: 0 },
				planned_managed: { cpu_millicores: 500, memory_bytes: 512 * 1024 * 1024 },
				available_for_run: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
				remaining_after_proposal: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
			},
			applications: [],
		}),
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();
	await page.getByRole("button", { name: "Resource proposal" }).click();

	const dialog = page.getByRole("dialog", { name: "Resource allocation proposal" });
	await expect(dialog).toBeVisible();
	await expect(dialog.getByText("Recommendation unavailable")).toBeVisible();
	await expect(dialog.getByText("Available capacity cannot satisfy the minimum required")).toBeVisible();
	await expect(dialog.getByRole("button", { name: "Apply to draft" })).toBeDisabled();
	await dialog.getByRole("button", { name: "Close", exact: true }).click();
	await expect(dialog).toBeHidden();
});

test("stale resource recommendation refreshes and requires confirmation before applying", async ({ page }) => {
	let run = deploymentRun("awaiting_approval");
	let stale = true;
	let recommendationRequests = 0;
	let planUpdates = 0;
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		recommendation: () => {
			recommendationRequests += 1;
			return resourceRecommendation(hash(recommendationRequests === 1 ? "d" : "e"));
		},
		planUpdateFailure: () => {
			if (!stale) return undefined;
			stale = false;
			return {
				status: 409,
				body: { error: { code: "RESOURCE_RECOMMENDATION_STALE", message: "Resource recommendation basis changed." } },
			};
		},
		onPlanUpdate: (body) => {
			planUpdates += 1;
			run = { ...run, plan: body.plan as typeof run.plan, revision: run.revision + 1 };
			return run;
		},
	});

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();
	await page.getByRole("button", { name: "Resource proposal" }).click();

	const dialog = page.getByRole("dialog", { name: "Resource allocation proposal" });
	expectHTTPFailure(page, { path: "/api/local/projects/proj-1/deployment-runs/run-1/plan", status: 409, method: "PUT" });
	await dialog.getByRole("button", { name: "Apply to draft" }).click();
	await expect(dialog).toBeVisible();
	await expect.poll(() => recommendationRequests).toBe(2);
	await expect.poll(() => planUpdates).toBe(0);

	await dialog.getByRole("button", { name: "Apply to draft" }).click();
	await expect(dialog).toBeHidden();
	await expect.poll(() => planUpdates).toBe(1);
});

test("a verified deployment publishes exactly one selected service through the canonical exposure rollout", async ({ page }) => {
	const run = deploymentRun("succeeded");
	run.plan.target.hostname = "tcip.test.opsidev.site";
	run.plan.target.public_routes = "manual";
	let exposure: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		onExposure: (body) => { exposure = body; },
	});

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Repository is running" })).toBeVisible();
	await expect(page.getByRole("link", { name: "https://tcip" })).toHaveCount(0);
	await page.getByRole("combobox", { name: "Running service" }).selectOption("svc-web");
	const hostname = page.getByLabel("Public subdomain");
	await expect(hostname).toHaveAttribute("placeholder", "tcip");
	await expect(page.getByText(".test.opsidev.site", { exact: true })).toBeVisible();
	await page.getByRole("button", { name: "Publish or update service" }).click();
	await expect.poll(() => exposure).toMatchObject({
		schema_version: "opsi.exposure_mutation/v1",
		base_deployment_job_id: "dep-web",
		expected_state_hash: hash("e"),
		exposure: { service_key: "identity-web", service_port: 3000, hostname: "tcip", tls: { mode: "disabled" } },
	});
	await expect(page.getByRole("link", { name: "https://tcip.test.opsidev.site" })).toBeVisible();
	await hostname.fill("api.foo");
	await expect(page.getByRole("alert").filter({ hasText: "one available DNS label" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Publish or update service" })).toBeDisabled();
});

test("a verified automatic deployment lists every HTTPS endpoint while routes are applying", async ({ page }) => {
	const run = deploymentRun("succeeded");
	run.plan.target.hostname = "tcip.test.opsidev.site";
	run.plan.target.public_routes = "automatic";
	run.plan.applications[0].exposure = { mode: "public", hostname: "tcip.test.opsidev.site", path: "/api", automatic: true };
	run.plan.applications[1].exposure = { mode: "public", hostname: "tcip.test.opsidev.site", path: "/", automatic: true };
	await mockDeployAPI(page, () => run, () => run, "owner", undefined, {
		repository: () => sourceRepository("active"),
		result: () => ({ run_id: "run-1", state: "succeeded", source_sha: hash("d").slice(0, 40), applications: [{ service_key: "identity-api", service_id: "svc-api", build_record_id: "br-api", build_digest: `sha256:${hash("a")}`, deployment_job_id: "dep-api", deployment_status: "succeeded", container_port: 8080, available_replicas: 1, digest_matches_image_id: true }, { service_key: "identity-web", service_id: "svc-web", build_record_id: "br-web", build_digest: `sha256:${hash("b")}`, deployment_job_id: "dep-web", deployment_status: "succeeded", container_port: 3000, available_replicas: 1, digest_matches_image_id: true }], public_endpoints: [{ service_key: "identity-api", service_id: "svc-api", port: 8080, hostname: "tcip.test.opsidev.site", url: "https://tcip.test.opsidev.site/api", status: "publishing" }, { service_key: "identity-web", service_id: "svc-web", port: 3000, hostname: "tcip.test.opsidev.site", url: "https://tcip.test.opsidev.site/", status: "ready" }], verifications: [], capacity: [] }),
	});
	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "HTTPS routes" })).toBeVisible();
	await expect(page.getByText("https://tcip.test.opsidev.site/api")).toBeVisible();
	await expect(page.getByText("Publishing", { exact: true })).toBeVisible();
	await expect(page.getByRole("link", { name: "Open HTTPS URL" })).toHaveCount(1);
});

test("public hostname quota blocks a fourth label until release", async ({ page }) => {
	let used = 3;
	const allocation = { id: "phn-1", hostname: "old.test.opsidev.site", owner_user_id: "user-1", project_id: "proj-1", environment_id: "env-1", runtime_id: "runtime-1", status: "failed", publication_error: "Cloudflare could not publish the exact DNS record.", created_at: "2026-08-24T00:00:00Z", updated_at: "2026-08-24T00:00:00Z" };
	await mockDeployAPI(page, () => null, () => deploymentRun("awaiting_approval"), "owner", undefined, {
		repository: () => sourceRepository("active"),
		quota: () => ({ used, limit: 3, remaining: 3 - used, allocations: used ? [allocation, { ...allocation, id: "phn-2", hostname: "two.test.opsidev.site", project_id: "proj-2" }, { ...allocation, id: "phn-3", hostname: "three.test.opsidev.site", project_id: "proj-3" }] : [], project_allocations: used ? [allocation] : [] }),
		onHostnameAction: () => { used = 2; },
	});
	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "3/3 public hostnames used" })).toBeVisible();
	await page.getByLabel("Repository", { exact: true }).selectOption("7");
	await page.getByLabel("Public subdomain").fill("four");
	await expect(page.getByRole("button", { name: "Analyze repository" })).toBeDisabled();
	await page.getByRole("button", { name: "Release" }).click();
	await expect(page.getByRole("heading", { name: "2/3 public hostnames used" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Analyze repository" })).toBeEnabled();
});

test("review plan has no WCAG 2.1 A or AA axe violations", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run);

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("heading", { name: "Ready to deploy" })).toBeVisible();
	await page.getByText("View or edit full detected configuration").click();
	const publicSubdomain = page.getByLabel("Public subdomain (.test.opsidev.site)");
	await expect(publicSubdomain).toHaveValue("identity");
	await publicSubdomain.fill("api.foo");
	await expect(publicSubdomain).toHaveValue("api.foo");
	await expect(page.getByRole("button", { name: "Save draft" })).toBeDisabled();
	const results = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
	expect(results.violations, JSON.stringify(results.violations, null, 2)).toEqual([]);
});

test("connection mappings select protocol dialects and reject unsafe templates accessibly", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run);
	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();
	const dialect = page.getByLabel("Dialect / value").first();
	await expect(dialect.locator("option", { hasText: "Npgsql connection string" })).toHaveCount(1);
	await expect(dialect.locator("option", { hasText: "StackExchange.Redis" })).toHaveCount(0);
	await dialect.selectOption("connection.template");
	const template = page.getByLabel("Safe connection template");
	await expect(template).toBeFocused();
	await template.fill("{{password}}");
	await expect(page.getByRole("alert").filter({ hasText: "password requires" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Save draft" })).toBeDisabled();
	await template.fill("Host={{host}};Password={{password|kv_quote}}");
	const mapping = page.getByRole("article").filter({ has: template }).last();
	await expect(mapping).toContainText("Sensitivity: secret");
	await expect(mapping).toContainText("connection.template · redacted");
	await expect(mapping.getByRole("alert")).toHaveCount(0);
	await expect(page.getByRole("button", { name: "Save draft" })).toBeEnabled();
});

test("connection mapping protocol changes clear stale sources and enforce row contract", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run);
	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();
	const protocol = page.getByLabel("Protocol").first();
	const dialect = page.getByLabel("Dialect / value").first();
	await protocol.selectOption("nats");
	await expect(dialect).toHaveValue("");
	await expect(dialect).toBeFocused();
	await expect(page.getByRole("button", { name: "Save draft" })).toBeDisabled();

	await dialect.selectOption("connection.template");
	const template = page.getByLabel("Safe connection template");
	await template.fill("password={{password|kv_quote}}");
	await expect(page.getByRole("alert").filter({ hasText: "NATS templates cannot" })).toBeVisible();
	await template.fill("nats://{{host}}:{{port}}");
	await expect(page.getByRole("button", { name: "Save draft" })).toBeEnabled();

	const environment = page.getByLabel("Environment name").first();
	await environment.fill("PORT");
	await expect(page.getByRole("alert").filter({ hasText: "reserved by the platform" })).toBeVisible();
	await expect(environment).toHaveAttribute("aria-invalid", "true");
	await environment.fill("NATS_URL");
	await protocol.selectOption("postgres");
	await dialect.selectOption("connection.template");
	await page.getByLabel("Safe connection template").fill("{{password|url_query|kv_quote}}");
	await expect(page.getByRole("alert").filter({ hasText: "only one encoder segment" })).toBeVisible();
	await page.getByLabel("Safe connection template").fill("Password={{password|kv_quote}}");
	await page.getByRole("button", { name: "Add mapping" }).first().click();
	const environments = page.getByLabel("Environment name");
	await environments.nth(1).fill("NATS_URL");
	await expect(page.getByRole("alert").filter({ hasText: "unique across dependency mappings" })).toHaveCount(2);
	await expect(page.getByRole("button", { name: "Save draft" })).toBeDisabled();
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
	await page.getByText("View or edit full detected configuration").click();
	const input = page.getByLabel("Value for oauth-client");
	await input.fill("one-time-browser-secret");
	await page.getByRole("button", { name: "Store securely" }).click();
	await expect.poll(() => secretPayload).toEqual({ logical_name: "oauth-client", value: "one-time-browser-secret" });
	await expect(input).toHaveCount(0);
	await expect(page.getByText("oauth-client · Stored securely · revision 1", { exact: true })).toBeVisible();
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

test("failed run at the retry limit offers a new deployment instead of an invalid retry", async ({ page }) => {
	const run = deploymentRun("failed");
	run.attempt = run.plan.failure_policy.max_attempts;
	await mockDeployAPI(page, () => run, () => run);

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByRole("button", { name: "Retry failed step" })).toHaveCount(0);
	await expect(page.getByRole("button", { name: "Export configuration" })).toHaveCount(0);
	await page.getByRole("button", { name: "New deployment" }).last().click();
	await expect(page.getByRole("heading", { name: "Choose a repository to deploy" })).toBeVisible();
});

test("multi-app runtime configuration enforces review gating, generated key sufficiency, and confirmation", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	run.plan.application_environment_reviews = [];
	await mockDeployAPI(page, () => run, () => run, "owner");

	await page.goto("/?project=proj-1&view=deploy");
	await expect(page.getByText('Review required: "identity-web" requires runtime configuration or confirmation.')).toBeVisible();
	await expect(page.getByRole("button", { name: "Approve & Deploy" })).toBeDisabled();

	await page.getByText("View or edit full detected configuration").click();
	await expect(page.getByRole("link", { name: "Review identity-web" })).toBeVisible();

	const apiRuntime = page.locator("#application-runtime-0");
	await expect(apiRuntime.getByText("2 runtime keys")).toBeVisible();
	const webRuntime = page.locator("#application-runtime-1");
	await expect(webRuntime.getByText("Needs review")).toBeVisible();

	const confirmCheck = webRuntime.getByLabel("This application does not require environment variables or secrets.");
	await expect(confirmCheck).toBeVisible();
	await expect(confirmCheck).not.toBeChecked();

	await confirmCheck.check();
	await expect(webRuntime.getByText("No keys required — confirmed")).toBeVisible();
	await expect(page.getByText('Review required: "identity-web" requires runtime configuration or confirmation.')).toHaveCount(0);
});

test("removing the last runtime key forces confirmation review again and blocks save", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	run.plan.applications[1].environment = { FEATURE_FLAG: "true" };
	run.plan.application_environment_reviews = [];
	await mockDeployAPI(page, () => run, () => run, "owner");

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();

	const webRuntime = page.locator("#application-runtime-1");
	await expect(webRuntime.getByText("1 runtime key")).toBeVisible();
	await expect(webRuntime.getByText("FEATURE_FLAG")).toBeVisible();

	await webRuntime.getByRole("button", { name: "Remove" }).click();
	await expect(webRuntime.getByText("Needs review")).toBeVisible();
	await expect(page.getByRole("button", { name: "Save Draft" })).toBeDisabled();
});

test("plain environment blocks secret-like names and directs to Add secret", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run, "owner");

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();

	const apiRuntime = page.locator("#application-runtime-0");
	const keyInput = apiRuntime.getByPlaceholder("VARIABLE_NAME");
	await keyInput.fill("DATABASE_PASSWORD");
	const valInput = apiRuntime.getByPlaceholder("value");
	await valInput.fill("secret123");
	await apiRuntime.getByRole("button", { name: "Add variable" }).click();

	await expect(apiRuntime.getByText("Secret-like keys such as PASSWORD, API_KEY, and TOKEN must use Add secret.")).toBeVisible();
});

test("secret value crosses the one-way API once and only metadata enters the deployment plan", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	run.plan.applications[1].environment = {};
	run.plan.application_environment_reviews = [];
	let secretRequest: Record<string, unknown> | undefined;
	let savedPlan: Record<string, unknown> | undefined;
	await mockDeployAPI(page, () => run, () => run, "owner", (body) => {
		secretRequest = body;
		return { id: "wsecret-web", reference: "workload-secret://wsecret-web", project_id: "proj-1", service_id: "planned:identity-web", logical_name: String(body.logical_name), revision: 1, status: "ready", updated_at: "2026-09-04T00:00:00Z" };
	}, { repository: () => sourceRepository("active"), onPlanUpdate: (body) => { savedPlan = body.plan as Record<string, unknown>; return { ...run, plan: body.plan as DeploymentRun["plan"], revision: run.revision + 1 }; } });

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();
	const runtime = page.locator("#application-runtime-1");
	await runtime.getByRole("button", { name: "Add secret" }).click();
	await runtime.getByLabel("Environment key").fill("SESSION_SECRET");
	await runtime.getByLabel("Logical secret name").fill("session-secret");
	await runtime.getByLabel("Secret value").fill("one-way-value");
	await runtime.getByRole("button", { name: "Store securely" }).click();

	await expect(runtime.getByText("Stored securely · revision 1")).toBeVisible();
	expect(secretRequest).toEqual({ logical_name: "session-secret", value: "one-way-value" });
	await page.getByRole("button", { name: "Save Draft" }).click();
	await expect.poll(() => savedPlan).toBeTruthy();
	expect(JSON.stringify(savedPlan)).not.toContain("one-way-value");
	expect(JSON.stringify(savedPlan)).toContain("workload-secret://wsecret-web");
});

test("viewer role sees runtime configuration metadata but no edit controls", async ({ page }) => {
	const run = deploymentRun("awaiting_approval");
	await mockDeployAPI(page, () => run, () => run, "viewer");

	await page.goto("/?project=proj-1&view=deploy");
	await page.getByText("View or edit full detected configuration").click();

	const apiRuntime = page.locator("#application-runtime-0");
	await expect(apiRuntime.getByText("2 runtime keys")).toBeVisible();
	await expect(apiRuntime.getByRole("button", { name: "Add variable" })).toHaveCount(0);
	await expect(apiRuntime.getByRole("button", { name: "Add secret" })).toHaveCount(0);
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
	await page.getByLabel("Public subdomain").fill("identity");
	await expect(page).toHaveURL(/source_repository=7/);
	await page.reload();
	await expect(page.getByLabel("Repository", { exact: true })).toHaveValue("7");
	await expect(page.getByLabel("Branch or ref")).toHaveValue("developer");
	await expect(page.getByLabel("Public subdomain")).toHaveValue("identity");
	await page.getByRole("button", { name: "Claim & analyze repository" }).click();
	await expect(page.getByRole("heading", { name: "Ready to deploy" })).toBeVisible();
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
	await page.getByLabel("Public subdomain").fill("identity");
	await page.getByRole("button", { name: "Analyze repository" }).click();
	await expect(page.getByRole("heading", { name: "Ready to deploy" })).toBeVisible();
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
	await page.getByLabel("Public subdomain").fill("identity");
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
	onPlanUpdate?: (body: Record<string, unknown>) => ReturnType<typeof deploymentRun>;
	planUpdateFailure?: () => { status: number; body: Record<string, unknown> } | undefined;
	recommendation?: () => Record<string, unknown>;
	claimFailure?: boolean;
	onLoginStart?: () => void;
	onExport?: () => void;
	bootstrapSession?: () => Record<string, unknown>;
	bootstrapEvents?: () => Array<Record<string, unknown>>;
	placement?: () => ReturnType<typeof placementFacts>;
	onExposure?: (body: Record<string, unknown>) => void;
	result?: () => Record<string, unknown>;
	quota?: () => Record<string, unknown>;
	onHostnameAction?: () => void;
	hostKeyProbe?: (data: { public_host: string; ssh_port: number }) => Record<string, unknown>;
	onResume?: () => void;
};
type AuthSelectionBehavior = { authenticated: boolean; selections: number };

async function mockDeployAPI(page: Page, current: () => ReturnType<typeof deploymentRun> | null, action: (body: Record<string, unknown>, name?: string) => ReturnType<typeof deploymentRun>, role = "owner", secretAction?: (body: Record<string, unknown>) => Record<string, unknown>, source?: SourceBehavior, authSelection?: AuthSelectionBehavior) {
	await page.route("**/api/local/**", async (route) => {
		const request = route.request();
		const path = new URL(request.url()).pathname;
		let body: unknown = {};
		if (/\/workload-secrets$/.test(path) && request.method() === "PUT" && secretAction) body = { workload_secret: secretAction(request.postDataJSON()), reused: false };
		else if (/\/workload-secrets$/.test(path) && request.method() === "GET") body = { workload_secrets: [] };
		else if (path === "/api/local/session/login/start" && request.method() === "POST" && source) { source.onLoginStart?.(); const login = request.postDataJSON(); const query = new URLSearchParams(String(login.return_query || "")); query.set("project", "proj-1"); query.set("view", "deploy"); query.set("auth", "ok"); body = { auth_url: "/?" + query.toString(), status: "pending" }; }
		else if (path === "/api/local/session/selection" && authSelection) body = { selection_id: "selection-1", projects: [{ id: "proj-1", name: "dada", role }] };
		else if (path === "/api/local/session/select-project" && request.method() === "POST" && authSelection) { authSelection.authenticated = true; authSelection.selections += 1; body = { authenticated: true, session: { user_id: "user-1", org_id: "org-1", project_id: "proj-1", role } }; }
		else if (/\/github\/repositories\/7\/claim$/.test(path) && request.method() === "POST" && source?.claimFailure) return fulfill(route, { error: { code: "CLOUD_AUTH_REQUIRED", message: "Cloud rejected the saved credential", next_action: "Sign in again." } }, 401);
		else if (/\/github\/repositories\/7\/claim$/.test(path) && request.method() === "POST" && source) { source.onClaim?.(); body = { repository_id: 7, project_id: "proj-1", status: "active" }; }
		else if (path.endsWith("/repository-export/preview") && request.method() === "POST") body = { run_id: "run-1", run_revision: 3, plan_hash: hash("a"), source_sha: hash("d").slice(0, 40), repository_id: 7, target_branch: "main", path: ".opsi/opsi-cd.yaml", yaml: "version: 2\n", diff: "+version: 2\n", preview_hash: hash("e"), export_enabled: true };
		else if (path.endsWith("/repository-export") && request.method() === "POST") { source?.onExport?.(); body = { repository_export: { branch: "opsi/export-run", commit_sha: hash("f").slice(0, 40), pull_request_number: 9, pull_request_url: "https://github.test/pr/9", reused: false } }; }
		else if (path.endsWith("/resource-recommendation") && request.method() === "GET") {
			body = {
				recommendation: source?.recommendation?.() ?? resourceRecommendation(hash("d")),
			};
		}
		else if (/\/deployment-runs\/run-1\/plan$/.test(path) && request.method() === "PUT") {
			const putData = request.postDataJSON();
			const failure = source?.planUpdateFailure?.();
			if (failure) return fulfill(route, failure.body, failure.status);
			if (source?.onPlanUpdate) {
				body = source.onPlanUpdate(putData);
			} else {
				body = { ...current()!, plan: putData.plan, revision: (current()?.revision ?? 1) + 1 };
			}
		}
		else if (path.endsWith("/deployment-runs") && request.method() === "POST" && source?.onCreate) body = { deployment_run: source.onCreate(), reused: false };
		else if (/\/public-hostnames\/[^/]+\/(release|retry)$/.test(path) && request.method() === "POST") { source?.onHostnameAction?.(); body = { id: "phn-1", status: "released" }; }
		else if (path === "/api/local/session") body = authSelection && !authSelection.authenticated ? { authenticated: false, cloud_connected: "ok", agent_connected: "ok", token_status: "missing", local_session: "local-session" } : { authenticated: true, cloud_connected: "ok", agent_connected: "ok", token_status: "valid", local_session: "local-session", user_id: "user-1", org_id: "org-1", project_id: "proj-1", role, capabilities: [] };
		else if (path === "/api/local/session/project") body = { status: "selected", project_id: "proj-1" };
		else if (path === "/api/local/projects") body = { projects: [{ id: "proj-1", org_id: "org-1", name: "Identity", slug: "identity", status: "ready" }] };
		else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
		else if (path.endsWith("/github/installations/discover")) body = { installations: [] };
		else if (path.endsWith("/github/installations")) body = { installations: [{ installation_id: 9, account_login: "acme", account_type: "Organization", status: "active" }] };
		else if (path.endsWith("/github/repositories")) body = { repositories: [source?.repository() ?? sourceRepository("active")] };
		else if (path.endsWith("/deployment-runs")) body = { deployment_runs: current() ? [current()] : [] };
		else if (path.endsWith("/public-hostnames")) body = source?.quota?.() ?? { used: 0, limit: 3, remaining: 3, allocations: [], project_allocations: [] };
		else if (/\/deployment-runs\/run-1\/events$/.test(path)) { const run = current(); body = { events: run ? [{ id: "event-1", project_id: "proj-1", run_id: "run-1", state: run.state, level: "info", message: run.state === "provisioning" ? "Plan approved; provisioning started." : "Repository analysis is ready for review.", created_at: "2026-08-24T00:00:00Z" }] : [] }; }
		else if (path.endsWith("/exposures/preview") && request.method() === "POST") { const mutation = request.postDataJSON(); body = { schema_version: "opsi.exposure_preview/v1", base_deployment_job_id: mutation.base_deployment_job_id, desired: mutation.exposure, changes: ["create exposure"], state_hash: hash("e"), eligible: true, decision_code: "EXPOSURE_READY", message: "ready", resolved_at: "2026-08-24T00:00:00Z" }; }
		else if (path.endsWith("/exposures") && request.method() === "POST") { source?.onExposure?.(request.postDataJSON()); body = { schema_version: "opsi.deployment_job/v1", id: "dep-exposure", project_id: "proj-1", environment_id: "env-1", runtime_id: "runtime-1", service_id: "svc-web", mode: "rollout", status: "succeeded", rollout_state: "succeeded" }; }
		else if (/\/deployment-runs\/run-1\/result$/.test(path)) { const run = current(); body = source?.result?.() ?? { run_id: "run-1", state: run?.state ?? "awaiting_input", source_sha: hash("d").slice(0, 40), applications: run?.state === "succeeded" ? [{ service_key: "identity-api", service_id: "svc-api", build_record_id: "br-api", build_digest: `sha256:${hash("a")}`, deployment_job_id: "dep-api", deployment_status: "succeeded", container_port: 8080, available_replicas: 1, digest_matches_image_id: true }, { service_key: "identity-web", service_id: "svc-web", build_record_id: "br-web", build_digest: `sha256:${hash("b")}`, deployment_job_id: "dep-web", deployment_status: "succeeded", container_port: 3000, available_replicas: 1, digest_matches_image_id: true }] : [], verifications: [], capacity: [] }; }
		else if (/\/deployment-runs\/run-1\/(approve|acknowledge|analyze|retry|cancel)$/.test(path)) body = action(request.postDataJSON(), path.split("/").at(-1));
		else if (/\/deployment-runs\/run-1$/.test(path)) body = current();
		else if (path.endsWith("/topology/facts")) body = source?.placement?.() ?? placementFacts();
		else if (path.endsWith("/topology")) body = { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 1, state_hash: hash("b"), plan_hash: hash("c"), assignments: [] };
		else if (path.endsWith("/nodes")) body = { nodes: [{ id: "node-1", name: "server-1", role: "primary", status: "healthy", public_host: "103.252.137.163" }] };
		else if (path.endsWith("/services")) body = { services: [] };
		else if (path.endsWith("/deployments")) body = { deployments: [] };
		else if (/\/bootstrap-sessions\/boot-1\/events$/.test(path) && source?.bootstrapEvents) body = source.bootstrapEvents();
		else if (path.endsWith("/bootstrap-sessions")) body = { sessions: source?.bootstrapSession ? [source.bootstrapSession()] : [] };
		else if (path.endsWith("/ssh-host-key-probes") && request.method() === "POST") {
			const postData = request.postDataJSON() as { public_host: string; ssh_port: number };
			body = source?.hostKeyProbe?.(postData) ?? {
				id: "probe-1",
				probe_id: "probe-1",
				project_id: "proj-1",
				public_host: postData.public_host || "103.252.137.163",
				ssh_port: postData.ssh_port || 22,
				resolved_ip: postData.public_host || "103.252.137.163",
				algorithm: "ssh-ed25519",
				fingerprint: "SHA256:4t7EExampleMockFingerprintAAAA",
				trust_state: "first_seen",
				status: "pending",
				expires_at: "2026-09-05T12:00:00Z",
				created_at: "2026-09-05T11:50:00Z",
			};
		}
		else if (/\/ssh-host-key-probes\/[^/]+\/confirm$/.test(path) && request.method() === "POST") {
			body = {
				id: "trust-1",
				trust_id: "trust-1",
				project_id: "proj-1",
				host: "103.252.137.163",
				port: 22,
				algorithm: "ssh-ed25519",
				fingerprint: "SHA256:4t7EExampleMockFingerprintAAAA",
				status: "active",
				created_at: "2026-09-05T11:50:00Z",
			};
		}
		else if (/\/bootstrap-sessions\/[^/]+\/resume$/.test(path) && request.method() === "POST") {
			source?.onResume?.();
			body = { id: "boot-1", status: "pending", public_host: "103.252.137.163", role: "first_server", auth_method: "password" };
		}
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

function resourceRecommendation(basisHash: string) {
	return {
		eligible: true,
		runtime_id: "runtime-1",
		node_id: "node-1",
		target_capacity: { cpu_millicores: 2000, memory_bytes: 4096 * 1024 * 1024, source: "agent_observed", heartbeat_age_seconds: 5, heartbeat_fresh: true },
		budget_projection: {
			real_capacity: { cpu_millicores: 2000, memory_bytes: 4096 * 1024 * 1024 },
			system_reserve: { cpu_millicores: 250, memory_bytes: 256 * 1024 * 1024 },
			existing_workloads: { cpu_millicores: 0, memory_bytes: 0 },
			planned_managed: { cpu_millicores: 200, memory_bytes: 512 * 1024 * 1024 },
			available_for_run: { cpu_millicores: 1550, memory_bytes: 3328 * 1024 * 1024 },
			remaining_after_proposal: { cpu_millicores: 950, memory_bytes: 2304 * 1024 * 1024 },
		},
		basis: { run_revision: 3, plan_hash: hash("a"), topology_revision: 1, topology_hash: hash("b"), capacity_state_hash: hash("c"), basis_hash: basisHash, observed_at: "2026-08-24T00:00:00Z" },
		applications: [
			{ key: "identity-api", name: "identity-api", replicas: 1, current: { cpu_request_milli: 250, cpu_limit_milli: 250, memory_request_bytes: 256 * 1024 * 1024, memory_limit_bytes: 256 * 1024 * 1024 }, proposed: { cpu_request_milli: 300, cpu_limit_milli: 1000, memory_request_bytes: 512 * 1024 * 1024, memory_limit_bytes: 1024 * 1024 * 1024 } },
			{ key: "identity-web", name: "identity-web", replicas: 1, current: { cpu_request_milli: 250, cpu_limit_milli: 250, memory_request_bytes: 256 * 1024 * 1024, memory_limit_bytes: 256 * 1024 * 1024 }, proposed: { cpu_request_milli: 300, cpu_limit_milli: 1000, memory_request_bytes: 512 * 1024 * 1024, memory_limit_bytes: 1024 * 1024 * 1024 } },
		],
		warnings: [],
	};
}

function deploymentRun(state: "awaiting_input" | "awaiting_approval" | "awaiting_warning_ack" | "provisioning" | "building" | "deploying" | "succeeded" | "failed" | "stale" | "cancelled"): DeploymentRun {
	return {
		schema_version: "opsi.deployment_run/v3", id: "run-1", project_id: "proj-1", created_by: "user-1", state,
		plan: {
			schema_version: "opsi.deployment_plan/v3", hash: hash("a"),
			source: { repository_id: 7, installation_id: 9, repository: "acme/identity-service", selected_ref: "main", commit_sha: hash("d").slice(0, 40) },
			applications: [
				{ source_key: "api", key: "identity-api", name: "identity-api", root: "be", port: 8080, build: { context: "be", dockerfile_path: "be/Dockerfile", strategy: "dockerfile", platform: "linux/amd64" }, confidence: "high", reason: "Dockerfile", evidence: [] },
				{ source_key: "web", key: "identity-web", name: "identity-web", root: "tcip-fake", port: 3000, build: { context: "tcip-fake", dockerfile_path: "tcip-fake/Dockerfile", strategy: "dockerfile", platform: "linux/amd64" }, confidence: "high", reason: "Dockerfile", evidence: [] },
			],
			resources: [{ logical_name: "postgres", type: "postgres", managed: true, required: true, recommendation: "Managed PostgreSQL", confidence: "high", evidence: [] }, { logical_name: "valkey", type: "redis", managed: true, required: true, recommendation: "Managed Valkey", confidence: "high", evidence: [] }],
			dependencies: [{ from: "identity-api", to: "postgres", protocol: "postgres", required: true, confidence: "high", reason: "Compose", evidence: [], injections: [{ environment_name: "ConnectionStrings__Database", symbolic_source: "connection.postgres.npgsql" }] }, { from: "identity-web", to: "identity-api", protocol: "http", strategy: "same_origin", path: "/api", required: true, verification: { type: "consumer_http", path: "/health", expected_status: 200 }, confidence: "high", reason: "Route", evidence: [] }],
			bindings: [{ from: "identity-web", to: "identity-api", kind: "browser_http", path: "/api", confidence: "high", reason: "Route", evidence: [] }],
			secrets: [{ name: "jwt-signing-key", application_key: "identity-api", environment_name: "Jwt__SigningKey", generated: true, secret_ref: "generated://jwt-signing-key", revision: 0, display: "Generated and securely stored", confidence: "high", reason: "Configuration", evidence: [] }],
			application_environment_reviews: [{ application_source_key: "web", no_environment_required: true }],
			issues: [], analysis_scope: { application_roots: [], exclude_paths: [] }, analysis_scope_hash: hash("s"), evidence_coverage: { candidates_found: 6, candidates_selected: 6, files_inspected: 6, bytes_inspected: 2048 }, target: { environment_id: "env-1", runtime_id: "runtime-1", node_id: "node-1", hostname: "identity.test.opsidev.site", exposure: "public", public_routes: "automatic", cpu_milli: 250, memory_bytes: 268435456 }, authority_revisions: { source_commit_sha: hash("d").slice(0, 40) }, failure_policy: { fail_fast: true, rollback_known_good: true, retain_persistent_data: true, max_attempts: 3 },
		},
		analysis: { authority: "compose", issues: [], files_inspected: 6, bytes_inspected: 2048, truncated: false }, authority_refs: { checkpoints: [] }, preflight_hash: state === "awaiting_warning_ack" ? hash("p") : undefined, failure: state === "failed" ? { step: "building", code: "BUILD_AUTHORITY_UNAVAILABLE", message: "Build authority unavailable.", next_action: "Retry the failed step.", retryable: true } : state === "stale" ? { step: "preflighting", code: "DEPLOYMENT_PLAN_STALE", message: "An authority changed.", next_action: "Analyze and review again.", retryable: false } : undefined, attempt: state === "awaiting_approval" ? 0 : 1, revision: state === "awaiting_approval" ? 3 : 4, created_at: "2026-08-24T00:00:00Z", updated_at: "2026-08-24T00:00:00Z",
	} as unknown as DeploymentRun;
}
