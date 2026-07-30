import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("observability uses one shared Local source model and truthful chart/log semantics", async () => {
  const source = await Promise.all([
    readFile(new URL("./data.ts", import.meta.url), "utf8"),
    readFile(new URL("./metrics-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./logs-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./incidents-tab.tsx", import.meta.url), "utf8"),
  ]).then((parts) => parts.join("\n"));
  for (const value of ["partial", "last factual data is preserved", "timestamps not reported", "safeLogMessage", "incidentEvidence", "Continue in CLI"]) assert.match(source, new RegExp(value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.doesNotMatch(source, /dangerouslySetInnerHTML|localStorage|sessionStorage|fake time-series|live tail/i);
});
