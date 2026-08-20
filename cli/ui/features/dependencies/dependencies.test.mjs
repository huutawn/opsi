import test from "node:test";
import assert from "node:assert/strict";
import {
  formatSymbolicSource,
  getPresetMappings,
  validateStrategyMatrix,
  calculateEdgeState,
} from "./types.ts";

test("formatSymbolicSource correctly renders safe human-readable descriptions", () => {
  assert.equal(
    formatSymbolicSource("connection.url", "postgres"),
    "PostgreSQL connection url"
  );
  assert.equal(
    formatSymbolicSource("connection.url", "redis"),
    "Valkey connection url"
  );
  assert.equal(
    formatSymbolicSource("endpoint.host", "postgres"),
    "PostgreSQL host endpoint"
  );
  assert.equal(
    formatSymbolicSource("endpoint.port", "postgres"),
    "PostgreSQL port number"
  );
  assert.equal(
    formatSymbolicSource("credential.username", "postgres"),
    "PostgreSQL credential username"
  );
  assert.equal(
    formatSymbolicSource("credential.password", "postgres"),
    "PostgreSQL password (symbolic reference)"
  );
  assert.equal(
    formatSymbolicSource("database.name", "postgres"),
    "Database name"
  );
});

test("getPresetMappings returns canonical injection blueprints without plain text secrets", () => {
  const pgUrl = getPresetMappings("postgres", "DATABASE_URL");
  assert.equal(pgUrl.length, 1);
  assert.equal(pgUrl[0].env_name, "DATABASE_URL");
  assert.equal(pgUrl[0].symbolic_source, "connection.url");

  const pgConv = getPresetMappings("postgres", "PG_CONVENTIONAL");
  assert.equal(pgConv.length, 5);
  const names = pgConv.map((m) => m.env_name);
  assert.deepEqual(names, ["PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD"]);

  const redisUrl = getPresetMappings("redis", "REDIS_URL");
  assert.equal(redisUrl.length, 1);
  assert.equal(redisUrl[0].env_name, "REDIS_URL");
  assert.equal(redisUrl[0].symbolic_source, "connection.url");
});

test("validateStrategyMatrix enforces browser vs server access context rules", () => {
  // Browser caller cannot use internal_http
  const browserInternal = validateStrategyMatrix("browser", "internal_http");
  assert.equal(browserInternal.valid, false);
  assert.match(browserInternal.error || "", /cannot route to private cluster endpoints/);

  // Server caller cannot use same_origin
  const serverSameOrigin = validateStrategyMatrix("server", "same_origin");
  assert.equal(serverSameOrigin.valid, false);
  assert.match(serverSameOrigin.error || "", /same_origin is only valid for browser/);

  // Valid combinations
  const browserSame = validateStrategyMatrix("browser", "same_origin");
  assert.equal(browserSame.valid, true);

  const browserPublic = validateStrategyMatrix("browser", "public_http");
  assert.equal(browserPublic.valid, true);

  const serverInternal = validateStrategyMatrix("server", "internal_http");
  assert.equal(serverInternal.valid, true);

  const serverPublic = validateStrategyMatrix("server", "public_http");
  assert.equal(serverPublic.valid, true);
});

test("calculateEdgeState returns Ready vs Needs setup based on active binding", () => {
  const bound = calculateEdgeState(true, "postgres", "app_db");
  assert.equal(bound.status, "Ready");
  assert.equal(bound.strokeDasharray, undefined);
  assert.equal(bound.label, "PostgreSQL · app_db · Ready");

  const unbound = calculateEdgeState(false, "postgres", "app_db");
  assert.equal(unbound.status, "Needs setup");
  assert.equal(unbound.strokeDasharray, "6 4");
  assert.equal(unbound.label, "PostgreSQL · app_db · Needs setup");

  const valkeyBound = calculateEdgeState(true, "redis", "cache");
  assert.equal(valkeyBound.status, "Ready");
  assert.equal(valkeyBound.label, "Valkey · cache · Ready");
});
