import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Opsi Factual Systems foundation exposes required visual state tokens", async () => {
  const css = await readFile(new URL("./app/design-system.css", import.meta.url), "utf8");
  const shell = await readFile(new URL("./app/globals.css", import.meta.url), "utf8");
  for (const token of [
    "--opsi-font-ui",
    "--opsi-font-mono",
    "--opsi-draft-bg",
    "--opsi-live-bg",
    "--opsi-ready",
    "--opsi-failed",
    "--opsi-progress",
    "--opsi-warning",
    "--opsi-unknown",
  ]) assert.match(css, new RegExp(`${token}:`));
  assert.match(css, /\.statusIcon/);
  assert.match(css, /backdrop-filter: blur/);
  assert.match(css, /min-height: 0/);
  assert.match(shell, /grid-template-columns: 288px minmax\(0, 1fr\)/);
  assert.match(shell, /\.shellContent/);
  assert.match(shell, /\.systemFact/);
  assert.doesNotMatch(shell, /fonts\.googleapis|Material Symbols/);
  assert.doesNotMatch(shell, /sidebarCollapsed|sidebarCollapse|sidebar\.collapsed/);
});
