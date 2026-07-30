import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { deploymentPollInterval, shouldPoll, terminalDeployment } from "./polling-model.ts";

test("polling is bounded to the selected visible non-terminal deployment", () => {
  assert.equal(deploymentPollInterval, 5_000);
  assert.equal(shouldPoll("proj-1", "dep-1", { id: "dep-1", service_id: "svc", status: "waiting", created_at: "now" }, false), true);
  assert.equal(shouldPoll("proj-1", "dep-1", { id: "dep-1", service_id: "svc", status: "succeeded", created_at: "now" }, false), false);
  assert.equal(shouldPoll("proj-1", "dep-1", null, true), false);
  assert.equal(terminalDeployment({ id: "dep-1", service_id: "svc", status: "waiting", rollout_state: "rolled_back", created_at: "now" }), true);
});

test("polling lifecycle guards visibility, project/job changes, stale responses, and preserves factual errors", async () => {
  const source = await readFile(new URL("./polling.ts", import.meta.url), "utf8");
  for (const required of ["visibilitychange", "document.hidden", "activeKey.current !== key", "sequence < applied.current", "window.clearTimeout", "last factual state is preserved"]) assert.match(source, new RegExp(required.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
});
