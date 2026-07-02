export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// Relative time for the dashboard/signals/work-item views: empty input renders
// as "" and future timestamps clamp to 0 ("0s ago").
export function timeAgo(iso?: string): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (isNaN(t)) return "";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

// Relative time for the collectors view: empty input renders as "never" and
// future timestamps render as "in X" (used for a unit's next-due time).
export function timeAgoSigned(iso?: string): string {
  if (!iso) return "never";
  const t = Date.parse(iso);
  if (isNaN(t)) return "";
  const s = (Date.now() - t) / 1000;
  const abs = Math.abs(s);
  let label: string;
  if (abs < 60) label = Math.floor(abs) + "s";
  else if (abs < 3600) label = Math.floor(abs / 60) + "m";
  else if (abs < 86400) label = Math.floor(abs / 3600) + "h";
  else label = Math.floor(abs / 86400) + "d";
  return s >= 0 ? label + " ago" : "in " + label;
}
