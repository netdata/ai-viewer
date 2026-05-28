import type { KeyboardEvent } from 'react';
import styles from './Tabs.module.css';

// Accessible tablist (frontend-architecture.md §pages, ui-pages.md §/sessions/:id).
// Controlled: the caller owns the active key (SessionDetail keeps it in the URL).
// This renders only the tab strip (role=tablist + role=tab buttons); the caller
// renders the matching panel. Keeping panel rendering with the caller avoids
// forcing every tab body to mount at once.
//
// Implements the WAI-ARIA tabs pattern (ui-pages.md §Shared UI primitives):
// roving tabindex (selected tab tabIndex=0, others -1) plus ArrowLeft/ArrowRight
// (wrapping) and Home/End keyboard navigation. Each arrow/Home/End selects the
// target tab and moves DOM focus to it, so keyboard users land on the now-active
// tab; Tab/Shift+Tab then move into/out of the strip as a single stop.

export interface TabSpec<K extends string> {
  key: K;
  label: string;
}

export interface TabsProps<K extends string> {
  tabs: ReadonlyArray<TabSpec<K>>;
  active: K;
  onSelect: (key: K) => void;
  ariaLabel: string;
}

export function Tabs<K extends string>({
  tabs,
  active,
  onSelect,
  ariaLabel,
}: TabsProps<K>) {
  const activeIndex = tabs.findIndex((t) => t.key === active);

  // selectIndex selects the tab at `index` (clamped into range) and moves focus
  // to it, so keyboard navigation lands the user on the newly-selected tab even
  // though selection is driven by the controlled parent re-render.
  const selectIndex = (index: number): void => {
    const target = tabs[index];
    if (!target) {
      return;
    }
    onSelect(target.key);
    document.getElementById(`tab-${target.key}`)?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>): void => {
    const count = tabs.length;
    if (count === 0 || activeIndex < 0) {
      return;
    }
    switch (e.key) {
      case 'ArrowRight':
        e.preventDefault();
        selectIndex((activeIndex + 1) % count);
        break;
      case 'ArrowLeft':
        e.preventDefault();
        selectIndex((activeIndex - 1 + count) % count);
        break;
      case 'Home':
        e.preventDefault();
        selectIndex(0);
        break;
      case 'End':
        e.preventDefault();
        selectIndex(count - 1);
        break;
      default:
        break;
    }
  };

  return (
    <div
      className={styles.tabs}
      role="tablist"
      aria-label={ariaLabel}
      onKeyDown={onKeyDown}
    >
      {tabs.map((t) => {
        const selected = t.key === active;
        return (
          <button
            key={t.key}
            type="button"
            role="tab"
            id={`tab-${t.key}`}
            aria-selected={selected}
            aria-controls={`tabpanel-${t.key}`}
            // Roving tabindex: only the selected tab is in the tab order.
            tabIndex={selected ? 0 : -1}
            className={selected ? `${styles.tab} ${styles.tabActive}` : styles.tab}
            onClick={() => {
              onSelect(t.key);
            }}
          >
            {t.label}
          </button>
        );
      })}
    </div>
  );
}
