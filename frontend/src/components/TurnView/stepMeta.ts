// stepMeta (SOW-0090 chunk 8): per-step header metadata — step index, elapsed
// since turn start, wall-clock time, and op-id badge. Operators want this at
// a glance when scanning a long turn so they can answer "when did this tool
// call run, and which op is it?" without leaving the page.

/** formatElapsed returns a compact "+0ms", "+1.2s", "+45.0s", "+3m12s"
 *  string for the microsecond delta between turn-start and op-start.
 *  The "+" prefix signals "elapsed since turn start" rather than a
 *  wall-clock time. Uses Math.floor (NOT toFixed) for integer-second
 *  formatting — toFixed rounds (banker's rounding) which would print
 *  "60s" for 59.999s. */
export function formatElapsed(elapsedUs: number): string {
  if (elapsedUs < 0) elapsedUs = 0;
  if (elapsedUs < 1000) return `+${elapsedUs}µs`;
  if (elapsedUs < 1_000_000) return `+${Math.floor(elapsedUs / 1000)}ms`;
  // For >= 1s, switch to seconds with one decimal under 60s, then m:ss.
  const secs = elapsedUs / 1_000_000;
  if (secs < 60) {
    if (secs < 10) return `+${secs.toFixed(1)}s`;
    return `+${Math.floor(secs)}s`;
  }
  const mins = Math.floor(secs / 60);
  const remSecs = Math.floor(secs - mins * 60);
  return `+${mins}m${String(remSecs).padStart(2, '0')}s`;
}

/** formatWallClock returns HH:MM:SS in UTC (or local time if `local=true`).
 *  The microsecond Unix timestamp comes from the op's start_ts. We render
 *  UTC by default so the operator can correlate with server logs without
 *  timezone confusion; pass local=true if they want their wall-clock. */
export function formatWallClock(tsUs: number, local = false): string {
  const d = new Date(tsUs / 1000);
  const fmt = (n: number): string => String(n).padStart(2, '0');
  if (local) {
    return `${fmt(d.getHours())}:${fmt(d.getMinutes())}:${fmt(d.getSeconds())}`;
  }
  return `${fmt(d.getUTCHours())}:${fmt(d.getUTCMinutes())}:${fmt(d.getUTCSeconds())}Z`;
}

/** shortOpId returns the first 8 characters of an op id for compact display
 *  in the step header badge. The full id appears in the tooltip via the
 *  `title` attribute on the badge — the operator can hover to confirm. */
export function shortOpId(opId: string): string {
  if (opId.length <= 8) return opId;
  return opId.slice(0, 8);
}
