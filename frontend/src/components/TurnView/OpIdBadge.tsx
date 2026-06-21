// OpIdBadge (SOW-0090 chunk 8): a compact, click-to-copy op id in the
// step header. Renders the first 8 chars of the id in monospace, with the
// full id in a title (tooltip) for hover-confirmation. Clicking the badge
// copies the full 64-char id to the clipboard. The badge is intentionally
// minimal — the operator wants to scan and share op ids fast.

import { Check, Copy, Hash } from 'lucide-react';
import { useEffect, useRef, useState } from 'react';
import { shortOpId } from './stepMeta';
import styles from './TurnStep.module.css';

export function OpIdBadge({ opId }: { opId: string }) {
  const [copied, setCopied] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (timerRef.current !== null) clearTimeout(timerRef.current);
  }, []);

  const handleClick = (): void => {
    void doCopy();
  };

  async function doCopy(): Promise<void> {
    try {
      const cb = navigator.clipboard;
      if (typeof cb.writeText === 'function') {
        await cb.writeText(opId);
      } else {
        const ta = document.createElement('textarea');
        ta.value = opId;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      setCopied(true);
      if (timerRef.current !== null) clearTimeout(timerRef.current);
      timerRef.current = setTimeout(() => {
        setCopied(false);
      }, 1500);
    } catch {
      setCopied(false);
    }
  }

  const short = shortOpId(opId);

  return (
    <button
      type="button"
      className={styles.opIdBadge}
      onClick={handleClick}
      aria-label={`Copy op id ${opId}`}
      title={opId}
      data-copied={copied ? 'true' : 'false'}
    >
      <Hash size={10} aria-hidden="true" />
      {copied ? <Check size={10} aria-hidden="true" /> : <Copy size={10} aria-hidden="true" />}
      <code className={styles.opIdShort}>{short}</code>
    </button>
  );
}
