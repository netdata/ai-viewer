import { useMemo } from 'react';
import { useStats } from '../../api/stats';
import { EntityListCard, EntityListSkeleton, EntityListEmpty, fmtCost, fmtNum } from '../../components/EntityList';
import { EntitySummaryStrip, EntitySummaryStripSkeleton } from '../../components/EntitySummaryStrip';
import { useFilters } from '../../state/filters';
import type { StatToolRow } from '../../api/types';

// ToolsList — /tools (SOW-0081)
//
// Lists every distinct tool in the current filter window. Each card
// shows the tool namespace + name + calls + failures, drilling to
// /tools/:name on click. Same pattern as /agents and /models.

const EMPTY_TOOLS: readonly StatToolRow[] = Object.freeze([]);

export function ToolsList() {
  const { filters, setFilters } = useFilters();
  const { data, isPending, isError, error } = useStats(filters);

  const totals = data?.totals;
  const rawTools = data?.by_tool;
  const tools = useMemo(() => rawTools ?? EMPTY_TOOLS, [rawTools]);

  const sortedTools = useMemo(
    () => [...tools].sort((a, b) => b.calls - a.calls),
    [tools],
  );

  const totalCalls = useMemo(
    () => tools.reduce((sum, t) => sum + t.calls, 0),
    [tools],
  );
  const totalFailures = useMemo(
    () => tools.reduce((sum, t) => sum + t.failures, 0),
    [tools],
  );

  const summaryTiles = useMemo(
    () => [
      { label: 'Tools', value: tools.length, format: 'number' as const },
      { label: 'Total calls', value: totalCalls, format: 'number' as const },
      {
        label: 'Failures',
        value: totalFailures,
        format: 'number' as const,
        tone: totalFailures > 0 ? ('failed' as const) : ('default' as const),
      },
      {
        label: 'Sessions',
        value: totals ? totals.session_count : (null as number | null),
        format: 'number' as const,
      },
    ],
    [tools.length, totalCalls, totalFailures, totals],
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
    <section aria-labelledby="tools-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="tools-title" className="text-2xl font-semibold tracking-tight">
          Tools
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Every distinct tool called across your sessions, ranked by usage.
        </p>
      </div>

      {isPending ? (
        <EntitySummaryStripSkeleton />
      ) : (
        <EntitySummaryStrip tiles={summaryTiles} />
      )}

      {isError ? (
        <div className="rounded-lg border border-border bg-card p-12 text-sm text-status-failed">
          Failed to load tools: {error instanceof Error ? error.message : 'unknown error'}
        </div>
      ) : isPending ? (
        <EntityListSkeleton />
      ) : sortedTools.length === 0 ? (
        <EntityListEmpty
          label="Tools"
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
          {sortedTools.map((tool) => (
            <ToolCard key={`${tool.namespace}::${tool.name}`} tool={tool} />
          ))}
        </div>
      )}
    </section>
  );
}

function ToolCard({ tool }: { tool: StatToolRow }) {
  const reliability =
    tool.calls > 0 ? (tool.calls - tool.failures) / tool.calls : null;
  const reliabilityTone: 'default' | 'failed' | 'success' | undefined =
    reliability === null
      ? 'default'
      : reliability >= 0.99
        ? 'success'
        : reliability >= 0.95
          ? 'default'
          : 'failed';

  return (
    <EntityListCard<StatToolRow>
      entity={tool}
      href={`/tools/${encodeURIComponent(`${tool.namespace}::${tool.name}`)}`}
      primaryLabel={tool.name}
      secondaryLabel={`${tool.namespace} · ${tool.pct_of_calls.toFixed(1)}% of calls`}
      stats={[
        { label: 'Calls', value: fmtNum(tool.calls) },
        { label: 'Failures', value: fmtNum(tool.failures), tone: tool.failures > 0 ? 'failed' : 'default' },
        { label: 'Avg ms', value: tool.calls > 0 ? fmtNum(Math.round(tool.total_us / 1000 / tool.calls)) : '—' },
        { label: 'Total time', value: tool.total_us > 0 ? `${(tool.total_us / 1_000_000).toFixed(1)}s` : '—' },
      ]}
      reliability={reliability === null ? undefined : { value: reliability, tone: reliabilityTone }}
      lastSeen={undefined}
    />
  );
}

// keep fmtCost import live for future tool-cost tiles (none yet — tool
// stats don't expose cost_usd on the /stats endpoint).
void fmtCost;
