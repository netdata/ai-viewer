import { Link } from 'react-router-dom';

// 404 fallback for unmatched routes.
export function NotFound() {
  return (
    <section aria-labelledby="notfound-title">
      <h1 id="notfound-title">Not found</h1>
      <p style={{ color: 'var(--text-secondary)' }}>
        That page does not exist. <Link to="/">Back to sessions</Link>.
      </p>
    </section>
  );
}
