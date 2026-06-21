// OverviewTiles — condensed stat strip at the top of the unified Session Detail
// view (ui-turn-view.md §Overview tiles). Six tiles: Status, Duration, Tokens
// in→out, Cost, Failures. Each is a compact pill with a tooltip carrying the
// full context the old OverviewTab provided (agent name, model, context pressure,
// running pulse, etc).

import { useEffect, useState } from 'react';
import type { SessionDetailResponse } from '../../../api/types';
import { ContextPressure } from '../../../components/ContextPressure';
import { StaleBadge } from '../../../components/StaleBadge';
import styles from './OverviewTiles.module.css';

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1000) return `${(n / 1000).toFixed(1)}k`;
  return String(n);
}

function formatCost(n: number): string {
  if (n === 0) return '$0.00';
  if (n < 0.01) return `$${n.toFixed(4)}`;
  return `$${n.toFixed(4).replace(/\.?0+$/, '')}`;
}

function formatDuration(start: number, end: number | null): string {
  if (end === null) return 'running';
  const us = end - start;
  if (us < 1000) return `${us}µs`;
  if (us < 1_000_000) return `${(us / 1000).toFixed(0)}ms`;
  if (us < 60_000_000) return `${(us / 1_000_000).toFixed(2)}s`;
  return `${(us / 60_000_000).toFixed(1)}m`;
}

interface AggregateStats {
  tokensIn: number;
  tokensOut: number;
  cost: number;
  failures: number;
}

function aggregateTurns(detail: SessionDetailResponse): AggregateStats {
  let tokensIn = 0;
  let tokensOut = 0;
  let cost = 0;
  let failures = 0;
  for (const turn of detail.turns) {
    tokensIn += turn.tokens_in;
    tokensOut += turn.tokens_out;
    cost += turn.cost_usd;
    for (const op of turn.ops) {
      if (op.error_class !== null) failures += 1;
    }
  }
  return { tokensIn, tokensOut, cost, failures };
}

export function OverviewTiles({ detail }: { detail: SessionDetailResponse }) {
  const session = detail.session;
  const stats = aggregateTurns(detail);
  // SOW-0089 chunk 5a: use the derived effective_status for the "is the
  // session live?" check, so the Overview tile flips to "stale · Nm" as
  // soon as the activity threshold trips (without waiting for ingest to
  // notice the source died).
  const displayStatus = session.effective_status ?? session.status;
  const isRunning = displayStatus === 'running';

  // StaleBadge needs nowUs; Date.now() is impure so we read it once per
  // mount + every 30s while the panel is visible (the stale badge is
  // time-dependent). The 30s tick is enough granularity for the 10-minute
  // stale threshold.
  const [nowUs, setNowUs] = useState(() => Date.now() * 1000);
  useEffect(() => {
    if (!isRunning) return;
    const tick = setInterval(() => {
      setNowUs(Date.now() * 1000);
    }, 30_000);
    return () => {
      clearInterval(tick);
    };
  }, [isRunning]);

  return (
    <div className={styles.tiles} role="group" aria-label="Session overview">
      <div className={styles.tile} data-kind="status">
        <span className={styles.label}>Status</span>
        <span className={styles.value} data-status={displayStatus}>
          {/* SOW-0090 polish: show the StaleBadge inline ONLY when the session
              is currently running (process alive but idle). When effective_status
              is already "stale" the tile's text shows it once — the badge would
              be redundant. */}
          {isRunning ? <StaleBadge lastActivityTs={session.last_activity_ts ?? null} status={displayStatus} nowUs={nowUs} /> : displayStatus}
        </span>
      </div>

      <div className={styles.tile}>
        <span className={styles.label}>Duration</span>
        <span className={styles.value}>{formatDuration(session.start_ts, session.end_ts)}</span>
      </div>

      <div className={styles.tile}>
        <span className={styles.label}>Tokens</span>
        <span className={styles.value}>{formatTokens(stats.tokensIn)}→{formatTokens(stats.tokensOut)}</span>
      </div>

      <div className={styles.tile}>
        <span className={styles.label}>Cost</span>
        <span className={styles.value}>{formatCost(stats.cost)}</span>
      </div>

      <div className={styles.tile}>
        <span className={styles.label}>Failures</span>
        <span className={styles.value} data-tone={stats.failures > 0 ? 'error' : 'neutral'}>
          {stats.failures}
        </span>
      </div>

      <div className={styles.tile}>
        <span className={styles.label}>Context</span>
        <span className={styles.value}>
          <ContextPressure model={session.model} tokensIn={stats.tokensIn} tokensCacheRead={session.tokens_cache_read} />
        </span>
      </div>
    </div>
  );
}