import { useFilters } from '../../state/filters';
import styles from './SessionsList.module.css';

// Sessions list page (ui-pages.md §"/"). Chunk 14 ships a clean placeholder
// that already consumes the URL-synced filters; Chunk 15 makes it live with a
// minimal, additive change:
//
//   const { filters } = useFilters();
//   const { data, isPending, error } = useSessions(filters, 'root');
//   // render <table> of <SessionRow session={item}/> for data.items
//   // useEffect(() => { const c = await connectSse(qc, filterToSub(filters), …)
//   //                   return () => c.close(); }, [filters])
//
// The structure below is deliberately shaped so that swap is local: the page
// already reads filters and owns the content region; only the body changes.

export function SessionsList() {
  const { filters } = useFilters();
  const activeCount =
    filters.agents.length +
    filters.models.length +
    filters.tools.length +
    filters.status.length +
    filters.sources.length +
    (filters.from !== undefined ? 1 : 0) +
    (filters.to !== undefined ? 1 : 0) +
    (filters.q !== undefined ? 1 : 0);

  return (
    <section aria-labelledby="sessions-title">
      <h1 id="sessions-title">Sessions</h1>
      <p className={styles.note}>
        Live session list lands in the next chunk. Filters are wired:{' '}
        <strong>{activeCount}</strong> active.
      </p>
    </section>
  );
}
