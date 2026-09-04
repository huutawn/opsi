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

test("missing counters, service identity, CLI handoff, and evidence fail closed", async () => {
  const [data, health, metrics, incidents] = await Promise.all([
    readFile(new URL("./data.ts", import.meta.url), "utf8"),
    readFile(new URL("./health-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./metrics-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./incidents-tab.tsx", import.meta.url), "utf8"),
  ]);
  assert.doesNotMatch(metrics, /restart_count \?\? 0|recent_error_count \?\? 0/);
  assert.match(metrics, /metricStatus/);
  assert.doesNotMatch(health, /service\.id \|\| item\.service_id === service\.name/);
  assert.match(health, /Unresolved identity/);
  for (const command of ["opsi action preflight", "opsi action approve", "opsi action execute"]) assert.match(incidents, new RegExp(command));
  assert.doesNotMatch(incidents, /opsi incident (?:preflight|approve|execute)/);
  assert.match(data, /\^\[a-f0-9\]\{64\}\$/);
  for (const bound of ["MAX_EVIDENCE_COVERAGE", "MAX_EVIDENCE_TIMELINE", "MAX_EVIDENCE_PODS"]) assert.match(data, new RegExp(bound));
});

test("observability supports 1h/6h/24h time-window selector, node-scoped incidents, and partial coverage diagnostics", async () => {
  const [data, overview, metrics, logs, incidents, health, shared] = await Promise.all([
    readFile(new URL("./data.ts", import.meta.url), "utf8"),
    readFile(new URL("./overview-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./metrics-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./logs-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./incidents-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./health-tab.tsx", import.meta.url), "utf8"),
    readFile(new URL("./shared.tsx", import.meta.url), "utf8"),
  ]);

  // TimeWindowSelector
  assert.match(shared, /TimeWindowSelector/);
  assert.match(shared, /"1h" \| "6h" \| "24h"/);
  assert.match(overview, /TimeWindowSelector/);
  assert.doesNotMatch(overview, /Window: Last 1h/);
  assert.match(metrics, /TimeWindowSelector/);
  assert.match(logs, /TimeWindowSelector/);

  // Window parameter handling in data.ts
  assert.match(data, /console\.route\.window \|\| "1h"/);
  assert.match(data, /windowParam === "24h" \? 86400 : windowParam === "6h" \? 21600 : 3600/);
  assert.match(data, /client\.telemetrySummary\(projectID, sinceUnix, windowParam\)/);

  // Partial coverage diagnostics
  assert.match(shared, /PartialCoverageBanner/);
  assert.match(overview, /PartialCoverageBanner/);
  assert.match(metrics, /PartialCoverageBanner/);
  assert.match(incidents, /PartialCoverageBanner/);
  assert.match(health, /PartialCoverageBanner/);

  // Node-scoped incident navigation
  assert.match(incidents, /node: incident\.node_id/);
  assert.match(health, /node: incident\.node_id/);
  assert.match(data, /client\.incident\(projectID, incidentID, nodeID\)/);
  assert.match(data, /client\.incidentEvidence\(projectID, incidentID, nodeID\)/);
});
