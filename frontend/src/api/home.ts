import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import { get, buildQuery } from './client';
import type { StatsResponse } from './types';

// Home summary — SOW-0079 P0.1.
//
// Aggregates for the Home summary card: "today" (since local midnight)
// and "running now" (currently in-flight sessions). Both are derived from
// the existing /api/stats endpoint with different filter sets, so the data
// layer needs no new endpoint — only a new hook + a Home card UI.
//
// Reuses the ['stats', filters] cache key prefix so the SSE `stats_invalidated`
// event invalidates these queries alongside the rest of the stats consumers.

// todayMidnightUs returns the local-midnight UNIX-microseconds boundary used
// as the "from" filter for the today's-stats call. The local TZ matters:
// the operator's "today" is their wall-clock day, not UTC.
export function todayMidnightUs(): number {
  const now = new Date();
  const midnight = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 0, 0, 0, 0);
  return midnight.getTime() * 1000;
}

export interface HomeSummary {
  today: {
    sessionCount: number;
    opCount: number;
    tokensIn: number;
    tokensOut: number;
    tokensCacheRead: number;
    tokensCacheWrite: number;
    costUsd: number;
    failures: number;
  } | null;
  running: {
    sessionCount: number;
    costUsd: number;
  } | null;
  /** Local midnight (microseconds) used as the "from" filter for today. */
  todayFromUs: number;
}

const EMPTY: HomeSummary = { today: null, running: null, todayFromUs: todayMidnightUs() };

export function useHomeSummary(): UseQueryResult<HomeSummary> {
  const todayFromUs = todayMidnightUs();
  return useQuery({
    queryKey: ['home-summary', todayFromUs] as const,
    // Stale enough that we don't refetch on every keystroke; the SSE
    // stats_invalidated event will invalidate this query on real changes.
    staleTime: 30_000,
    refetchOnWindowFocus: true,
    queryFn: async ({ signal }): Promise<HomeSummary> => {
      // Two parallel calls to /api/stats with different filters. Each is
      // bounded by the (open) day so the cost is small.
      const [today, running] = await Promise.all([
        get<StatsResponse>(`/stats${buildQuery({ from: todayFromUs })}`, signal),
        get<StatsResponse>(`/stats${buildQuery({ status: ['running'] })}`, signal),
      ]);
      return {
        today: {
          sessionCount: today.totals.session_count,
          opCount: today.totals.op_count,
          tokensIn: today.totals.tokens_in,
          tokensOut: today.totals.tokens_out,
          tokensCacheRead: today.totals.tokens_cache_read,
          tokensCacheWrite: today.totals.tokens_cache_write,
          costUsd: today.totals.cost_usd,
          failures: today.totals.failures,
        },
        running: {
          sessionCount: running.totals.session_count,
          costUsd: running.totals.cost_usd,
        },
        todayFromUs,
      };
    },
  });
}

export const EMPTY_HOME_SUMMARY: HomeSummary = EMPTY;