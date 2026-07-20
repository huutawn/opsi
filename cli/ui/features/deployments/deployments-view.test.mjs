import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./deployments-view.tsx", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);

test("manual deployment UI uses the loopback immutable job API and typed review flow", async () => {
  const componentSource = await readFile(component, "utf8");
  const source = `${componentSource}\n${await readFile(client, "utf8")}`;

  for (const required of ["deploymentPreview", "deploymentApply", "deploymentEvents", "deploymentCancel", "deploymentRetry", "application_container_name: \"app\"", "setPreview(null)", "mode === \"immutable_image\""]) {
    assert.match(source, new RegExp(required.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.match(componentSource, /Exact digest/);
  assert.match(componentSource, /Resolved target/);
  assert.match(componentSource, /Topology/);
  assert.match(componentSource, /DeploymentPolicy/);
  assert.doesNotMatch(componentSource, /textarea|raw yaml|raw manifest|localStorage|sessionStorage|Authorization/i);
  assert.doesNotMatch(source, /https?:\/\/(?!127\.0\.0\.1|localhost)/);
});
