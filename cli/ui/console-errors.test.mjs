import assert from "node:assert/strict";
import test from "node:test";

import { matchesExpectedHTTPFailure, matchesExpectedRequestFailure } from "./e2e/console-errors.ts";

const expected = { path: "/api/local/projects/proj-1/audit", status: 503, method: "GET" };

test("expected HTTP failures require the exact path, status, and method", () => {
  assert.equal(matchesExpectedHTTPFailure(expected, { ...expected, url: "http://127.0.0.1:19881/api/local/projects/proj-1/audit" }), true);
  assert.equal(matchesExpectedHTTPFailure(expected, { ...expected, status: 500, url: "http://127.0.0.1:19881/api/local/projects/proj-1/audit" }), false);
  assert.equal(matchesExpectedHTTPFailure(expected, { ...expected, path: "/api/local/projects/proj-2/audit", url: "http://127.0.0.1:19881/api/local/projects/proj-2/audit" }), false);
  assert.equal(matchesExpectedHTTPFailure(expected, { ...expected, method: "POST", url: "http://127.0.0.1:19881/api/local/projects/proj-1/audit" }), false);
});

test("expected request failures also require the exact browser error", () => {
  const requestFailure = { ...expected, errorText: "net::ERR_ABORTED" };
  assert.equal(matchesExpectedRequestFailure(requestFailure, { ...requestFailure, url: "http://127.0.0.1:19881/api/local/projects/proj-1/audit" }), true);
  assert.equal(matchesExpectedRequestFailure(requestFailure, { ...requestFailure, errorText: "net::ERR_FAILED", url: "http://127.0.0.1:19881/api/local/projects/proj-1/audit" }), false);
});
