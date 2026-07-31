import assert from "node:assert/strict";
import test from "node:test";

import { groupedTabs, normalizeRoute, parseRoute, projectDestinations, routeHref } from "./navigation.ts";

test("workspace and project navigation keep one authoritative route model", () => {
  assert.equal(projectDestinations.length, 6);
  assert.deepEqual(projectDestinations.map((item) => item.label), ["Overview", "Services", "Delivery", "Infrastructure", "Observability", "Security"]);
  assert.deepEqual(normalizeRoute({ view: "overview" }), { projectID: "", view: "projects", tab: "" });
  assert.deepEqual(parseRoute("?project=proj-1&view=observability&tab=logs"), { projectID: "proj-1", view: "observability", tab: "logs" });
  assert.equal(routeHref({ projectID: "proj-1", view: "delivery", tab: "builds" }), "/?project=proj-1&view=delivery&tab=builds");
  assert.equal(groupedTabs.delivery[0].id, "pipeline");
  assert.equal(groupedTabs.settings.length, 4);
  assert.deepEqual(normalizeRoute({ view: "settings" }), { projectID: "", view: "settings", tab: "general" });
  assert.deepEqual(
    parseRoute("?project=proj-1&view=delivery&tab=deployments&service=svc-api&build=build-7&deployment=dep-9&status=failed&kind=preview"),
    { projectID: "proj-1", view: "delivery", tab: "deployments", service: "svc-api", build: "build-7", deployment: "dep-9", status: "failed", kind: "preview" },
  );
  assert.equal(
    routeHref({ projectID: "proj-1", view: "delivery", tab: "builds", service: "svc-api", build: "build-7", repository: "101", sha: "abc", cursor: "next" }),
    "/?project=proj-1&view=delivery&tab=builds&service=svc-api&build=build-7&repository=101&sha=abc&cursor=next",
  );
});

test("capabilities remain reachable through one grouped route model", () => {
  assert.equal(Object.values(groupedTabs).flat().length, 19);
  assert.deepEqual(groupedTabs.infrastructure.map((item) => item.id), ["topology", "runtimes", "nodes", "bootstrap"]);
  assert.deepEqual(groupedTabs.observability.map((item) => item.id), ["health", "metrics", "logs", "incidents"]);
  assert.deepEqual(groupedTabs.security.map((item) => item.id), ["secrets", "audit"]);
});
