import type { Locale } from "./types.ts";

export function localeToTag(locale: Locale): string {
  return locale === "vi" ? "vi-VN" : "en-US";
}

function toMillis(value?: string | number | Date | null): number | null {
  if (value === null || value === undefined || value === "") return null;
  if (value instanceof Date) return value.getTime();
  if (typeof value === "number") {
    return value < 10_000_000_000 ? value * 1000 : value;
  }
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : null;
}

export function formatDate(
  value?: string | number | Date | null,
  locale: Locale = "en",
  options?: Intl.DateTimeFormatOptions,
): string {
  const ms = toMillis(value);
  if (ms === null) return locale === "vi" ? "Chưa báo cáo" : "Not reported";
  return new Intl.DateTimeFormat(
    localeToTag(locale),
    options ?? { dateStyle: "medium", timeStyle: "short" },
  ).format(new Date(ms));
}

export function formatTime(
  value?: string | number | Date | null,
  locale: Locale = "en",
): string {
  const ms = toMillis(value);
  if (ms === null) return "-";
  return new Date(ms).toLocaleString(localeToTag(locale));
}

export function formatRelativeTime(
  value?: string | number | Date | null,
  locale: Locale = "en",
  now = Date.now(),
): string {
  const ms = toMillis(value);
  if (ms === null) return locale === "vi" ? "Chưa báo cáo" : "Not reported";

  const diffSec = Math.round((ms - now) / 1000);
  const absSec = Math.abs(diffSec);

  if (absSec < 5) {
    return locale === "vi" ? "Vừa xong" : "Just now";
  }

  const tag = localeToTag(locale);
  const formatter = new Intl.RelativeTimeFormat(tag, { numeric: "auto" });

  if (absSec < 60) {
    return formatter.format(diffSec, "second");
  }
  const diffMin = Math.round(diffSec / 60);
  if (Math.abs(diffMin) < 60) {
    return formatter.format(diffMin, "minute");
  }
  const diffHours = Math.round(diffMin / 60);
  if (Math.abs(diffHours) < 24) {
    return formatter.format(diffHours, "hour");
  }
  const diffDays = Math.round(diffHours / 24);
  if (Math.abs(diffDays) < 30) {
    return formatter.format(diffDays, "day");
  }

  return new Intl.DateTimeFormat(tag, { dateStyle: "medium", timeStyle: "short" }).format(new Date(ms));
}

export function formatFreshness(
  timestamp?: string | number | null,
  locale: Locale = "en",
  now = Date.now(),
): string {
  const ms = toMillis(timestamp);
  if (ms === null) return locale === "vi" ? "Chưa báo cáo" : "Not reported";

  const seconds = Math.round((now - ms) / 1000);
  if (seconds < 0 || seconds < 5) {
    return locale === "vi" ? "Vừa xong" : "Just now";
  }
  if (seconds < 60) {
    return locale === "vi" ? `Ghi nhận ${seconds} giây trước` : `Observed ${seconds}s ago`;
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return locale === "vi" ? `Ghi nhận ${minutes} phút trước` : `Observed ${minutes}m ago`;
  }
  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return locale === "vi" ? `Ghi nhận ${hours} giờ trước` : `Observed ${hours}h ago`;
  }
  const days = Math.round(hours / 24);
  return locale === "vi" ? `Ghi nhận ${days} ngày trước` : `Observed ${days}d ago`;
}

export function formatObserved(
  unixSeconds?: number | null,
  locale: Locale = "en",
): string {
  if (!unixSeconds) return locale === "vi" ? "Chưa báo cáo" : "Not reported";
  return new Intl.DateTimeFormat(localeToTag(locale), {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(unixSeconds * 1000));
}
