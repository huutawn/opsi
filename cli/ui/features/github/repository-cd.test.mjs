import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const componentPath = new URL("./repository-cd.tsx", import.meta.url);
const clientPath = new URL("../../lib/api/local-client.ts", import.meta.url);

test("repository CD UI exposes real preview, apply, error, retry, and hash states", async () => {
  const source = await readFile(componentPath, "utf8");
  for (const marker of ["previewing", "applying", "success", "error", "Retry apply", "config_hash", "plan_hash", "full_build"]) {
    assert.match(source, new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  assert.match(source, /window\.confirm/);
  assert.doesNotMatch(source, /fake success/i);
});

test("repository CD browser code stays local and stores no credentials", async () => {
  const source = `${await readFile(componentPath, "utf8")}\n${await readFile(clientPath, "utf8")}`;
  assert.match(source, /\/api\/local\/repository\//);
  assert.doesNotMatch(source, /api\.github\.com|NEXT_PUBLIC_CLOUD|localStorage|sessionStorage/);
});
