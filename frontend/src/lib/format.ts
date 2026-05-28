// Display formatters. All timestamps from the API are UNIX MICROSECONDS UTC
// (rest-api.md §Conventions); durations are microseconds. These helpers are
// pure and unit-tested (lib coverage gate).

const US_PER_MS = 1000;
const US_PER_SEC = 1_000_000;
const US_PER_MIN = 60 * US_PER_SEC;
const US_PER_HOUR = 60 * US_PER_MIN;

/**
 * formatTimestamp renders a µs UNIX timestamp as a local date-time string.
 * Returns an em dash for null/undefined (open-ended end_ts is common). Invalid
 * inputs also render the em dash rather than "Invalid Date".
 */
export function formatTimestamp(us: number | null | undefined): string {
  if (us === null || us === undefined || !Number.isFinite(us)) {
    return '—';
  }
  const d = new Date(us / US_PER_MS);
  if (Number.isNaN(d.getTime())) {
    return '—';
  }
  return d.toLocaleString();
}

/**
 * formatDuration renders a µs duration as a compact human string
 * (e.g. "850ms", "1.5s", "2m 30s", "1h 5m"). Null/undefined → em dash.
 * Negative values are clamped to 0.
 */
export function formatDuration(us: number | null | undefined): string {
  if (us === null || us === undefined || !Number.isFinite(us)) {
    return '—';
  }
  const v = Math.max(0, us);
  if (v < US_PER_MS) {
    return `${Math.round(v)}µs`;
  }
  if (v < US_PER_SEC) {
    return `${Math.round(v / US_PER_MS)}ms`;
  }
  if (v < US_PER_MIN) {
    return `${trimZero(v / US_PER_SEC)}s`;
  }
  if (v < US_PER_HOUR) {
    const mins = Math.floor(v / US_PER_MIN);
    const secs = Math.round((v % US_PER_MIN) / US_PER_SEC);
    return secs > 0 ? `${mins}m ${secs}s` : `${mins}m`;
  }
  const hours = Math.floor(v / US_PER_HOUR);
  const mins = Math.round((v % US_PER_HOUR) / US_PER_MIN);
  return mins > 0 ? `${hours}h ${mins}m` : `${hours}h`;
}

/** trimZero drops a trailing ".0" from a one-decimal fixed string. */
function trimZero(n: number): string {
  const s = n.toFixed(1);
  return s.endsWith('.0') ? s.slice(0, -2) : s;
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB'] as const;

/**
 * formatBytes renders a byte count with binary scaling (1024). Null/undefined →
 * em dash. Sub-KB values show no decimals; larger values show one.
 */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes)) {
    return '—';
  }
  const v = Math.max(0, bytes);
  if (v < 1024) {
    return `${Math.round(v)} B`;
  }
  let value = v;
  let unit = 0;
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(1)} ${BYTE_UNITS[unit]}`;
}

/**
 * formatCost renders a USD cost. Zero → "$0.00"; tiny non-zero values use more
 * precision so a fraction-of-a-cent op is not rendered as $0.00. Null/undefined
 * → em dash.
 */
export function formatCost(usd: number | null | undefined): string {
  if (usd === null || usd === undefined || !Number.isFinite(usd)) {
    return '—';
  }
  if (usd === 0) {
    return '$0.00';
  }
  if (Math.abs(usd) < 0.01) {
    return `$${usd.toFixed(4)}`;
  }
  return `$${usd.toFixed(2)}`;
}

/**
 * formatNumber renders an integer-ish count with thousands separators
 * (e.g. 12345 -> "12,345"). Null/undefined → em dash.
 */
export function formatNumber(n: number | null | undefined): string {
  if (n === null || n === undefined || !Number.isFinite(n)) {
    return '—';
  }
  return n.toLocaleString();
}

/**
 * formatPct renders a ratio in [0,1] as a percent string with one decimal
 * (e.g. 0.42 -> "42.0%"). Null/undefined → em dash.
 */
export function formatPct(ratio: number | null | undefined): string {
  if (ratio === null || ratio === undefined || !Number.isFinite(ratio)) {
    return '—';
  }
  return `${(ratio * 100).toFixed(1)}%`;
}
