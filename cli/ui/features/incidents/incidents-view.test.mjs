import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./incidents-view.tsx", import.meta.url);
const state = new URL("../../hooks/use-console-state.ts", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);

test("incident UI derives authority from the local session", async () => {
  const componentSource = await readFile(component, "utf8");
  const stateSource = await readFile(state, "utf8");
  const clientSource = await readFile(client, "utf8");
  const incidentActions = stateSource.slice(stateSource.indexOf("async function incidentList"), stateSource.indexOf("  return {"));
  const clientMethods = clientSource.slice(clientSource.indexOf("incidents("), clientSource.indexOf("private async call"));

  assert.doesNotMatch(componentSource, /name="(?:user_id|role|pat)"/);
  assert.doesNotMatch(incidentActions, /form\.get\("(?:user_id|role|pat)"\)/);
  assert.doesNotMatch(clientMethods, /user_id|\brole\b|\bpat\b/i);
});
