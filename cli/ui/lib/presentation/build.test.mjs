import assert from "node:assert/strict";
import test from "node:test";

import { buildFailure, buildFailureCategory, terminalBuild } from "./build.ts";

test("BuildJob lifecycle keeps every canonical terminal state", () => {
  for (const status of ["pending", "ready", "running"]) assert.equal(terminalBuild({ status }), false);
  for (const status of ["succeeded", "failed", "cancelled"]) assert.equal(terminalBuild({ status }), true);
});

test("build failures retain their user-facing authority category", () => {
  assert.equal(buildFailureCategory("GITHUB_REF_NOT_FOUND"), "Source");
  assert.equal(buildFailureCategory("DOCKERFILE_NOT_FOUND"), "Dockerfile");
  assert.equal(buildFailureCategory("BUILDPACK_BUILD_FAILED"), "Buildpacks");
  assert.equal(buildFailureCategory("EXECUTOR_INFRASTRUCTURE_FAILED"), "Executor");
  assert.equal(buildFailureCategory("REGISTRY_PUSH_FAILED"), "Registry");
});

test("typed build failures stay actionable", () => {
  for (const code of ["GITHUB_REF_NOT_FOUND", "BUILD_SOURCE_INVALID", "DOCKERFILE_NOT_FOUND", "BUILDPACK_DETECTION_FAILED", "EXECUTOR_INFRASTRUCTURE_FAILED", "REGISTRY_PUSH_FAILED"]) {
    const failure = buildFailure(code);
    assert.notEqual(failure.title, "Build failed.");
    assert.match(failure.action, /retry|choose|check|fix|restore|use/i);
  }
  assert.equal(buildFailure("BUILDPACK_MONOREPO_UNSUPPORTED").action, "This application depends on files outside its application root. Use a Dockerfile build or choose a self-contained application directory.");
});
