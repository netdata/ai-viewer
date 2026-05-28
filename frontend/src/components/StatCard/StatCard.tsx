import type { ReactNode } from 'react';
import styles from './StatCard.module.css';

// Small labeled stat tile used by the Overview tab and analytics summaries.
// Presentational only — the caller formats the value (lib/format.ts).

export interface StatCardProps {
  label: string;
  value: ReactNode;
  /** Optional sub-line (e.g. a delta or share). */
  hint?: ReactNode;
}

export function StatCard({ label, value, hint }: StatCardProps) {
  return (
    <div className={styles.card}>
      <span className={styles.label}>{label}</span>
      <span className={styles.value}>{value}</span>
      {hint !== undefined ? <span className={styles.hint}>{hint}</span> : null}
    </div>
  );
}
