import assert from "node:assert/strict";
import test from "node:test";
import {
  applicationConnectionEdgeID,
  applicationTopologyNodeID,
  dependencyTopologyEdgeID,
  deriveTopologyGraphIdentity,
  topologyHandleIDs,
} from "./topology-graph-identity.ts";

const revisions = [0, 1, 2, 3];

const baseAuthority = {
  applications: [
    { id: "api", name: "api" },
    { id: "worker", name: "worker" },
    { id: "reports", name: "reports" },
  ],
  applicationBindings: [{ sourceID: "api", targetID: "worker" }],
};

test("topology graph derivation is deterministic across authority revisions", () => {
  const revisionsOutput = revisions.map((revision) => deriveTopologyGraphIdentity({ ...baseAuthority, revision }));
  assert.deepEqual(revisionsOutput, Array.from({ length: 4 }, () => revisionsOutput[0]));
  assert.deepEqual(revisionsOutput[0].nodes, [
    { id: "service:api", sourceHandle: "source", targetHandle: "target" },
    { id: "service:reports", sourceHandle: "source", targetHandle: "target" },
    { id: "service:worker", sourceHandle: "source", targetHandle: "target" },
  ]);
  assert.deepEqual(revisionsOutput[0].edges, [{
    id: "connection:api:worker",
    source: "service:api",
    target: "service:worker",
    sourceHandle: "source",
    targetHandle: "target",
  }]);
  assert.deepEqual(topologyHandleIDs, { source: "source", target: "target" });
});

test("topology graph derivation tracks added and removed edges without changing unrelated identities", () => {
  const initial = deriveTopologyGraphIdentity(baseAuthority);
  const added = deriveTopologyGraphIdentity({
    ...baseAuthority,
    applicationBindings: [...baseAuthority.applicationBindings, { sourceID: "api", targetID: "reports" }],
  });
  const removed = deriveTopologyGraphIdentity({ ...baseAuthority, applicationBindings: [] });
  assert.deepEqual(initial.nodes, added.nodes);
  assert.deepEqual(initial.nodes, removed.nodes);
  assert.deepEqual(initial.edges.map((edge) => edge.id), ["connection:api:worker"]);
  assert.deepEqual(added.edges.map((edge) => edge.id), ["connection:api:reports", "connection:api:worker"]);
  assert.deepEqual(removed.edges, []);
});

test("topology graph derivation covers applications, managed resources, and dependency edges", () => {
  const graph = deriveTopologyGraphIdentity({
    ...baseAuthority,
    managedResources: [{ id: "postgres" }],
    dependencies: [
      { sourceID: "api", targetID: "reports", targetKind: "application", logicalName: "reports-api" },
      { sourceID: "api", targetID: "postgres", targetKind: "managed_service", logicalName: "database" },
    ],
  });
  assert.deepEqual(graph.edges.map((edge) => edge.id), [
    "connection:api:worker",
    "dep:api:database",
    "dep:api:reports-api",
  ]);
  assert.ok(graph.nodes.some((node) => node.id === "resource:postgres"));
  assert.equal(applicationTopologyNodeID("api"), "service:api");
  assert.equal(applicationConnectionEdgeID("api", "worker"), "connection:api:worker");
  assert.equal(dependencyTopologyEdgeID("api", "database"), "dep:api:database");
});
