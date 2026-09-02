import { expect, test, type Page, type Route } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

const hash = (value: string) => value.repeat(64).slice(0, 64);

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("AI Assistant chats through Codex and requires review, approval, then apply", async ({ page }) => {
  const actions: string[] = [];
  await mockAssistantAPI(page, actions);
  await page.goto("/?project=proj-1&view=assistant");

  await expect(page.getByRole("heading", { name: "AI Assistant", exact: true })).toBeVisible();
  await expect(page.getByText("Connected", { exact: true })).toBeVisible();
  await expect(page.getByText("Provider authenticated", { exact: true })).toBeVisible();
  await expect(page.getByText("Opsi Cloud PAT valid", { exact: true })).toBeVisible();
  await expect(page.getByText(/read-only Opsi MCP server/i)).toBeVisible();
  await page.getByLabel("Message AI Assistant").fill("Review frontend and backend routing");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Use one origin and route the API under /api.")).toBeVisible();
  await expect(page.getByText("Grounded by 3 Opsi MCP calls")).toBeVisible();
  await expect(page.getByRole("button", { name: "Create review" })).toBeVisible();

  await page.getByRole("button", { name: "Create review" }).click();
  await expect(page.getByText("set user_environment")).toBeVisible();
  await page.getByRole("button", { name: "Approve" }).click();
  await expect(page.getByRole("button", { name: "Apply configuration" })).toBeVisible();
  await page.getByRole("button", { name: "Apply configuration" }).click();
  await expect(page.getByText("applied", { exact: true })).toBeVisible();
  expect(actions).toEqual(["create_review", "approve", "apply"]);

  const accessibility = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(accessibility.violations, JSON.stringify(accessibility.violations, null, 2)).toEqual([]);
});

async function mockAssistantAPI(page: Page, actions: string[]) {
  await page.route("**/api/local/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown = {};
    if (path === "/api/local/session") body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", token_status: "valid", local_session: "local-session", user_id: "user-1", org_id: "org-1", project_id: "proj-1", role: "owner", capabilities: [] };
    else if (path === "/api/local/projects") body = { projects: [{ id: "proj-1", org_id: "org-1", name: "Identity", slug: "identity", status: "ready" }] };
    else if (path === "/api/local/ai/providers") body = { mcp_surface: "mcp-04", providers: [{ id: "codex", name: "OpenAI Codex", available: true, authenticated: true, version: "codex-cli 0.151.0", capabilities: ["project_review", "configuration_advice"], data_boundary: "Project data is available to the agent only through the read-only Opsi MCP server." }] };
    else if (/\/assistant\/turns$/.test(path) && request.method() === "POST") body = { id: "turn-1", conversation_id: "conversation-1", provider_id: "codex", project_id: "proj-1", state: "running", started_at: "2026-08-30T00:00:00Z" };
    else if (/\/assistant\/turns\/turn-1$/.test(path)) body = { id: "turn-1", conversation_id: "conversation-1", provider_id: "codex", project_id: "proj-1", state: "succeeded", response: "Use one origin and route the API under /api.", grounding: { status: "verified", successful_tool_calls: 3, tools: ["project_review_context", "validate_service_configuration_proposal", "topology"] }, configuration_proposals: [{ application_id: "svc-web", application_name: "web", environment_id: "env-1", rationale: "Keep the browser on one origin.", expected_revision: 5, expected_state_hash: hash("a"), analysis_inputs_hash: hash("b"), draft_json: JSON.stringify({ schema_version: "opsi.service_configuration/v1", environment: [{ name: "API_BASE_PATH", value: "/api" }] }) }], started_at: "2026-08-30T00:00:00Z" };
    else if (/\/services\/svc-web\/configuration$/.test(path)) body = { schema_version: "opsi.service_configuration/v1", revision: 5, state_hash: hash("a") };
    else if (path.endsWith("/configuration/validate")) body = { valid: true };
    else if (path.endsWith("/configuration/diff")) body = { changes: [{ kind: "user_environment", action: "set", name: "API_BASE_PATH", after: "/api" }] };
    else if (path.endsWith("/services/svc-web/proposal-reviews") && request.method() === "POST") { actions.push("create_review"); body = review("review_required"); }
    else if (path.endsWith("/proposal-reviews/review-1/approve")) { actions.push("approve"); body = review("approved"); }
    else if (path.endsWith("/proposal-reviews/review-1/apply")) { actions.push("apply"); body = review("applied"); }
    else if (path.endsWith("/readiness")) body = { project_id: "proj-1", status: "ready", can_deploy: true };
    else if (path.endsWith("/topology/facts")) body = { environments: [{ id: "env-1", name: "Production", status: "active" }], runtimes: [], nodes: [] };
    else if (path.endsWith("/topology")) body = { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 1, state_hash: hash("c"), plan_hash: hash("d"), assignments: [] };
    else if (path.endsWith("/nodes")) body = { nodes: [] };
    else if (path.endsWith("/services")) body = { services: [{ id: "svc-web", name: "web", type: "application", status: "ready", source_type: "git" }] };
    else if (path.endsWith("/deployments")) body = { deployments: [] };
    else if (path.endsWith("/bootstrap-sessions")) body = { sessions: [] };
    else if (path.endsWith("/build-records")) body = { records: [] };
    else if (path.endsWith("/audit")) body = { events: [] };
    else if (path.endsWith("/incidents")) body = { source: "agent", payload_policy: "redacted", incidents: [] };
    else if (path.endsWith("/support")) body = { generated_at: "2026-08-30T00:00:00Z", counts: {}, signals: [] };
    await json(route, body);
  });
}

function review(status: string) {
  return { id: "review-1", project_id: "proj-1", environment_id: "env-1", application_id: "svc-web", kind: "service_configuration", status, proposal_hash: hash("e"), analysis_inputs_hash: hash("b"), normalized_payload: {}, reviewed_payload_hash: hash("f"), expected_configuration_revision: 5, expected_configuration_state_hash: hash("a"), created_at: "2026-08-30T00:00:00Z", expires_at: "2026-08-31T00:00:00Z" };
}

async function json(route: Route, body: unknown) { await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 }); }
