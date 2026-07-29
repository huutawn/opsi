import assert from "node:assert/strict";
import test from "node:test";

import { groupedTabs, normalizeRoute, parseRoute, projectDestinations, routeForLegacy, routeHref } from "./navigation.ts";

test("workspace and project navigation keep one authoritative route model", () => {
  assert.equal(projectDestinations.length, 6);
  assert.deepEqual(projectDestinations.map((item) => item.label), ["Overview", "Services", "Delivery", "Infrastructure", "Observability", "Security"]);
  assert.deepEqual(normalizeRoute({ view: "overview" }), { projectID: "", view: "projects", tab: "" });
  assert.deepEqual(parseRoute("?project=proj-1&view=observability&tab=logs"), { projectID: "proj-1", view: "observability", tab: "logs" });
  assert.equal(routeHref({ projectID: "proj-1", view: "delivery", tab: "builds" }), "/?project=proj-1&view=delivery&tab=builds");
});

test("legacy capabilities remain reachable only through grouped tabs", () => {
  const expected = {
    GitHub: ["delivery", "source"],
    "Build Records": ["delivery", "builds"],
    Deployments: ["delivery", "deployments"],
    Runtime: ["infrastructure", "runtime"],
    "Servers / Nodes": ["infrastructure", "nodes"],
    Topology: ["infrastructure", "topology"],
    Metrics: ["observability", "metrics"],
    Logs: ["observability", "logs"],
    Incidents: ["observability", "incidents"],
    Support: ["observability", "support"],
    Secrets: ["security", "secrets"],
    Audit: ["security", "audit"],
  };
  for (const [label, [view, tab]] of Object.entries(expected)) {
    assert.deepEqual(routeForLegacy(label, "proj-1"), { projectID: "proj-1", view, tab });
  }
  assert.equal(Object.values(groupedTabs).flat().length, 15);
});
