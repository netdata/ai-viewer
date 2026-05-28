import styles from './ComingSoon.module.css';

// Shared placeholder for Phase-2/3 routes scaffolded but not yet implemented
// (ui-pages.md §Phase Mapping). Renders a title + note so the route is
// navigable and obviously a stub rather than a broken page.

export function ComingSoon({ title, note }: { title: string; note?: string }) {
  return (
    <section aria-labelledby="coming-soon-title">
      <h1 id="coming-soon-title">{title}</h1>
      <p className={styles.note}>{note ?? 'This view is planned for a later phase.'}</p>
    </section>
  );
}
