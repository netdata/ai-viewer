import { Link } from 'react-router-dom';
import styles from './NotFound.module.css';

// 404 fallback for unmatched routes.
export function NotFound() {
  return (
    <section aria-labelledby="notfound-title">
      <h1 id="notfound-title">Not found</h1>
      <p className={styles.note}>
        That page does not exist. <Link to="/">Back to sessions</Link>.
      </p>
    </section>
  );
}
