import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("settings is a factual local workspace destination with reviewed PAT lifecycle", async () => {
  const source = await readFile(new URL("./settings-view.tsx", import.meta.url), "utf8");
  for (const marker of ["General", "Authentication", "Integrations", "System", "Settings sections", "Capability limits", "PAT rotation", "revoke and sign out", "confirmation: \"REVOKE\"", "hideSensitive", "backend_gaps"]) {
    assert.match(source, new RegExp(marker));
  }
  assert.doesNotMatch(source, /disabled.*gap\.status|SupportView|localStorage|sessionStorage|raw PAT|certificate material/i);
});
