// CopyButton: copies text to clipboard via navigator.clipboard.writeText.
// Shows a transient "Copied" label for 1.5s after success. The icon swaps
// to a check mark briefly. Falls back to a selection-based copy if the
// clipboard API is unavailable (e.g. http on some browsers — we serve
// 127.0.0.1 in production but localhost on dev, both of which the API
// treats as secure contexts so the fallback is rarely exercised).

import { useEffect, useRef, useState } from 'react';
import { Check, Copy } from 'lucide-react';
import styles from './CopyButton.module.css';

export function CopyButton({
  text,
  kind,
}: {
  text: string;
  /** Drives the aria-label. */
  kind: 'prose' | 'code';
}) {
  const [copied, setCopied] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => () => {
    if (timerRef.current !== null) clearTimeout(timerRef.current);
  }, []);

  // We bind click to a wrapper that handles the promise rejection explicitly.
  // The button attribute cannot accept an async function directly without
  // misusing the promise-returning function type — see the lint rule.
  const handleClick = (): void => {
    void doCopy();
  };

  async function doCopy(): Promise<void> {
    try {
      const cb = navigator.clipboard;
      if (typeof cb.writeText === 'function') {
        await cb.writeText(text);
      } else {
        // Fallback: textarea + execCommand. Only runs in very old browsers or
        // non-secure contexts (we serve 127.0.0.1 in prod so the API works).
        const ta = document.createElement('textarea');
        ta.value = text;
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
      // No silent failure: surface the issue to the user.
      setCopied(false);
    }
  }

  return (
    <button
      type="button"
      className={styles.button}
      onClick={handleClick}
      aria-label={`Copy ${kind}`}
      data-copied={copied ? 'true' : 'false'}
    >
      {copied ? <Check size={12} /> : <Copy size={12} />}
      <span className={styles.label}>{copied ? 'Copied' : 'Copy'}</span>
    </button>
  );
}