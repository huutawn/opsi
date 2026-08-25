import assert from "node:assert/strict";
import test from "node:test";
import {
  connectionTemplateError,
  mappingPreview,
  sourceOptions,
  transitionMappings,
} from "./connection-descriptors.ts";

test("connection catalog scopes dialects and clears stale protocol mappings", () => {
  assert(sourceOptions("postgres").some((item) => item.source === "connection.postgres.npgsql"));
  assert(!sourceOptions("nats").some((item) => item.source === "credential.password"));
  assert.deepEqual(
    transitionMappings([{ environment_name: "DATABASE_URL", symbolic_source: "connection.postgres.uri" }], "nats"),
    [{ environment_name: "DATABASE_URL", symbolic_source: "" }],
  );
  assert.deepEqual(
    transitionMappings([{ environment_name: "HOST", symbolic_source: "resource.host" }], "redis"),
    [{ environment_name: "HOST", symbolic_source: "resource.host" }],
  );
});

test("frontend template parser matches credential safety contract", () => {
  assert.equal(connectionTemplateError("Password = {{password|kv_quote}}"), "");
  assert.match(connectionTemplateError("password={{host}}"), /matching encoded placeholder/);
  assert.match(connectionTemplateError("{{password|url_query|kv_quote}}"), /only one encoder segment/);
  assert.match(connectionTemplateError("postgres://admin:{{password|url_userinfo}}@{{host}}"), /URL userinfo/);
  assert.match(connectionTemplateError("password={{password|kv_quote}}", false), /NATS templates/);
});

test("secret connection previews remain redacted", () => {
  assert.equal(mappingPreview("postgres", { environment_name: "DATABASE_URL", symbolic_source: "connection.postgres.uri" }), "[connection.postgres.uri · redacted]");
  assert.equal(mappingPreview("postgres", { environment_name: "DATABASE_DSN", symbolic_source: "connection.template", template: "Password={{password|kv_quote}}" }), "[connection.template · redacted]");
});
