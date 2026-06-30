export function formatTime(value?: string) {
  return value ? new Date(value).toLocaleString() : "-";
}
