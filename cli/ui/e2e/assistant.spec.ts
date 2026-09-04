import { expect, test, type Page, type Route } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { expectNoConsoleErrors, watchConsoleErrors } from "./console-errors";

const hash = (value: string) => value.repeat(64).slice(0, 64);

test.beforeEach(async ({ page }) => watchConsoleErrors(page));
test.afterEach(async ({ page }) => expectNoConsoleErrors(page));

test("AI Assistant chats through Codex and requires review, approval, then apply", async ({ page }) => {
  const actions: string[] = [];
  await mockAssistantAPI(page, { actions });
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

test("AI Assistant displays live progress bubble, trace details, and handles AUTH_REQUIRED error with Retry", async ({ page }) => {
  let pollCount = 0;
  let retried = false;

  await mockAssistantAPI(page, {
    onTurn: (path, method) => {
      if (/\/assistant\/turns$/.test(path) && method === "POST") {
        return { id: "turn-progress-1", conversation_id: "conv-live", provider_id: "codex", project_id: "proj-1", state: "running", started_at: "2026-09-03T00:00:00Z" };
      }
      if (/\/assistant\/turns\/turn-progress-1$/.test(path)) {
        pollCount++;
        if (pollCount === 1) {
          return {
            id: "turn-progress-1",
            conversation_id: "conv-live",
            provider_id: "codex",
            project_id: "proj-1",
            state: "running",
            started_at: "2026-09-03T00:00:00Z",
            progress: [
              { sequence: 1, phase: "queued", summary: "Đang xếp hàng thực thi", timestamp: "2026-09-03T00:00:01Z" },
              { sequence: 2, phase: "tool_running", tool: "deployments_list", summary: "Đang lấy lịch sử deployment", timestamp: "2026-09-03T00:00:02Z" },
            ],
          };
        }
        return {
          id: "turn-progress-1",
          conversation_id: "conv-live",
          provider_id: "codex",
          project_id: "proj-1",
          state: "failed",
          error_code: "AUTH_REQUIRED",
          error: "Opsi local session unauthenticated",
          next_action: "Run 'opsi login' outside MCP to authenticate.",
          started_at: "2026-09-03T00:00:00Z",
          finished_at: "2026-09-03T00:00:05Z",
          progress: [
            { sequence: 1, phase: "queued", summary: "Đang xếp hàng thực thi", timestamp: "2026-09-03T00:00:01Z" },
            { sequence: 2, phase: "tool_running", tool: "deployments_list", summary: "Đang lấy lịch sử deployment", timestamp: "2026-09-03T00:00:02Z" },
            { sequence: 3, phase: "tool_failed", tool: "deployments_list", code: "AUTH_REQUIRED", summary: "Công cụ deployments_list thất bại", timestamp: "2026-09-03T00:00:03Z" },
          ],
        };
      }
      if (/\/assistant\/turns\/turn-progress-1\/retry$/.test(path) && method === "POST") {
        retried = true;
        return {
          id: "turn-progress-2",
          conversation_id: "conv-live",
          provider_id: "codex",
          project_id: "proj-1",
          state: "running",
          started_at: "2026-09-03T00:00:10Z",
        };
      }
      if (/\/assistant\/turns\/turn-progress-2$/.test(path)) {
        return {
          id: "turn-progress-2",
          conversation_id: "conv-live",
          provider_id: "codex",
          project_id: "proj-1",
          state: "succeeded",
          response: "Retried review completed successfully.",
          grounding: { status: "verified", successful_tool_calls: 1, tools: ["deployments_list"] },
          started_at: "2026-09-03T00:00:10Z",
          finished_at: "2026-09-03T00:00:12Z",
        };
      }
      return null;
    },
  });

  await page.goto("/?project=proj-1&view=assistant");
  await page.getByLabel("Message AI Assistant").fill("Review deployment status");
  await page.getByRole("button", { name: "Send" }).click();

  // 1. Verify failure card rendered in chat with code, message, and next action
  await expect(page.getByText("AUTH_REQUIRED", { exact: true })).toBeVisible();
  await expect(page.getByText("Opsi local session unauthenticated")).toBeVisible();
  await expect(page.getByText("Run 'opsi login' outside MCP to authenticate.")).toBeVisible();

  // 2. Open Technical details trace
  const details = page.getByText(/Technical details/i).first();
  await expect(details).toBeVisible();
  await details.click();
  await expect(page.getByText("deployments_list").first()).toBeVisible();

  // 3. Retry turn directly from chat
  const retryBtn = page.getByRole("button", { name: "Retry" }).first();
  await expect(retryBtn).toBeVisible();
  await retryBtn.click();
  await expect(page.getByText("Retried review completed successfully.")).toBeVisible();
  expect(retried).toBe(true);

  // 4. Verify WCAG AA accessibility
  const accessibility = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(accessibility.violations, JSON.stringify(accessibility.violations, null, 2)).toEqual([]);
});

test("AI Assistant restores recent chat from local history and supports New Chat and Delete", async ({ page }) => {
  let deleted = false;
  const conversationList = [
    {
      id: "conv-saved-1",
      project_id: "proj-1",
      provider_id: "codex",
      title: "Earlier security check",
      created_at: "2026-09-02T10:00:00Z",
      updated_at: "2026-09-02T10:05:00Z",
      message_count: 2,
      last_turn_state: "succeeded",
    },
  ];

  const storedConversation = {
    id: "conv-saved-1",
    project_id: "proj-1",
    provider_id: "codex",
    title: "Earlier security check",
    created_at: "2026-09-02T10:00:00Z",
    updated_at: "2026-09-02T10:05:00Z",
    messages: [
      {
        id: "msg-1",
        turn_id: "turn-saved-1",
        role: "user",
        text: "Check deployment readiness",
        created_at: "2026-09-02T10:00:00Z",
      },
      {
        id: "msg-2",
        turn_id: "turn-saved-1",
        role: "assistant",
        text: "The topology has 1 service ready and 0 drift.",
        state: "succeeded",
        grounding: { status: "verified", successful_tool_calls: 2, tools: ["topology", "project_context"] },
        created_at: "2026-09-02T10:01:00Z",
      },
    ],
  };

  await mockAssistantAPI(page, {
    getConversations: () => (deleted ? [] : conversationList),
    getConversationDetail: (id) => (id === "conv-saved-1" ? storedConversation : null),
    onDeleteConversation: (id) => {
      if (id === "conv-saved-1") deleted = true;
    },
  });

  await page.goto("/?project=proj-1&view=assistant");

  // 1. Check restored message from history on load
  await expect(page.getByText("Check deployment readiness")).toBeVisible();
  await expect(page.getByText("The topology has 1 service ready and 0 drift.")).toBeVisible();

  // 2. Test Recent chats toggle
  await expect(page.getByRole("button", { name: /Recent chats/i })).toBeVisible();
  await page.getByRole("button", { name: /Recent chats/i }).click();
  await expect(page.getByText("Earlier security check").first()).toBeVisible();

  // 3. Test New Chat button
  await page.getByRole("button", { name: "New chat" }).click();
  await expect(page.getByText(/Suggested Review Prompts|Ask from current Opsi facts/i)).toBeVisible();

  // 4. Test Delete chat via recent chats
  await page.getByRole("button", { name: /Recent chats/i }).click();
  const deleteBtn = page.getByLabel("Delete chat: Earlier security check");
  await expect(deleteBtn).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await deleteBtn.click();
  await expect.poll(() => deleted).toBe(true);

  // 5. Accessibility audit
  const accessibility = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
  expect(accessibility.violations, JSON.stringify(accessibility.violations, null, 2)).toEqual([]);
});

test("AI Assistant chat panel respects responsive bounds and scrolls properly across viewports (1366x768, 1440x900, 390x844)", async ({ page }) => {
  const longMessages = Array.from({ length: 16 }, (_, i) => [
    { id: `user-msg-${i}`, role: "user", text: `User message number ${i + 1} asking about deployment topology and configurations in Opsi.` },
    { id: `asst-msg-${i}`, role: "assistant", text: `Assistant response number ${i + 1} detailing topology findings and validation steps.`, state: "succeeded", grounding: { status: "verified", successful_tool_calls: 2, tools: ["topology", "project_context"] } },
  ]).flat();

  const storedConv = {
    id: "conv-long",
    project_id: "proj-1",
    provider_id: "codex",
    title: "Long conversation",
    created_at: "2026-09-02T10:00:00Z",
    updated_at: "2026-09-02T10:05:00Z",
    messages: longMessages,
  };

  await mockAssistantAPI(page, {
    getConversations: () => [{ id: "conv-long", project_id: "proj-1", provider_id: "codex", title: "Long conversation", created_at: "2026-09-02T10:00:00Z", updated_at: "2026-09-02T10:05:00Z", message_count: 32, last_turn_state: "succeeded" }],
    getConversationDetail: () => storedConv,
  });

  const viewports = [
    { width: 1366, height: 768 },
    { width: 1440, height: 900 },
    { width: 390, height: 844 },
  ];

  for (const vp of viewports) {
    await page.setViewportSize(vp);
    await page.goto("/?project=proj-1&view=assistant");

    const chatSection = page.locator("section[aria-labelledby='assistant-chat-title']");
    await expect(chatSection).toBeVisible();

    const box = await chatSection.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.height).toBeLessThanOrEqual(768);

    const logFeed = page.locator("[role='log']");
    await expect(logFeed).toBeVisible();
    const isScrollable = await logFeed.evaluate((el) => el.scrollHeight > el.clientHeight);
    expect(isScrollable).toBe(true);

    const promptInput = page.getByLabel("Message AI Assistant");
    await expect(promptInput).toBeVisible();
    await promptInput.fill(`Testing in ${vp.width}x${vp.height}`);
    await expect(promptInput).toHaveValue(`Testing in ${vp.width}x${vp.height}`);
  }
});

test("AI Assistant smart scroll and Jump to latest button work via mouse and keyboard without jerking reader", async ({ page }) => {
  const messages: Record<string, unknown>[] = Array.from({ length: 12 }, (_, i) => [
    { id: `u-${i}`, role: "user", text: `User message ${i + 1}` },
    { id: `a-${i}`, role: "assistant", text: `Assistant reply ${i + 1}`, state: "succeeded", grounding: { status: "verified", successful_tool_calls: 1, tools: ["topology"] } },
  ]).flat();

  let turnCount = 0;
  await mockAssistantAPI(page, {
    getConversations: () => [{ id: "conv-smart", project_id: "proj-1", provider_id: "codex", title: "Smart scroll test", created_at: "2026-09-02T10:00:00Z", updated_at: "2026-09-02T10:05:00Z", message_count: messages.length, last_turn_state: "succeeded" }],
    getConversationDetail: () => ({ id: "conv-smart", project_id: "proj-1", provider_id: "codex", title: "Smart scroll test", messages }),
    onTurn: (path, method) => {
      if (path.endsWith("/turns") && method === "POST") {
        turnCount++;
        return { id: `turn-live-${turnCount}`, conversation_id: "conv-smart", provider_id: "codex", project_id: "proj-1", state: "running", started_at: "2026-09-03T00:00:00Z" };
      }
      if (path.includes("/turns/turn-live-")) {
        const respText = `New live response ${turnCount}`;
        if (!messages.some((m) => m.id === `a-live-${turnCount}`)) {
          messages.push(
            { id: `u-live-${turnCount}`, turn_id: `turn-live-${turnCount}`, role: "user", text: turnCount === 1 ? "Question while reading" : "Second question" },
            { id: `a-live-${turnCount}`, turn_id: `turn-live-${turnCount}`, role: "assistant", text: respText, state: "succeeded", grounding: { status: "verified", successful_tool_calls: 1, tools: ["topology"] } }
          );
        }
        return {
          id: `turn-live-${turnCount}`,
          conversation_id: "conv-smart",
          provider_id: "codex",
          project_id: "proj-1",
          state: "succeeded",
          response: respText,
          grounding: { status: "verified", successful_tool_calls: 1, tools: ["topology"] },
          started_at: "2026-09-03T00:00:00Z",
          finished_at: "2026-09-03T00:00:02Z",
        };
      }
    },
  });

  await page.goto("/?project=proj-1&view=assistant");
  const logFeed = page.locator("[role='log']");
  await expect(logFeed).toBeVisible();

  // Scroll up to top
  await logFeed.evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });

  const topPos = await logFeed.evaluate((el) => el.scrollTop);
  expect(topPos).toBe(0);

  // Send a message
  await page.getByLabel("Message AI Assistant").fill("Question while reading");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Question while reading")).toBeVisible();
  // User scrolls up to top
  await logFeed.evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });

  // Verify Jump to latest button appears
  const jumpBtn = page.getByRole("button", { name: /Jump to latest/i });
  await expect(jumpBtn).toBeVisible();

  // Click Jump to latest with mouse
  await jumpBtn.click();
  await expect(jumpBtn).not.toBeVisible();

  await expect.poll(async () => logFeed.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);

  // Now scroll up again and test keyboard activation of Jump to latest
  await logFeed.evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });
  // Trigger turn 2
  await page.getByLabel("Message AI Assistant").fill("Second question");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Second question")).toBeVisible();

  // Scroll up to top
  await logFeed.evaluate((el) => {
    el.scrollTop = 0;
    el.dispatchEvent(new Event("scroll"));
  });

  await expect(jumpBtn).toBeVisible();
  await jumpBtn.focus();
  await page.keyboard.press("Enter");
  await expect(jumpBtn).not.toBeVisible();
  await expect.poll(async () => logFeed.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
});

test("AI Assistant first, second, and third messages in same conversation display grounded responses", async ({ page }) => {
  let turnIdx = 0;
  const conversationMessages: Record<string, unknown>[] = [];

  await mockAssistantAPI(page, {
    getConversations: () => [{ id: "conv-consecutive", project_id: "proj-1", provider_id: "codex", title: "Three turns chat", created_at: "2026-09-02T10:00:00Z", updated_at: "2026-09-02T10:05:00Z", message_count: conversationMessages.length, last_turn_state: "succeeded" }],
    getConversationDetail: () => ({ id: "conv-consecutive", project_id: "proj-1", provider_id: "codex", title: "Three turns chat", messages: conversationMessages }),
    onTurn: (path, method) => {
      if (path.endsWith("/turns") && method === "POST") {
        turnIdx++;
        const turnId = `turn-${turnIdx}`;
        return { id: turnId, conversation_id: "conv-consecutive", provider_id: "codex", project_id: "proj-1", state: "running", started_at: "2026-09-03T00:00:00Z" };
      }
      const turnMatch = path.match(/\/turns\/(turn-\d+)$/);
      if (turnMatch && method === "GET") {
        const id = turnMatch[1];
        const num = id.replace("turn-", "");
        const respText = `Grounded response number ${num} for the conversation.`;
        if (!conversationMessages.some((m) => m.turn_id === id && m.role === "assistant")) {
          conversationMessages.push(
            { id: `msg-${id}-user`, turn_id: id, role: "user", text: `Question ${num}` },
            { id: `msg-${id}-assistant`, turn_id: id, role: "assistant", text: respText, state: "succeeded", grounding: { status: "verified", successful_tool_calls: parseInt(num), tools: ["topology"] } }
          );
        }
        return {
          id,
          conversation_id: "conv-consecutive",
          provider_id: "codex",
          project_id: "proj-1",
          state: "succeeded",
          response: respText,
          grounding: { status: "verified", successful_tool_calls: parseInt(num), tools: ["topology"] },
          started_at: "2026-09-03T00:00:00Z",
          finished_at: "2026-09-03T00:00:02Z",
        };
      }
      return null;
    },
  });

  await page.goto("/?project=proj-1&view=assistant");

  // Turn 1
  await page.getByLabel("Message AI Assistant").fill("Question 1");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Grounded response number 1 for the conversation.")).toBeVisible();
  await expect(page.getByText("Grounded by 1 Opsi MCP calls")).toBeVisible();

  // Turn 2
  await page.getByLabel("Message AI Assistant").fill("Question 2");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Grounded response number 2 for the conversation.")).toBeVisible();
  await expect(page.getByText("Grounded by 2 Opsi MCP calls")).toBeVisible();

  // Turn 3
  await page.getByLabel("Message AI Assistant").fill("Question 3");
  await page.getByRole("button", { name: "Send" }).click();
  await expect(page.getByText("Grounded response number 3 for the conversation.")).toBeVisible();
  await expect(page.getByText("Grounded by 3 Opsi MCP calls")).toBeVisible();

  // Verify all 3 responses remain visible simultaneously in the same conversation
  await expect(page.getByText("Grounded response number 1 for the conversation.")).toBeVisible();
  await expect(page.getByText("Grounded response number 2 for the conversation.")).toBeVisible();
  await expect(page.getByText("Grounded response number 3 for the conversation.")).toBeVisible();
});

type MockOptions = {
  actions?: string[];
  getConversations?: () => unknown[];
  getConversationDetail?: (id: string) => unknown;
  onDeleteConversation?: (id: string) => void;
  onTurn?: (path: string, method: string) => unknown;
};

async function mockAssistantAPI(page: Page, options: MockOptions = {}) {
  await page.route("**/api/local/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body: unknown = {};

    if (path === "/api/local/session") {
      body = { authenticated: true, cloud_connected: "ok", agent_connected: "ok", token_status: "valid", local_session: "local-session", user_id: "user-1", org_id: "org-1", project_id: "proj-1", role: "owner", capabilities: [] };
    } else if (path === "/api/local/projects") {
      body = { projects: [{ id: "proj-1", org_id: "org-1", name: "Identity", slug: "identity", status: "ready" }] };
    } else if (path === "/api/local/ai/providers") {
      body = { mcp_surface: "mcp-04", providers: [{ id: "codex", name: "OpenAI Codex", available: true, authenticated: true, version: "codex-cli 0.151.0", capabilities: ["project_review", "configuration_advice"], data_boundary: "Project data is available to the agent only through the read-only Opsi MCP server." }] };
    } else if (path.endsWith("/assistant/conversations") && request.method() === "GET") {
      body = { conversations: options.getConversations ? options.getConversations() : [] };
    } else if (/\/assistant\/conversations\/([^/]+)$/.test(path)) {
      const match = path.match(/\/assistant\/conversations\/([^/]+)$/);
      const convID = match ? match[1] : "";
      if (request.method() === "DELETE") {
        options.onDeleteConversation?.(convID);
        body = { deleted: true, conversation_id: convID };
      } else {
        body = options.getConversationDetail ? options.getConversationDetail(convID) : { id: convID, messages: [] };
      }
    } else if (options.onTurn && (() => { const p = options.onTurn(path, request.method()); if (p) { body = p; return true; } return false; })()) {
      // Handled by custom onTurn
    } else if (/\/assistant\/turns$/.test(path) && request.method() === "POST") {
      body = { id: "turn-1", conversation_id: "conversation-1", provider_id: "codex", project_id: "proj-1", state: "running", started_at: "2026-08-30T00:00:00Z" };
    } else if (/\/assistant\/turns\/turn-1$/.test(path)) {
      body = { id: "turn-1", conversation_id: "conversation-1", provider_id: "codex", project_id: "proj-1", state: "succeeded", response: "Use one origin and route the API under /api.", grounding: { status: "verified", successful_tool_calls: 3, tools: ["project_review_context", "validate_service_configuration_proposal", "topology"] }, configuration_proposals: [{ application_id: "svc-web", application_name: "web", environment_id: "env-1", rationale: "Keep the browser on one origin.", expected_revision: 5, expected_state_hash: hash("a"), analysis_inputs_hash: hash("b"), draft_json: JSON.stringify({ schema_version: "opsi.service_configuration/v1", environment: [{ name: "API_BASE_PATH", value: "/api" }] }) }], started_at: "2026-08-30T00:00:00Z", finished_at: "2026-08-30T00:00:05Z" };
    } else if (/\/services\/svc-web\/configuration$/.test(path)) {
      body = { schema_version: "opsi.service_configuration/v1", revision: 5, state_hash: hash("a") };
    } else if (path.endsWith("/configuration/validate")) {
      body = { valid: true };
    } else if (path.endsWith("/configuration/diff")) {
      body = { changes: [{ kind: "user_environment", action: "set", name: "API_BASE_PATH", after: "/api" }] };
    } else if (path.endsWith("/services/svc-web/proposal-reviews") && request.method() === "POST") {
      options.actions?.push("create_review");
      body = review("review_required");
    } else if (path.endsWith("/proposal-reviews/review-1/approve")) {
      options.actions?.push("approve");
      body = review("approved");
    } else if (path.endsWith("/proposal-reviews/review-1/apply")) {
      options.actions?.push("apply");
      body = review("applied");
    } else if (path.endsWith("/readiness")) {
      body = { project_id: "proj-1", status: "ready", can_deploy: true };
    } else if (path.endsWith("/topology/facts")) {
      body = { environments: [{ id: "env-1", name: "Production", status: "active" }], runtimes: [], nodes: [] };
    } else if (path.endsWith("/topology")) {
      body = { schema_version: "opsi.topology_plan/v1", id: "topology-1", project_id: "proj-1", revision: 1, state_hash: hash("c"), plan_hash: hash("d"), assignments: [] };
    } else if (path.endsWith("/nodes")) {
      body = { nodes: [] };
    } else if (path.endsWith("/services")) {
      body = { services: [{ id: "svc-web", name: "web", type: "application", status: "ready", source_type: "git" }] };
    } else if (path.endsWith("/deployments")) {
      body = { deployments: [] };
    } else if (path.endsWith("/bootstrap-sessions")) {
      body = { sessions: [] };
    } else if (path.endsWith("/build-records")) {
      body = { records: [] };
    } else if (path.endsWith("/audit")) {
      body = { events: [] };
    } else if (path.endsWith("/incidents")) {
      body = { source: "agent", payload_policy: "redacted", incidents: [] };
    } else if (path.endsWith("/support")) {
      body = { generated_at: "2026-08-30T00:00:00Z", counts: {}, signals: [] };
    }

    await json(route, body);
  });
}

function review(status: string) {
  return { id: "review-1", project_id: "proj-1", environment_id: "env-1", application_id: "svc-web", kind: "service_configuration", status, proposal_hash: hash("e"), analysis_inputs_hash: hash("b"), normalized_payload: {}, reviewed_payload_hash: hash("f"), expected_configuration_revision: 5, expected_configuration_state_hash: hash("a"), created_at: "2026-08-30T00:00:00Z", expires_at: "2026-08-31T00:00:00Z" };
}

async function json(route: Route, body: unknown) {
  await route.fulfill({ body: JSON.stringify(body), contentType: "application/json", status: 200 });
}
