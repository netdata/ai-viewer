import { useMemo } from 'react';
import { useStats } from '../../api/stats';
import { EntityListCard, EntityListSkeleton, EntityListEmpty, fmtCost, fmtNum } from '../../components/EntityList';
import { EntitySummaryStrip, EntitySummaryStripSkeleton } from '../../components/EntitySummaryStrip';
import { useFilters } from '../../state/filters';
import { Skeleton } from '../../components/ui/skeleton';
import type { StatModelRow } from '../../api/types';

// ModelsList — /models (SOW-0081)
//
// Lists every distinct model in the current filter window. Each card
// shows the model name + provider + cost + sessions, drilling to
// /models/:name on click. Same pattern as /agents.

const EMPTY_MODELS: readonly StatModelRow[] = Object.freeze([]);

export function ModelsList() {
  const { filters, setFilters } = useFilters();
  const { data, isPending, isError, error } = useStats(filters);

  const totals = data?.totals;
  const rawModels = data?.by_model;
  const models = useMemo(() => rawModels ?? EMPTY_MODELS, [rawModels]);

  const sortedModels = useMemo(
    () => [...models].sort((a, b) => b.cost_usd - a.cost_usd),
    [models],
  );

  const fleetReliability = useMemo(() => {
    if (!totals) return null;
    const denom = totals.session_count - totals.failures;
    if (totals.session_count <= 0 || denom <= 0) return null;
    return denom / totals.session_count;
  }, [totals]);

  const summaryTiles = useMemo(
    () => [
      { label: 'Models', value: models.length, format: 'number' as const },
      {
        label: 'Sessions',
        value: totals ? totals.session_count : (null as number | null),
        format: 'number' as const,
      },
      {
        label: 'Total cost',
        value: totals ? totals.cost_usd : (null as number | null),
        format: 'cost' as const,
      },
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
    ],
    [models.length, totals, fleetReliability],
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
    <section aria-labelledby="models-title" className="flex flex-col gap-6 px-6 py-5">
      <div>
        <h1 id="models-title" className="text-2xl font-semibold tracking-tight">
          Models
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Every distinct model in your sessions, ranked by cost.
        </p>
      </div>

      {isPending ? (
        <EntitySummaryStripSkeleton />
      ) : (
        <EntitySummaryStrip tiles={summaryTiles} />
      )}

      {isError ? (
        <div className="rounded-lg border border-border bg-card p-12 text-sm text-status-failed">
          Failed to load models: {error instanceof Error ? error.message : 'unknown error'}
        </div>
      ) : isPending ? (
        <EntityListSkeleton />
      ) : sortedModels.length === 0 ? (
        <EntityListEmpty
          label="Models"
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
          {sortedModels.map((model) => (
            <ModelCard key={`${model.provider}::${model.name}`} model={model} />
          ))}
        </div>
      )}
    </section>
  );
}

function ModelCard({ model }: { model: StatModelRow }) {
  const reliability =
    model.calls > 0 ? (model.calls - model.failures) / model.calls : null;
  const reliabilityTone: 'default' | 'failed' | 'success' | undefined =
    reliability === null
      ? 'default'
      : reliability >= 0.9
        ? 'success'
        : reliability >= 0.7
          ? 'default'
          : 'failed';

  return (
    <EntityListCard<StatModelRow>
      entity={model}
      href={`/models/${encodeURIComponent(model.name)}`}
      primaryLabel={model.name}
      secondaryLabel={`${model.provider} · ${model.pct_of_cost.toFixed(1)}% of cost`}
      stats={[
        { label: 'Calls', value: fmtNum(model.calls) },
        { label: 'Cost', value: fmtCost(model.cost_usd) },
        { label: 'Tokens', value: fmtNum(model.tokens_in + model.tokens_out) },
        { label: 'Failures', value: fmtNum(model.failures), tone: model.failures > 0 ? 'failed' : 'default' },
      ]}
      reliability={reliability === null ? undefined : { value: reliability, tone: reliabilityTone }}
      lastSeen={undefined}
    />
  );
}

// silence unused-import warning for Skeleton (kept for future use)
void Skeleton;
