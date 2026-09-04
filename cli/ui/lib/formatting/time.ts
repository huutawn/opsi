import { formatTime as formatTimeI18n, type Locale } from "../i18n/index.ts";

export function formatTime(value?: string | number | Date | null, locale?: Locale) {
  return formatTimeI18n(value, locale);
}
