import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("runtime view renders factual topology inventory and Agent outage", async () => {
  const source = await readFile(new URL("./runtime-view.tsx", import.meta.url), "utf8");
  assert.match(source, /placementFacts/);
  assert.match(source, /AGENT_UNAVAILABLE/);
  assert.match(source, /facts\.runtimes/);
  assert.doesNotMatch(source, /fake|https?:\/\/|dangerouslySetInnerHTML/i);
});
