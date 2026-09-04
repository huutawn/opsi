export type Locale = "en" | "vi";

export const SUPPORTED_LOCALES: readonly Locale[] = ["en", "vi"] as const;
export const DEFAULT_LOCALE: Locale = "en";
export const LOCALE_STORAGE_KEY = "opsi:locale";

export type InterpolationValues = Record<string, string | number | boolean | undefined | null>;

export interface I18nContextValue {
  locale: Locale;
  setLocale: (next: Locale) => void;
  t: (key: string, valuesOrFallback?: InterpolationValues | string, fallback?: string) => string;
  formatTime: (value?: string | number | null) => string;
  formatRelativeTime: (value?: string | number | null, now?: number) => string;
  formatDate: (value?: string | number | null, options?: Intl.DateTimeFormatOptions) => string;
}
