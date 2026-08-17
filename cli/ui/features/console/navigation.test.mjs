import assert from "node:assert/strict";
import test from "node:test";

import { groupedTabs, normalizeRoute, parseRoute, projectDestinations, routeHref } from "./navigation.ts";

test("workspace and project navigation keep one authoritative route model", () => {
  assert.equal(projectDestinations.length, 7);
  assert.deepEqual(
    projectDestinations.map((item) => item.label),
    ["Topology", "Overview", "Services", "Infrastructure", "Delivery", "Observability", "Security"],
  );
  assert.deepEqual(normalizeRoute({ view: "overview" }), { projectID: "", view: "projects", tab: "" });
  assert.deepEqual(normalizeRoute({ projectID: "proj-1" }), { projectID: "proj-1", view: "topology", tab: "" });
  assert.equal(routeHref({ projectID: "proj-1" }), "/?project=proj-1&view=topology");
  assert.deepEqual(parseRoute("?project=proj-1&view=overview"), { projectID: "proj-1", view: "overview", tab: "" });
  assert.deepEqual(parseRoute("?project=proj-1&view=observability&tab=applications"), { projectID: "proj-1", view: "observability", tab: "applications" });
  assert.deepEqual(normalizeRoute({ projectID: "proj-1", view: "observability", tab: "logs" }), { projectID: "proj-1", view: "observability", tab: "logs" });
  assert.deepEqual(parseRoute("?project=proj-1&view=infrastructure&tab=resources"), { projectID: "proj-1", view: "infrastructure", tab: "resources" });
  assert.equal(routeHref({ projectID: "proj-1", view: "delivery", tab: "builds" }), "/?project=proj-1&view=delivery&tab=builds");
  assert.equal(routeHref({ projectID: "proj-1", view: "infrastructure", tab: "resources" }), "/?project=proj-1&view=infrastructure&tab=resources");
  assert.equal(groupedTabs.delivery[0].id, "pipeline");
  assert.equal(groupedTabs.infrastructure[0].id, "servers");
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
  assert.equal(routeHref({ projectID: "proj-1", view: "services", service: "svc-api" }), "/?project=proj-1&view=services&service=svc-api");
});

test("capabilities remain reachable through one grouped route model", () => {
  assert.equal(Object.values(groupedTabs).flat().length, 18);
  assert.deepEqual(groupedTabs.infrastructure.map((item) => item.id), ["servers", "resources", "storage"]);
  assert.deepEqual(groupedTabs.observability.map((item) => item.id), ["overview", "applications", "servers", "resources"]);
  assert.deepEqual(groupedTabs.security.map((item) => item.id), ["secrets", "audit"]);
});
