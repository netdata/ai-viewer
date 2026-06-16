import { NavLink, Outlet } from 'react-router-dom';
import { ThemeToggle } from '../ThemeToggle';
import { FilterBar } from '../FilterBar';
import { LiveIndicator } from '../LiveIndicator/LiveIndicator';
import { filtersToSubscription } from '../../state/filters';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import styles from './Layout.module.css';

// App shell (ui-pages.md §Global Layout): header with brand + primary nav +
// theme control, then the always-visible global FilterBar, then the routed
// content via <Outlet/>. The FilterBar applies to every page; routes interpret
// the URL-synced filters themselves.

const NAV: ReadonlyArray<{ to: string; label: string }> = [
  { to: '/', label: 'Sessions' },
  { to: '/topology', label: 'Topology' },
  { to: '/stats', label: 'Statistics' },
  { to: '/sources', label: 'Sources' },
];

export function Layout() {
  // One SSE subscription for the active filter; keeps the live indicator + all
  // pages' queries fresh. The indicator reflects the SSE connection state.
  const sseStatus = useLiveUpdates(filtersToSubscription({ agents: [], models: [], tools: [], sources: [], status: [], q: '' }));

  return (
    <div className={styles.shell}>
      <header className={styles.header}>
        <div className={styles.brandRow}>
          <span className={styles.brand}>ai-viewer</span>
          <nav className={styles.nav} aria-label="Primary">
            {NAV.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.to === '/'}
                className={({ isActive }) =>
                  isActive ? `${styles.navLink} ${styles.navLinkActive}` : styles.navLink
                }
              >
                {item.label}
              </NavLink>
            ))}
          </nav>
          <div className={styles.headerActions}>
            <LiveIndicator status={sseStatus} />
            <ThemeToggle />
          </div>
        </div>
        <FilterBar />
      </header>
      <main className={styles.content}>
        <Outlet />
      </main>
    </div>
  );
}
