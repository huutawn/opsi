import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./secrets-view.tsx", import.meta.url);
const state = new URL("../../hooks/use-console-state.ts", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);

test("secret UI sends no caller-controlled identity or PAT", async () => {
  const componentSource = await readFile(component, "utf8");
  const stateSource = await readFile(state, "utf8");
  const clientSource = await readFile(client, "utf8");
  const secretBody = stateSource.slice(stateSource.indexOf("function secretBody"), stateSource.indexOf("async function loadProject"));
  const clientMethods = clientSource.slice(clientSource.indexOf("createSecret("), clientSource.indexOf("incidents("));

  assert.doesNotMatch(componentSource, /name="(?:user_id|role|pat)"/);
  assert.doesNotMatch(secretBody, /user_id|form\.get\("role"\)|\bpat\b/i);
  assert.doesNotMatch(clientMethods, /user_id|"role"|"pat"/i);
});
