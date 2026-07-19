import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./topology-view.tsx", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);

test("manual placement wizard is local-only, deterministic, and has no deploy action", async () => {
  const source = `${await readFile(component, "utf8")}\n${await readFile(client, "utf8")}`;
  assert.match(source, /Manual placement wizard/);
  assert.match(source, /topology\/validate/);
  assert.match(source, /deployment-policies\/apply/);
  assert.match(source, /allow unknown capacity/);
  assert.match(source, /TOPOLOGY|TopologyPlan/);
  assert.doesNotMatch(source, /https?:\/\/(?!127\.0\.0\.1|localhost)|Authorization|localStorage|sessionStorage|agent_token/);
  assert.doesNotMatch(await readFile(component, "utf8"), /client\.deploy\(|>\s*Deploy\s*</i);
});
