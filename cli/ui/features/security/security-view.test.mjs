import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./security-view.tsx", import.meta.url);
const state = new URL("../../hooks/use-console-state.ts", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);
const router = new URL("../console/router-map.tsx", import.meta.url);

test("Security is one canonical route with contextual operations and bounded audit detail", async () => {
  const [source, routerSource] = await Promise.all([readFile(component, "utf8"), readFile(router, "utf8")]);
  for (const marker of ["SecurityView", "Choose a service", "SecondFactorFields", "ProtectedResult", "Hide now", "Automatically hides in", "Loaded history", "Machine actor", "Human actor", "boundedMetadata"]) assert.match(source, new RegExp(marker));
  assert.match(routerSource, /security: \{ secrets: SecurityView, audit: SecurityView \}/);
  assert.match(source, /setIsOpen\(true\)/);
  assert.match(source, /isOpen && result/);
  assert.match(source, /isOpen && totp/);
  assert.doesNotMatch(routerSource, /SecretsView|AuditView/);
  assert.doesNotMatch(source, /defaultValue=\{services\[0\]|JSON\.stringify|localStorage|sessionStorage|dangerouslySetInnerHTML/);
});

test("secret requests carry no caller identity, PAT, silent namespace, or protected value in review", async () => {
  const componentSource = await readFile(component, "utf8");
  const stateSource = await readFile(state, "utf8");
  const clientSource = await readFile(client, "utf8");
  const secretBody = stateSource.slice(stateSource.indexOf("function secretCreate"), stateSource.indexOf("function setupTOTP"));
  const clientMethods = clientSource.slice(clientSource.indexOf("createSecret("), clientSource.indexOf("incidents("));
  assert.doesNotMatch(componentSource, /name="(?:user_id|role|pat)"/);
  assert.doesNotMatch(secretBody, /user_id|form\.get\("role"\)|\bpat\b|namespace.*default/i);
  assert.doesNotMatch(clientMethods, /user_id|"role"|"pat"/i);
  assert.doesNotMatch(secretBody, /password:|secret:|totp\.secret|totp\.uri/);
});
