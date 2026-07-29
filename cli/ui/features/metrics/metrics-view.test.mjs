import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("metrics view uses only the Local API and reports Agent outage", async () => {
  const source = await readFile(new URL("./metrics-view.tsx", import.meta.url), "utf8");
  assert.match(source, /telemetrySummary/);
  assert.match(source, /AGENT_UNAVAILABLE/);
  assert.doesNotMatch(source, /SupportView|https?:\/\/|localStorage|sessionStorage|dangerouslySetInnerHTML/);
});
