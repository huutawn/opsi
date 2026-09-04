"use client";

import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  formatDate as formatWithLocale,
  formatRelativeTime as formatRelativeWithLocale,
  formatTime as formatTimeWithLocale,
} from "./formatting.ts";
import { en } from "./locales/en.ts";
import { vi } from "./locales/vi.ts";
import { resolveInitialLocale, setStoredLocale } from "./resolver.ts";
import {
  DEFAULT_LOCALE,
  type I18nContextValue,
  type InterpolationValues,
  type Locale,
} from "./types.ts";

export const catalogs: Record<Locale, Record<string, string>> = {
  en,
  vi,
};

export function interpolate(template: string, values?: InterpolationValues): string {
  if (!values || Object.keys(values).length === 0) {
    return template;
  }
  return template.replace(/\{(\w+)\}/g, (match, key: string) => {
    const replacement = values[key];
    return replacement !== undefined && replacement !== null ? String(replacement) : match;
  });
}

export function translate(
  key: string,
  locale: Locale = DEFAULT_LOCALE,
  valuesOrFallback?: InterpolationValues | string,
  fallback?: string,
): string {
  let values: InterpolationValues | undefined;
  let fallbackText: string | undefined = fallback;

  if (typeof valuesOrFallback === "string") {
    fallbackText = valuesOrFallback;
  } else if (valuesOrFallback && typeof valuesOrFallback === "object") {
    values = valuesOrFallback;
  }

  const catalog = catalogs[locale];
  const template = catalog?.[key] ?? catalogs.en[key] ?? fallbackText ?? key;
  return interpolate(template, values);
}

const I18nContext = createContext<I18nContextValue | null>(null);

export function LocaleProvider({ children }: { children: ReactNode }) {
  // Always start with DEFAULT_LOCALE ("en") on the first server/static render to avoid hydration mismatch
  const [locale, setLocaleState] = useState<Locale>(DEFAULT_LOCALE);

  // After hydrate, resolve the preferred locale (stored preference or browser language)
  useEffect(() => {
    const initial = resolveInitialLocale();
    if (initial !== DEFAULT_LOCALE) {
      setLocaleState(initial);
    }
    if (typeof document !== "undefined") {
      document.documentElement.lang = initial;
      const title = translate("metadata.title", initial);
      if (title) document.title = title;
    }
  }, []);

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    setStoredLocale(next);
    if (typeof document !== "undefined") {
      document.documentElement.lang = next;
      const title = translate("metadata.title", next);
      if (title) document.title = title;
    }
  }, []);

  const t = useCallback(
    (key: string, valuesOrFallback?: InterpolationValues | string, fallback?: string) => {
      return translate(key, locale, valuesOrFallback, fallback);
    },
    [locale],
  );

  const formatTime = useCallback(
    (value?: string | number | null) => formatTimeWithLocale(value, locale),
    [locale],
  );

  const formatRelativeTime = useCallback(
    (value?: string | number | null, now?: number) =>
      formatRelativeWithLocale(value, locale, now),
    [locale],
  );

  const formatDate = useCallback(
    (value?: string | number | null, options?: Intl.DateTimeFormatOptions) =>
      formatWithLocale(value, locale, options),
    [locale],
  );

  const value = useMemo(
    () => ({
      locale,
      setLocale,
      t,
      formatTime,
      formatRelativeTime,
      formatDate,
    }),
    [locale, setLocale, t, formatTime, formatRelativeTime, formatDate],
  );

  return React.createElement(I18nContext.Provider, { value }, children);
}

export function useI18n(): I18nContextValue {
  const context = useContext(I18nContext);
  if (!context) {
    // Graceful fallback for components rendered outside LocaleProvider (e.g. isolated test environments)
    return {
      locale: DEFAULT_LOCALE,
      setLocale: () => {},
      t: (key, valuesOrFallback, fallback) =>
        translate(key, DEFAULT_LOCALE, valuesOrFallback, fallback),
      formatTime: (value) => formatTimeWithLocale(value, DEFAULT_LOCALE),
      formatRelativeTime: (value, now) => formatRelativeWithLocale(value, DEFAULT_LOCALE, now),
      formatDate: (value, options) => formatWithLocale(value, DEFAULT_LOCALE, options),
    };
  }
  return context;
}
