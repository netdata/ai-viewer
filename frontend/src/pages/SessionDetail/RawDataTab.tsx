import { useState } from 'react';
import type { SessionDetailResponse } from '../../api/types';
import styles from './RawDataTab.module.css';

// Raw Data tab (operator feedback #2, 2026-06-15: "it would be nice to have a
// tab per session to show a dump of everything we have in the db, because I
// currently don't know what is there really"). Renders the full session detail
// response (session row + turns + ops + child sessions) as formatted JSON so
// the operator can inspect exactly what the DB holds for this session — a
// diagnostic aid for verifying data integrity.

export interface RawDataTabProps {
  detail: SessionDetailResponse;
}

export function RawDataTab({ detail }: RawDataTabProps) {
  const [section, setSection] = useState<'all' | 'session' | 'turns' | 'ops' | 'children'>('all');

  const ops = detail.turns.flatMap((t) => t.ops ?? []);
  const sections: Record<string, unknown> = {
    session: detail.session,
    turns: detail.turns,
    ops,
    children: detail.child_sessions ?? [],
  };

  const data = section === 'all' ? detail : sections[section];

  return (
    <div className={styles.container}>
      <div className={styles.toolbar}>
        <span className={styles.label}>Section:</span>
        <select
          value={section}
          onChange={(e) => setSection(e.target.value as typeof section)}
          className={styles.select}
        >
          <option value="all">All (full response)</option>
          <option value="session">Session row</option>
          <option value="turns">Turns ({detail.turns.length})</option>
          <option value="ops">Ops ({ops.length})</option>
          <option value="children">Child sessions ({detail.child_sessions?.length ?? 0})</option>
        </select>
      </div>
      <pre className={styles.json}>
        <code>{JSON.stringify(data, null, 2)}</code>
      </pre>
    </div>
  );
}
