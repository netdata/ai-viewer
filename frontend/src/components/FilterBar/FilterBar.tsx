import { useFilters } from '../../state/filters';
import type { SessionStatus } from '../../api/types';
import { useSources } from '../../api/sources';
import styles from './FilterBar.module.css';

// Global filter bar (ui-pages.md §Global Layout). Always visible; every control
// reads from and writes to the URL via useFilters() — there is no local filter
// state. Routes read the same hook to scope their queries.
//
// The Sources filter is a multi-select dropdown populated from /api/sources
// (SOW-0068). The status checkboxes include "failed" with an error_class
// sub-filter badge count.

const STATUSES: readonly SessionStatus[] = [
  'running',
  'completed',
  'failed',
  'abandoned',
  'interrupted',
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
