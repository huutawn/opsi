import assert from "node:assert/strict";
import test from "node:test";
import {
  getApplicationEffectiveKeys,
  getUnreviewedApplications,
  isApplicationConfirmed,
  isApplicationReviewed,
  isSecretLikeEnvironmentName,
  isValidEnvironmentName,
} from "./runtime-config.ts";

test("isSecretLikeEnvironmentName correctly identifies secret-like names", () => {
  for (const name of [
    "PASSWORD",
    "DB_PASSWORD",
    "my_passwd",
    "API_KEY",
    "SECRET_TOKEN",
    "AUTH_CREDENTIAL",
    "CLIENT_SECRET",
    "APP_PRIVATEKEY",
    "SIGNING_KEY",
    "ACCESS_KEY",
    "DATABASE_CONNECTIONSTRING",
  ]) {
    assert.equal(isSecretLikeEnvironmentName(name), true, `expected ${name} to be secret-like`);
  }

  for (const name of [
    "PORT",
    "NODE_ENV",
    "APP_ENV",
    "DATABASE_URL",
    "REDIS_URL",
    "TIMEOUT_SECONDS",
    "PUBLIC_HOSTNAME",
    "ENABLE_LOGGING",
    "LOG_LEVEL",
  ]) {
    assert.equal(isSecretLikeEnvironmentName(name), false, `expected ${name} to not be secret-like`);
  }
});

test("isValidEnvironmentName enforces POSIX/Docker environment naming standard", () => {
  for (const name of ["PORT", "NODE_ENV", "_CUSTOM_VAR", "VAR1", "a", "A_1_b"]) {
    assert.equal(isValidEnvironmentName(name), true, `expected ${name} to be valid`);
  }

  for (const name of ["1PORT", "NODE-ENV", "VAR.NAME", "VAR NAME", "VAR@VALUE", ""]) {
    assert.equal(isValidEnvironmentName(name), false, `expected ${name} to be invalid`);
  }
});

test("getApplicationEffectiveKeys aggregates plain, secret, and generated keys", () => {
  const plan = {
    schema_version: "opsi.deployment_plan/v3",
    hash: "h",
    source: {},
    applications: [
      {
        source_key: "api",
        key: "api-service",
        name: "api",
        environment: { PORT: "8080", LOG_LEVEL: "info" },
      },
      {
        source_key: "web",
        key: "web-service",
        name: "web",
        environment: {},
      },
    ],
    secrets: [
      {
        name: "oauth-client",
        application_key: "api-service",
        environment_name: "OAUTH_SECRET",
      },
    ],
    dependencies: [
      {
        from: "api-service",
        to: "postgres",
        injections: [{ environment_name: "DATABASE_URL", symbolic_source: "connection.postgres.uri" }],
      },
      {
        from: "web-service",
        to: "api-service",
        injections: [{ environment_name: "BACKEND_URL", symbolic_source: "application.internal_url" }],
      },
    ],
  };

  const apiKeys = getApplicationEffectiveKeys(plan, plan.applications[0]);
  assert.equal(apiKeys.plain.length, 2);
  assert.equal(apiKeys.secrets.length, 1);
  assert.equal(apiKeys.generated.length, 1);
  assert.equal(apiKeys.total, 4);

  const webKeys = getApplicationEffectiveKeys(plan, plan.applications[1]);
  assert.equal(webKeys.plain.length, 0);
  assert.equal(webKeys.secrets.length, 0);
  assert.equal(webKeys.generated.length, 1);
  assert.equal(webKeys.total, 1);
});

test("isApplicationReviewed and getUnreviewedApplications enforce review requirements", () => {
  const plan = {
    schema_version: "opsi.deployment_plan/v3",
    hash: "h",
    source: {},
    applications: [
      { source_key: "api", key: "api", name: "api", environment: { PORT: "8080" } },
      { source_key: "worker", key: "worker", name: "worker", environment: {} },
      { source_key: "web", key: "web", name: "web", environment: {} },
    ],
    secrets: [],
    dependencies: [],
    application_environment_reviews: [
      { application_source_key: "worker", no_environment_required: true },
    ],
  };

  assert.equal(isApplicationReviewed(plan, plan.applications[0]), true); // Has plain key
  assert.equal(isApplicationReviewed(plan, plan.applications[1]), true); // Has confirmation
  assert.equal(isApplicationReviewed(plan, plan.applications[2]), false); // Unreviewed!

  assert.equal(isApplicationConfirmed(plan, plan.applications[1]), true);
  assert.equal(isApplicationConfirmed(plan, plan.applications[2]), false);

  const unreviewed = getUnreviewedApplications(plan);
  assert.equal(unreviewed.length, 1);
  assert.equal(unreviewed[0].source_key, "web");
});
