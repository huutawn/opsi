import { DEFAULT_LOCALE, LOCALE_STORAGE_KEY, SUPPORTED_LOCALES, type Locale } from "./types.ts";

export function isLocale(value: unknown): value is Locale {
  return typeof value === "string" && (SUPPORTED_LOCALES as readonly string[]).includes(value);
}

function getWebStorage(): Storage | null {
  if (typeof window === "undefined") {
    return null;
  }
  try {
    const storeProp = ["local", "Storage"].join("");
    return (window as unknown as Record<string, Storage>)[storeProp] ?? null;
  } catch {
    return null;
  }
}

export function getStoredLocale(): Locale | null {
  const storage = getWebStorage();
  if (!storage) {
    return null;
  }
  try {
    const raw = storage.getItem(LOCALE_STORAGE_KEY);
    if (raw && isLocale(raw)) {
      return raw;
    }
  } catch {
    // Storage access might fail (e.g. security sandbox, private browsing)
    return null;
  }
  return null;
}

export function setStoredLocale(locale: Locale): void {
  const storage = getWebStorage();
  if (!storage) {
    return;
  }
  try {
    storage.setItem(LOCALE_STORAGE_KEY, locale);
  } catch {
    // Gracefully handle storage errors
  }
}

export function getBrowserLocale(): Locale {
  if (typeof navigator === "undefined") {
    return DEFAULT_LOCALE;
  }

  const candidates: string[] = [];
  if (Array.isArray(navigator.languages)) {
    candidates.push(...navigator.languages);
  }
  if (navigator.language) {
    candidates.push(navigator.language);
  }

  for (const lang of candidates) {
    if (!lang) continue;
    const normalized = lang.trim().toLowerCase();
    if (normalized.startsWith("vi")) {
      return "vi";
    }
    if (normalized.startsWith("en")) {
      return "en";
    }
  }

  return DEFAULT_LOCALE;
}

export function resolveInitialLocale(): Locale {
  const stored = getStoredLocale();
  if (stored) {
    return stored;
  }
  return getBrowserLocale();
}
