import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { LocalClient } from "../../lib/api/local-client.ts";

function authErrorMessage(code) {
  return ({
    GITHUB_ACCOUNT_UNLINKED: "This GitHub account is not linked to an Opsi user.",
    OPSI_MEMBERSHIP_REQUIRED: "No Opsi projects are available for this account.",
    PROJECT_SELECTION_REQUIRED: "This account needs an explicit project selection.",
    GITHUB_AUTH_DENIED: "GitHub authorization was cancelled.",
    AUTH_SESSION_EXPIRED: "The sign-in request expired.",
    AUTH_UNAVAILABLE: "Opsi sign-in is temporarily unavailable.",
    GITHUB_AUTH_FAILED: "GitHub sign-in failed.",
  })[code] ?? "";
}

test("auth error mapping maps OPSI_MEMBERSHIP_REQUIRED to factual empty state message", () => {
  assert.equal(authErrorMessage("OPSI_MEMBERSHIP_REQUIRED"), "No Opsi projects are available for this account.");
});

test("auth error mapping maps PROJECT_SELECTION_REQUIRED to explicit selection message", () => {
  assert.equal(authErrorMessage("PROJECT_SELECTION_REQUIRED"), "This account needs an explicit project selection.");
});

test("auth error mapping covers all required error codes", () => {
  assert.equal(authErrorMessage("GITHUB_ACCOUNT_UNLINKED"), "This GitHub account is not linked to an Opsi user.");
  assert.equal(authErrorMessage("GITHUB_AUTH_DENIED"), "GitHub authorization was cancelled.");
  assert.equal(authErrorMessage("AUTH_SESSION_EXPIRED"), "The sign-in request expired.");
  assert.equal(authErrorMessage("AUTH_UNAVAILABLE"), "Opsi sign-in is temporarily unavailable.");
  assert.equal(authErrorMessage("GITHUB_AUTH_FAILED"), "GitHub sign-in failed.");
  assert.equal(authErrorMessage("UNKNOWN_CODE"), "");
});

test("LocalClient getSelectableProjects and selectProject generate correct requests", async () => {
  const calls = [];
  const client = new LocalClient();
  // Mock internal call method
  client.call = async (path, options) => {
    calls.push({ path, options });
    if (path.startsWith("/api/local/session/selection")) {
      return {
        selection_id: "sel-123",
        projects: [
          { id: "proj-1", name: "Project Alpha", role: "owner" },
          { id: "proj-2", name: "Project Beta", role: "developer" },
        ],
      };
    }
    if (path === "/api/local/session/select-project") {
      return {
        authenticated: true,
        session: { project_id: "proj-1", org_id: "org-1" },
      };
    }
    return {};
  };

  const selRes = await client.getSelectableProjects("sel-123");
  assert.equal(selRes.selection_id, "sel-123");
  assert.equal(selRes.projects.length, 2);
  assert.equal(calls[0].path, "/api/local/session/selection?selection_id=sel-123");

  const selectRes = await client.selectProject("sel-123", "proj-1");
  assert.equal(selectRes.authenticated, true);
  assert.equal(calls[1].path, "/api/local/session/select-project");
  assert.equal(calls[1].options.method, "POST");
  assert.equal(calls[1].options.body, JSON.stringify({ selection_id: "sel-123", project_id: "proj-1" }));
});

test("project selection resumes through console state without Next document navigation", async () => {
  const [shell, consoleState] = await Promise.all([
    readFile(new URL("../../components/layout/app-shell.tsx", import.meta.url), "utf8"),
    readFile(new URL("../../hooks/use-console-state.ts", import.meta.url), "utf8"),
  ]);
  assert.doesNotMatch(shell, /useRouter|router\.push/);
  assert.match(shell, /window\.history\.replaceState\([^\n]+view=deploy/);
  assert.match(shell, /await onAuthenticated\(projectID\)/);
  assert.match(consoleState, /setProjectID: async/);
  assert.match(consoleState, /return loaded/);
});
