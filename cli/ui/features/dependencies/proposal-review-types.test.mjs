import test from "node:test";
import assert from "node:assert/strict";
import { asApplicationDependency, isDependencyProposal, isSourcePatchProposal } from "./proposal-review-types.ts";

const dependency = {
  project_id: "project-1",
  environment_id: "environment-1",
  application_id: "application-1",
  provenance: { source_commit: "a".repeat(40), application_root: "api", analysis_inputs_hash: "b".repeat(64) },
  candidate: {
    logical_name: "database",
    dependency_kind: "managed_service",
    target_id: "resource-1",
    protocol: "postgres",
    phase: "runtime",
    required: true,
    mappings: [{ env_name: "DATABASE_URL", symbolic_source: "connection.url" }],
  },
};

test("dependency proposal envelope is transport data and maps only its explicit candidate", () => {
  assert.equal(isDependencyProposal(dependency), true);
  const candidate = asApplicationDependency(dependency);
  assert.deepEqual(candidate, {
    logical_name: "database",
    target_kind: "managed_service",
    target_identity: "resource-1",
    protocol: "postgres",
    injection_phase: "runtime",
    required: true,
    access_context: undefined,
    strategy: undefined,
    path: undefined,
    injection_mappings: [{ env_name: "DATABASE_URL", symbolic_source: "connection.url" }],
    verification_contract: undefined,
  });
});

test("source patch envelopes remain distinct from dependency proposals", () => {
  const patch = {
    project_id: "project-1",
    environment_id: "environment-1",
    application_id: "application-1",
    provenance: { build_record_id: "build-1", source_commit: "a".repeat(40), application_root: "api", analysis_inputs_hash: "b".repeat(64) },
    rationale: { observed_source: "DATABASE_URL is read", opsi_facts: "dependency missing", inference: "add configuration" },
    files: [{ path: "main.go", base_blob_sha: "c".repeat(40), unified_diff: "@@ -1 +1 @@\n-old\n+new" }],
  };
  assert.equal(isSourcePatchProposal(patch), true);
  assert.equal(isDependencyProposal(patch), false);
});

test("incomplete envelopes fail closed before review", () => {
  assert.equal(isDependencyProposal({ project_id: "project-1" }), false);
  assert.equal(isSourcePatchProposal({ project_id: "project-1", files: [] }), false);
});
