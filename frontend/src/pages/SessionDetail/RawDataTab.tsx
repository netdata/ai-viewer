import { useState } from 'react';
import { Braces, Copy, Check } from 'lucide-react';
import type { SessionDetailResponse } from '../../api/types';
import { Button } from '../../components/ui/button';
import { cn } from '../../lib/utils';

// Raw Data tab (operator feedback #2, 2026-06-15). Renders the full session
// detail response (session row + turns + ops + child sessions) as formatted
// JSON so the operator can inspect exactly what the DB holds for this session.
//
// SOW-0078 dark-theme bugfix: the previous CSS module used --bg-sunken /
// --bg-surface which were never defined in the new design system, so the
// JSON block fell through to the browser default (white text on white
// background in dark mode, "tiny and white on white"). Replaced with
// Tailwind utilities that resolve to the proper dark tokens
// (bg-muted, text-foreground, font-mono, tabular-nums) so the payload
// is readable in BOTH themes with a code-friendly chrome (subtle bg,
// border, monospace, scrollable).

export interface RawDataTabProps {
  detail: SessionDetailResponse;
}

type Section = 'all' | 'session' | 'turns' | 'ops' | 'children';

const SECTION_OPTIONS: readonly { value: Section; label: string; count?: number }[] = [
  { value: 'all', label: 'All (full response)' },
  { value: 'session', label: 'Session row' },
  { value: 'turns', label: 'Turns' },
  { value: 'ops', label: 'Ops' },
  { value: 'children', label: 'Child sessions' },
];

export function RawDataTab({ detail }: RawDataTabProps) {
  const [section, setSection] = useState<Section>('all');
  const [copied, setCopied] = useState(false);

  const ops = detail.turns.flatMap((t) => t.ops);
  const sections: Record<Exclude<Section, 'all'>, unknown> = {
    session: detail.session,
    turns: detail.turns,
    ops,
    children: detail.child_sessions,
  };

  const data: unknown = section === 'all' ? detail : sections[section];
  const json = JSON.stringify(data, null, 2);
  const byteSize = new Blob([json]).size;

  const counts: Record<Exclude<Section, 'all'>, number> = {
    session: 1,
    turns: detail.turns.length,
    ops: ops.length,
    children: detail.child_sessions.length,
  };

  const onCopy = (): void => {
    void (async () => {
      try {
        await navigator.clipboard.writeText(json);
        setCopied(true);
        setTimeout(() => { setCopied(false); }, 1500);
      } catch {
        // Fallback: select the pre block so the user can Ctrl+C.
        const el = document.getElementById('raw-data-pre');
        if (el !== null) {
          const range = document.createRange();
          range.selectNodeContents(el);
          const sel = window.getSelection();
          if (sel !== null) {
            sel.removeAllRanges();
            sel.addRange(range);
          }
        }
      }
    })();
  };

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <label
          htmlFor="raw-data-section"
          className="inline-flex items-center gap-2 text-xs font-medium uppercase tracking-wider text-muted-foreground"
        >
          Section
        </label>
        <select
          id="raw-data-section"
          value={section}
          onChange={(e) => { setSection(e.target.value as Section); }}
          className={cn(
            'rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground',
            'focus:outline-none focus:ring-2 focus:ring-ring',
          )}
        >
          {SECTION_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
              {opt.value !== 'all' && opt.value !== 'session' ? ` (${counts[opt.value]})` : ''}
            </option>
          ))}
        </select>
        <span className="ml-auto inline-flex items-center gap-3 font-mono text-xs tabular-nums text-muted-foreground">
          <span>
            {formatBytes(byteSize)} · {json.split('\n').length} lines
          </span>
        </span>
        <Button
          variant="outline"
          size="sm"
          onClick={onCopy}
          aria-label={copied ? 'Copied to clipboard' : 'Copy JSON to clipboard'}
        >
          {copied ? <Check className="size-3.5" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>

      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Braces className="size-3.5" aria-hidden />
        <span>
          Inspect exactly what the DB holds for this session. The data is
          formatted JSON; use the section selector above to focus on a
          particular table.
        </span>
      </div>

      <pre
        id="raw-data-pre"
        // SOW-0078 dark-theme bugfix: explicit dark-aware text + background so
        // the JSON is readable in BOTH themes. font-mono + text-xs + tabular-nums
        // for compact, monospace-friendly inspection. max-h + overflow-auto so
        // long payloads scroll inside the panel without making the page itself
        // grow unbounded.
        className={cn(
          'max-h-[70vh] overflow-auto rounded-lg border border-border bg-muted/40 p-4',
          'font-mono text-xs leading-relaxed tabular-nums',
          'text-foreground',
          'whitespace-pre-wrap break-words',
        )}
      >
        <code>{json}</code>
      </pre>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(2)} MB`;
}