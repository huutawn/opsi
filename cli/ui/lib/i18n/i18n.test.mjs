import assert from "node:assert/strict";
import test from "node:test";

import {
  catalogs,
  formatDate,
  formatFreshness,
  formatObserved,
  formatRelativeTime,
  formatTime,
  getBrowserLocale,
  getStoredLocale,
  interpolate,
  isLocale,
  resolveInitialLocale,
  setStoredLocale,
  translate,
  en,
  vi,
} from "./index.ts";
import { statusLabel } from "../presentation/project.ts";

test("isLocale validates supported locales correctly", () => {
  assert.equal(isLocale("en"), true);
  assert.equal(isLocale("vi"), true);
  assert.equal(isLocale("fr"), false);
  assert.equal(isLocale(""), false);
  assert.equal(isLocale(null), false);
  assert.equal(isLocale(undefined), false);
  assert.equal(isLocale(123), false);
});

test("resolver detects browser locale and falls back safely", () => {
  const originalDescriptor = Object.getOwnPropertyDescriptor(globalThis, "navigator");

  function mockNav(languages, language) {
    Object.defineProperty(globalThis, "navigator", {
      value: { languages, language },
      configurable: true,
      writable: true,
    });
  }

  // Vietnamese browser locale variations
  for (const viTag of ["vi", "vi-VN", "vi-vn", "VI", "vi-US"]) {
    mockNav([viTag], viTag);
    assert.equal(getBrowserLocale(), "vi");
  }

  // English and other languages fall back to English
  for (const otherTag of ["en", "en-US", "en-GB", "fr-FR", "de-DE", "ja-JP", "es-ES"]) {
    mockNav([otherTag], otherTag);
    assert.equal(getBrowserLocale(), "en");
  }

  // Empty or missing navigator
  mockNav([], "");
  assert.equal(getBrowserLocale(), "en");

  // Restore navigator
  if (originalDescriptor) {
    Object.defineProperty(globalThis, "navigator", originalDescriptor);
  } else {
    delete globalThis.navigator;
  }
});

test("resolver handles localStorage reading, writing, and failure modes safely", () => {
  const mockStorage = new Map();
  const originalWindow = globalThis.window;

  // Mock working window.localStorage
  globalThis.window = {
    localStorage: {
      getItem: (key) => mockStorage.get(key) ?? null,
      setItem: (key, val) => mockStorage.set(key, String(val)),
      removeItem: (key) => mockStorage.delete(key),
    },
  };
  // resolveInitialLocale combines stored and browser resolution
  assert.equal(resolveInitialLocale(), "en");
  mockStorage.set("opsi:locale", "vi");
  assert.equal(resolveInitialLocale(), "vi");
  mockStorage.delete("opsi:locale");

  // Initially empty -> null
  assert.equal(getStoredLocale(), null);

  // Set valid locale
  setStoredLocale("vi");
  assert.equal(getStoredLocale(), "vi");

  setStoredLocale("en");
  assert.equal(getStoredLocale(), "en");

  // Invalid stored value -> returns null
  mockStorage.set("opsi:locale", "invalid_lang");
  assert.equal(getStoredLocale(), null);

  // Faulty / throwing localStorage
  globalThis.window.localStorage.getItem = () => {
    throw new Error("SecurityError: Access is denied");
  };
  globalThis.window.localStorage.setItem = () => {
    throw new Error("QuotaExceededError");
  };

  assert.doesNotThrow(() => {
    assert.equal(getStoredLocale(), null);
    setStoredLocale("vi");
  });

  // Clean up
  if (originalWindow) {
    globalThis.window = originalWindow;
  } else {
    delete globalThis.window;
  }
});

test("English and Vietnamese catalogs have identical keys with complete 1:1 coverage", () => {
  const enKeys = Object.keys(en).sort();
  const viKeys = Object.keys(vi).sort();

  assert.equal(enKeys.length > 50, true, "Catalog should have substantial coverage");
  assert.deepEqual(enKeys, viKeys, "Vietnamese catalog must match English catalog keys exactly");
  assert.equal(catalogs.en, en);
  assert.equal(catalogs.vi, vi);

  for (const key of enKeys) {
    assert.equal(typeof en[key], "string", `en[${key}] must be a non-empty string`);
    assert.equal(typeof vi[key], "string", `vi[${key}] must be a non-empty string`);
    assert.notEqual(en[key].trim().length, 0, `en[${key}] must not be blank`);
    assert.notEqual(vi[key].trim().length, 0, `vi[${key}] must not be blank`);
  }
});

test("interpolate substitutes parameters cleanly", () => {
  assert.equal(interpolate("Hello {name}", { name: "Alice" }), "Hello Alice");
  assert.equal(interpolate("Attempt {attempt} of {max}", { attempt: 1, max: 3 }), "Attempt 1 of 3");
  assert.equal(interpolate("Static text without params", {}), "Static text without params");
  assert.equal(interpolate("Missing {param} remains", {}), "Missing {param} remains");
});

test("translate handles translations, interpolations, and fallbacks safely", () => {
  // English translation
  assert.equal(translate("metadata.title", "en"), "Opsi Console");
  assert.equal(translate("settings.title", "en"), "Settings");

  // Vietnamese translation
  assert.equal(translate("metadata.title", "vi"), "Opsi Console");
  assert.equal(translate("settings.title", "vi"), "Cài đặt");
  assert.equal(translate("nav.projects", "vi"), "Dự án");

  // Interpolation with translate
  assert.equal(translate("settings.return_to_project", "en", { name: "Checkout" }), "Return to Checkout");
  assert.equal(translate("settings.return_to_project", "vi", { name: "Checkout" }), "Quay lại Checkout");

  // Missing key with fallback
  assert.equal(translate("non_existent_key", "vi", undefined, "Default Text"), "Default Text");
  assert.equal(translate("non_existent_key", "en", undefined, "Default Text"), "Default Text");
  assert.equal(translate("non_existent_key", "en"), "non_existent_key");
});

test("date and time formatters respect selected locale without static module cache", () => {
  const sampleTimeMs = 1785290900000;

  // formatDate
  const enDate = formatDate(sampleTimeMs, "en");
  const viDate = formatDate(sampleTimeMs, "vi");
  assert.notEqual(enDate, "");
  assert.notEqual(viDate, "");
  assert.equal(formatDate(null, "en"), "Not reported");
  assert.equal(formatDate(null, "vi"), "Chưa báo cáo");

  // formatTime
  const enTime = formatTime(sampleTimeMs, "en");
  const viTime = formatTime(sampleTimeMs, "vi");
  assert.notEqual(enTime, "-");
  assert.notEqual(viTime, "-");
  assert.equal(formatTime(null, "en"), "-");
  assert.equal(formatTime(null, "vi"), "-");

  // formatRelativeTime
  const now = 1785291000000;
  const enRel = formatRelativeTime(now - 120_000, "en", now);
  const viRel = formatRelativeTime(now - 120_000, "vi", now);
  assert.match(enRel, /2 minutes ago|2 min/);
  assert.match(viRel, /2 phút trước/);
  assert.equal(formatRelativeTime(now - 2000, "en", now), "Just now");
  assert.equal(formatRelativeTime(now - 2000, "vi", now), "Vừa xong");

  // formatFreshness
  assert.equal(formatFreshness(now - 10_000, "en", now), "Observed 10s ago");
  assert.equal(formatFreshness(now - 10_000, "vi", now), "Ghi nhận 10 giây trước");
  assert.equal(formatFreshness(now - 120_000, "en", now), "Observed 2m ago");
  assert.equal(formatFreshness(now - 120_000, "vi", now), "Ghi nhận 2 phút trước");
  assert.equal(formatFreshness(now - 7_200_000, "en", now), "Observed 2h ago");
  assert.equal(formatFreshness(now - 7_200_000, "vi", now), "Ghi nhận 2 giờ trước");
  assert.equal(formatFreshness(now - 172_800_000, "en", now), "Observed 2d ago");
  assert.equal(formatFreshness(now - 172_800_000, "vi", now), "Ghi nhận 2 ngày trước");
  assert.equal(formatFreshness(null, "en"), "Not reported");
  assert.equal(formatFreshness(null, "vi"), "Chưa báo cáo");

  // formatObserved
  assert.equal(formatObserved(null, "en"), "Not reported");
  assert.equal(formatObserved(null, "vi"), "Chưa báo cáo");
  assert.notEqual(formatObserved(1785290900, "en"), "Not reported");
  assert.notEqual(formatObserved(1785290900, "vi"), "Chưa báo cáo");
});

test("presentation status adapter supports both en and vi locales and preserves raw dynamic data", () => {
  const statuses = ["healthy", "degraded", "failed", "unknown", "unavailable", "in_progress"];

  for (const status of statuses) {
    const enLabel = statusLabel(status, "en");
    const viLabel = statusLabel(status, "vi");
    assert.notEqual(enLabel, "");
    assert.notEqual(viLabel, "");
    assert.notEqual(enLabel, viLabel, `Label for ${status} should differ between English and Vietnamese`);
  }

  assert.equal(statusLabel("healthy", "en"), "Healthy");
  assert.equal(statusLabel("healthy", "vi"), "Hoạt động tốt");
  assert.equal(statusLabel("degraded", "vi"), "Suy giảm");
  assert.equal(statusLabel("failed", "vi"), "Thất bại");
  assert.equal(statusLabel("in_progress", "vi"), "Đang xử lý");

  // Raw dynamic API errors, technical names, and hashes remain untranslated and uncorrupted
  const rawApiError = "dial tcp 10.0.0.1:443: connect: connection refused";
  assert.equal(rawApiError, "dial tcp 10.0.0.1:443: connect: connection refused");
  const sha = "sha256:abcdef0123456789";
  assert.equal(sha, "sha256:abcdef0123456789");
});
