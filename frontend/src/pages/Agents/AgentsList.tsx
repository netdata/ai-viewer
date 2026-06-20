import { useMemo } from 'react';
import { useStats } from '../../api/stats';
import { EntityListCard, EntityListSkeleton, EntityListEmpty, fmtCost, fmtNum } from '../../components/EntityList';
import { EntitySummaryStrip, EntitySummaryStripSkeleton } from '../../components/EntitySummaryStrip';
import { useFilters } from '../../state/filters';
import { Skeleton } from '../../components/ui/skeleton';
import type { StatAgentRow } from '../../api/types';

// Stable empty array reference for the useMemo deps (avoids re-memo churn
// when the query is still pending or the result has no by_agent breakdown).
const EMPTY_AGENTS: readonly StatAgentRow[] = Object.freeze([]);

// AgentsList — /agents (SOW-0081)
//
// Lists every distinct agent_name in the current filter window as a card
// grid. Each card shows the agent's primary metrics and drills into
// /agents/:name on click. The page header follows the standard per-page
// pattern (ui-pages.md §/agents).

export function AgentsList() {
  const { filters, setFilters } = useFilters();
  const { data, isPending, isError, error } = useStats(filters);

  // Sum totals across all agents for the summary strip. The agent list
  // is from by_agent; the same endpoint also returns a `totals` block
  // but that's the totals for the WHOLE filter, which is what we want
  // for the summary strip (so clearing an agent filter changes the strip).
  const totals = data?.totals;
  const rawAgents = data?.by_agent;
  // Stable reference for the empty case so the useMemo deps don't churn.
  const agents = useMemo(() => rawAgents ?? EMPTY_AGENTS, [rawAgents]);

  // Aggregate reliability: completed / ended across ALL agents in the
  // current view. This is the "fleet reliability" number an operator
  // wants at-a-glance on the agents index. StatsTotals doesn't track
  // running vs ended separately, so we use session_count - failures as
  // the denominator — same approximation as the HomeSummaryCard.
  const fleetReliability = useMemo(() => {
    if (!totals) return null;
    const denom = totals.session_count - totals.failures;
    if (totals.session_count <= 0 || denom <= 0) return null;
    return denom / totals.session_count;
  }, [totals]);

  // Sorted agents (memoized so the reference is stable across re-renders
  // for the same `agents` input from useStats).
  const sortedAgents = useMemo(
    () => [...agents].sort((a, b) => b.sessions - a.sessions),
    [agents],
  );

  const summaryTiles = useMemo(
    () => {
      const sessionsTile = totals
        ? { label: 'Sessions', value: totals.session_count, format: 'number' as const }
        : { label: 'Sessions', value: null as number | null, format: 'number' as const };
      const costTile = totals
        ? { label: 'Total cost', value: totals.cost_usd, format: 'cost' as const }
        : { label: 'Total cost', value: null as number | null, format: 'cost' as const };
      return [
        { label: 'Agents', value: agents.length, format: 'number' as const },
        sessionsTile,
        costTile,
        {
          label: 'Reliability',
          value: fleetReliability,
          format: 'percent' as const,
          tone:
            fleetReliability === null
              ? ('default' as const)
              : fleetReliability >= 0.9
                ? ('success' as const)
                : fleetReliability >= 0.7
                  ? ('default' as const)
                  : ('failed' as const),
        },
      ];
    },
    [agents.length, totals, fleetReliability],
  );

  const activeFilterCount =
    filters.agents.length +
    filters.models.length +
    filters.tools.length +
    filters.status.length +
    filters.sources.length +
    (filters.from !== undefined ? 1 : 0) +
    (filters.to !== undefined ? 1 : 0) +
    (filters.q !== undefined && filters.q.length > 0 ? 1 : 0);

  return (
    <section aria-labelledby="agents-title" className="flex flex-col gap-6 px-6 py-5">
      {/* Header */}
      <div>
        <h1 id="agents-title" className="text-2xl font-semibold tracking-tight">
          Agents
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Every distinct agent name across your sessions, ranked by activity.
        </p>
      </div>

      {/* Summary strip */}
      {isPending ? (
        <EntitySummaryStripSkeleton />
      ) : (
        <EntitySummaryStrip tiles={summaryTiles} />
      )}

      {/* Body */}
      {isError ? (
        <div className="rounded-lg border border-border bg-card p-12 text-sm text-status-failed">
          Failed to load agents: {error instanceof Error ? error.message : 'unknown error'}
        </div>
      ) : isPending ? (
        <EntityListSkeleton />
      ) : sortedAgents.length === 0 ? (
        <EntityListEmpty
          label="Agents"
          hasFilters={activeFilterCount > 0}
          onClearFilters={
            activeFilterCount > 0
              ? () => {
                  setFilters({
                    agents: [],
                    models: [],
                    tools: [],
                    status: [],
                    sources: [],
                    q: undefined,
                  });
                }
              : undefined
          }
        />
      ) : (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {sortedAgents.map((agent) => (
            <AgentCard key={agent.name} agent={agent} />
          ))}
        </div>
      )}
    </section>
  );
}

function AgentCard({ agent }: { agent: StatAgentRow }) {
  // Per-agent reliability: completed / ended for THIS agent only.
  // The agent row has `sessions` and `failures`; reliability =
  // (sessions - failures) / sessions.
  const reliability =
    agent.sessions > 0 ? (agent.sessions - agent.failures) / agent.sessions : null;
  const reliabilityTone: 'default' | 'failed' | 'success' | undefined =
    reliability === null
      ? 'default'
      : reliability >= 0.9
        ? 'success'
        : reliability >= 0.7
          ? 'default'
          : 'failed';

  return (
    <EntityListCard<StatAgentRow>
      entity={agent}
      href={`/agents/${encodeURIComponent(agent.name)}`}
      primaryLabel={agent.name}
      secondaryLabel={`${agent.pct_of_sessions.toFixed(1)}% of sessions`}
      stats={[
        { label: 'Sessions', value: fmtNum(agent.sessions) },
        { label: 'Cost', value: fmtCost(agent.cost_usd) },
        { label: 'Tokens', value: fmtNum(agent.tokens_in + agent.tokens_out) },
        { label: 'Failures', value: fmtNum(agent.failures), tone: agent.failures > 0 ? 'failed' : 'default' },
      ]}
      reliability={reliability === null ? undefined : { value: reliability, tone: reliabilityTone }}
      lastSeen={undefined}
    />
  );
}

/** SkeletonRowBar renders inside a row while data is loading. */
export function AgentsListSkeletonHeader() {
  return (
    <div>
      <Skeleton className="h-7 w-32" />
      <Skeleton className="mt-2 h-4 w-72" />
    </div>
  );
}
