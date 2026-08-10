import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Opsi Factual Systems foundation exposes required visual state tokens", async () => {
  const css = await readFile(new URL("./app/design-system.css", import.meta.url), "utf8");
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
  assert.match(css, /min-height: 40px/);
});
