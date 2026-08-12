import assert from "node:assert/strict";
import test from "node:test";

import { buildFailure, terminalBuild } from "./build.ts";

test("BuildJob lifecycle keeps every canonical terminal state", () => {
  for (const status of ["pending", "ready", "running"]) assert.equal(terminalBuild({ status }), false);
  for (const status of ["succeeded", "failed", "cancelled"]) assert.equal(terminalBuild({ status }), true);
});

test("typed build failures stay actionable", () => {
  for (const code of ["GITHUB_REF_NOT_FOUND", "BUILD_SOURCE_INVALID", "DOCKERFILE_NOT_FOUND", "BUILDPACK_DETECTION_FAILED", "EXECUTOR_INFRASTRUCTURE_FAILED", "REGISTRY_PUSH_FAILED"]) {
    const failure = buildFailure(code);
    assert.notEqual(failure.title, "Build failed.");
    assert.match(failure.action, /retry|choose|check|fix|restore|use/i);
  }
  assert.equal(buildFailure("BUILDPACK_MONOREPO_UNSUPPORTED").action, "This application depends on files outside its application root. Use a Dockerfile build or choose a self-contained application directory.");
});
