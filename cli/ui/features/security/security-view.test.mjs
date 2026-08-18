import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const component = new URL("./security-view.tsx", import.meta.url);
const accessTab = new URL("./access-tab.tsx", import.meta.url);
const overviewTab = new URL("./overview-tab.tsx", import.meta.url);
const auditTab = new URL("./audit-tab.tsx", import.meta.url);
const router = new URL("../console/router-map.tsx", import.meta.url);

test("Security Center is strictly audit, visibility, and access inspection without secret mutation controls", async () => {
  const [source, accessSource, overviewSource, auditSource, routerSource] = await Promise.all([
    readFile(component, "utf8"),
    readFile(accessTab, "utf8"),
    readFile(overviewTab, "utf8"),
    readFile(auditTab, "utf8"),
    readFile(router, "utf8"),
  ]);

  // Routing and canonical tabs
  assert.match(source, /OverviewTab/);
  assert.match(source, /AuditTab/);
  assert.match(source, /AccessTab/);
  assert.match(routerSource, /security: \{ overview: SecurityView, audit: SecurityView, access: SecurityView, secrets: SecurityView \}/);

  // Factual security visibility and safe metadata
  assert.match(overviewSource, /Security Overview/);
  assert.match(overviewSource, /Loaded Audit Events/);
  assert.match(overviewSource, /Denied Operations/);
  assert.match(overviewSource, /High-Impact Operations/);
  assert.match(auditSource, /Audit/);
  assert.match(auditSource, /safeAuditMetadata/);
  assert.match(accessSource, /Access & Identities/);
  assert.match(accessSource, /Authenticated Session/);
  assert.match(accessSource, /Authority Connections/);
  assert.match(accessSource, /Connected Nodes & Machine Authorities/);

  // Strictly NO secret mutation, reveal, rotation, or TOTP administration controls in Security UI
  for (const fileSource of [source, accessSource, overviewSource, auditSource]) {
    assert.doesNotMatch(fileSource, /<form[^>]*onSubmit/);
    assert.doesNotMatch(fileSource, /secretCreate|secretReveal|secretRotate|setupTOTP/);
    assert.doesNotMatch(fileSource, /SecondFactorFields|ProtectedResult/);
    assert.doesNotMatch(fileSource, /Reveal Secret|Rotate Secret|Create Secret|Set up TOTP/i);
    assert.doesNotMatch(fileSource, /name="otp_code"|name="totp_code"/);
  }

  // Strictly NO synthetic compliance scoring, percentage, or risk grade
  for (const fileSource of [source, accessSource, overviewSource, auditSource]) {
    assert.doesNotMatch(fileSource, /compliance score|security score|risk score|compliance grade/i);
    assert.doesNotMatch(fileSource, /\b(SOC|ISO|CIS|PCI|HIPAA)\b/);
  }
});

test("Security Center preserves safe cross-surface links without destructive action duplication", async () => {
  const [accessSource, auditSource] = await Promise.all([
    readFile(accessTab, "utf8"),
    readFile(auditTab, "utf8"),
  ]);

  // Links to canonical surfaces
  assert.match(accessSource, /console\.navigate\(\{ view: "infrastructure", tab: "servers"/);
  assert.match(accessSource, /console\.navigate\(\{ view: "services", service:/);
  assert.match(auditSource, /console\.navigate\((?:selectedRow|item)\.crossLink/);

  // No duplicated destructive action triggers
  for (const fileSource of [accessSource, auditSource]) {
    assert.doesNotMatch(fileSource, /Delete Server|Destroy Storage|Delete Resource/i);
  }
});
