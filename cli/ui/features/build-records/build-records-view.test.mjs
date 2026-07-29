import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./build-records-view.tsx", import.meta.url);
const client = new URL("../../lib/api/local-client.ts", import.meta.url);

test("BuildRecords UI is local-only, read-only, and credential-free", async () => {
  const source = `${await readFile(component, "utf8")}\n${await readFile(client, "utf8")}`;
  assert.match(source, /\/api\/local\/projects\/\$\{projectID\}\/build-records/);
  assert.match(source, /No deploy action is available/);
  assert.doesNotMatch(source, /Authorization|OIDC token|localStorage|sessionStorage|NEXT_PUBLIC_CLOUD|api\.github\.com/);
  assert.doesNotMatch(source, /submitBuildRecord|deployBuildRecord/);
});
