import { useSearchParams } from 'react-router-dom';
import { applyPatch, useFilters } from '../../state/filters';
import type { SessionStatus } from '../../api/types';
import { useSources } from '../../api/sources';
import styles from './FilterBar.module.css';

// Global filter bar (ui-pages.md §Global Layout). Always visible; every control
// reads from and writes to the URL via useFilters() — there is no local filter
// state. Routes read the same hook to scope their queries.
//
// The Sources filter is a multi-select dropdown populated from /api/sources
// (SOW-0068). The status checkboxes include "failed" with an error_class
// sub-filter badge count. The Time range preset (SOW-0067) writes the
// already-supported from/to params so every page + the SSE subscription honor
// it without per-route wiring. The selected preset is mirrored in a `range`
// URL param so the control reflects the URL purely during render (no Date.now
// in the render path — that runs only in the onChange handler).

const STATUSES: readonly SessionStatus[] = [
  'running',
  'completed',
  'failed',
  'abandoned',
  'interrupted',
];

/**
 * Time-range presets (SOW-0067). Selecting one writes BOTH a `range` mirror
 * param (pure source of truth for the select's displayed value) AND the
 * canonical absolute `from` bound (microseconds, `Date.now()*1000 - duration`)
 * that every endpoint + the SSE subscription read. `to` stays open so live data
 * keeps flowing. 'all' clears both bounds. Durations in microseconds.
 */
const RANGE_PRESETS: ReadonlyArray<{ value: string; label: string; us: number }> = [
  { value: '1h', label: 'Last hour', us: 3_600_000_000 },
  { value: '24h', label: 'Last 24 hours', us: 86_400_000_000 },
  { value: '7d', label: 'Last 7 days', us: 604_800_000_000 },
  { value: '30d', label: 'Last 30 days', us: 2_592_000_000_000 },
];

/** csvToList splits a comma input into a trimmed, non-empty list. */
function csvToList(value: string): string[] {
  return value
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
}

export function FilterBar() {
  const { filters, setFilters, clearFilters } = useFilters();
  const [searchParams, setSearchParams] = useSearchParams();
  const sourcesQuery = useSources();

  const sourceOptions = (sourcesQuery.data?.items ?? []).map((s) => ({
    value: s.id,
    label: s.format,
  }));

  const toggleStatus = (status: SessionStatus, checked: boolean): void => {
    const next = checked
      ? [...filters.status, status]
      : filters.status.filter((s) => s !== status);
    setFilters({ status: next });
  };

  const toggleSource = (sourceID: string): void => {
    const next = filters.sources.includes(sourceID)
      ? filters.sources.filter((s) => s !== sourceID)
      : [...filters.sources, sourceID];
    setFilters({ sources: next });
  };

  // The preset select's value is derived PURELY from the URL: 'all' when there
  // is no time bound, the mirrored `range` param when one is set by this
  // control, else 'custom' (a bound set by another path). Date.now() is NEVER
  // called during render — only in the onChange handler below.
  const rangeValue =
    filters.from === undefined && filters.to === undefined
      ? 'all'
      : (searchParams.get('range') ?? 'custom');

  const applyRangePreset = (value: string): void => {
    if (value === 'custom') return; // no-op: a bound already set outside a preset
    if (value === 'all') {
      setSearchParams(
        (prev) => {
          const next = applyPatch(prev, { from: undefined, to: undefined });
          next.delete('range');
          return next;
        },
        { replace: true },
      );
      return;
    }
    const preset = RANGE_PRESETS.find((p) => p.value === value);
    if (!preset) return;
    const fromUs = Date.now() * 1000 - preset.us; // impure call OK in an event handler
    setSearchParams(
      (prev) => {
        const next = applyPatch(prev, { from: fromUs, to: undefined });
        next.set('range', value);
        return next;
      },
      { replace: true },
    );
  };

  return (
    <div className={styles.bar} role="search" aria-label="Session filters">
      <div className={styles.row}>
        <label className={styles.field}>
          <span className={styles.label}>Search</span>
          <input
            type="search"
            className={styles.input}
            placeholder="agent name…"
            value={filters.q ?? ''}
            onChange={(e) => {
              setFilters({ q: e.target.value });
            }}
          />
        </label>

        <label className={styles.field}>
          <span className={styles.label}>Time range</span>
          <select
            className={styles.input}
            aria-label="Time range preset"
            value={rangeValue}
            onChange={(e) => applyRangePreset(e.target.value)}
          >
            {RANGE_PRESETS.map((p) => (
              <option key={p.value} value={p.value}>
                {p.label}
              </option>
            ))}
            <option value="all">All time</option>
            {rangeValue === 'custom' ? <option value="custom">Custom</option> : null}
          </select>
        </label>

        <label className={styles.field}>
          <span className={styles.label}>Agents</span>
          <input
            type="text"
            className={styles.input}
            placeholder="comma,separated"
            aria-label="Agents filter"
            value={filters.agents.join(',')}
            onChange={(e) => {
              setFilters({ agents: csvToList(e.target.value) });
            }}
          />
        </label>

        <label className={styles.field}>
          <span className={styles.label}>Models</span>
          <input
            type="text"
            className={styles.input}
            placeholder="comma,separated"
            aria-label="Models filter"
            value={filters.models.join(',')}
            onChange={(e) => {
              setFilters({ models: csvToList(e.target.value) });
            }}
          />
        </label>

        <label className={styles.field}>
          <span className={styles.label}>Tools</span>
          <input
            type="text"
            className={styles.input}
            placeholder="comma,separated"
            aria-label="Tools filter"
            value={filters.tools.join(',')}
            onChange={(e) => {
              setFilters({ tools: csvToList(e.target.value) });
            }}
          />
        </label>

        <fieldset className={styles.sourcePicker}>
          <legend className={styles.label}>Sources</legend>
          <div className={styles.sourceChips}>
            {sourceOptions.map((opt) => (
              <button
                key={opt.value}
                type="button"
                className={`${styles.sourceChip} ${filters.sources.includes(opt.value) ? styles.sourceChipActive : ''}`}
                onClick={() => toggleSource(opt.value)}
              >
                {opt.label}
              </button>
            ))}
          </div>
        </fieldset>
      </div>

      <div className={styles.row}>
        <fieldset className={styles.statusSet}>
          <legend className={styles.label}>Status</legend>
          {STATUSES.map((status) => (
            <label key={status} className={styles.checkbox}>
              <input
                type="checkbox"
                checked={filters.status.includes(status)}
                onChange={(e) => {
                  toggleStatus(status, e.target.checked);
                }}
              />
              <span>{status}</span>
            </label>
          ))}
        </fieldset>

        <button
          type="button"
          className={styles.clear}
          onClick={() => {
            clearFilters();
          }}
        >
          Clear filters
        </button>
      </div>
    </div>
  );
}
