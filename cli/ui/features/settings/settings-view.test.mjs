import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("settings view renders version, configuration, and truthful backend gaps", async () => {
  const source = await readFile(new URL("./settings-view.tsx", import.meta.url), "utf8");
  for (const marker of ["Version and configuration", "backend_gaps", "PAT rotation", "PAT revoke", "gap.status"]) {
    assert.match(source, new RegExp(marker));
  }
  assert.doesNotMatch(source, /placeholder|SupportView|localStorage|sessionStorage/i);
});
