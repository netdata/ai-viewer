import { useTheme, type ThemePreference } from '../../state/theme';
import styles from './ThemeToggle.module.css';

// Three-state theme control (frontend-architecture.md §User control): Auto /
// Dark / Light. Auto follows the OS; explicit choices persist to localStorage.
// The control is a segmented set of buttons; the active one is marked
// aria-pressed and each button carries a STATIC aria-label describing what it
// does. The *resolved* theme is announced through a dedicated visually-hidden
// aria-live="polite" region (frontend-architecture.md §Accessibility) — never
// by mutating a control's label on every OS-preference flip, which re-announces
// the whole control and is noisy for screen-reader users.

const OPTIONS: ReadonlyArray<{ value: ThemePreference; label: string; symbol: string }> = [
  { value: 'auto', label: 'Auto (follow system)', symbol: 'A' },
  { value: 'dark', label: 'Dark', symbol: '◑' }, // ◑ — monochrome, theme-agnostic
  { value: 'light', label: 'Light', symbol: '○' }, // ○
];

export function ThemeToggle() {
  const { preference, resolved, setPreference } = useTheme();
  return (
    <div className={styles.group} role="group" aria-label="Theme">
      {OPTIONS.map((opt) => (
        <button
          key={opt.value}
          type="button"
          className={styles.button}
          aria-pressed={preference === opt.value}
          aria-label={opt.label}
          title={opt.label}
          onClick={() => {
            setPreference(opt.value);
          }}
        >
          <span aria-hidden="true">{opt.symbol}</span>
        </button>
      ))}
      {/* Polite live region announces only the resolved theme when it changes;
          visually hidden so it does not affect layout. */}
      <span role="status" aria-live="polite" className={styles.srOnly}>
        {`Theme: ${resolved}`}
      </span>
    </div>
  );
}
