import { useCallback, useEffect, useState } from 'react';

// pinned — SOW-0087 chunk 4 (A14).
//
// A tiny client-side store for the operator's pinned sessions. Stored
// in localStorage so the pins survive reloads + nav between routes
// (the data layer doesn't track per-operator UI preferences).
//
// Capped at MAX_PINS to keep storage bounded; the oldest pin is evicted
// when over the cap. Pin ids are session ids from /api/sessions.

const STORAGE_KEY = 'ai-viewer.pinned-sessions.v1';
const MAX_PINS = 10;

function read(): string[] {
  if (typeof window === 'undefined') return [];
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw === null) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((v): v is string => typeof v === 'string').slice(0, MAX_PINS);
  } catch {
    return [];
  }
}

function write(ids: readonly string[]): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(ids));
  } catch {
    // Storage disabled or quota exceeded; skip silently — the in-memory
    // state still works for the current session.
  }
}

/** usePinned returns the pinned session IDs + toggle/remove helpers.
 *  Updates localStorage whenever the list changes. */
export function usePinned(): {
  pinned: readonly string[];
  isPinned: (id: string) => boolean;
  toggle: (id: string) => void;
  remove: (id: string) => void;
  clear: () => void;
} {
  const [pinned, setPinned] = useState<readonly string[]>(() => read());

  // Cross-tab sync: if the operator opens a second tab and pins
  // something, the change reflects here.
  useEffect(() => {
    if (typeof window === 'undefined') return undefined;
    const onStorage = (e: StorageEvent): void => {
      if (e.key === STORAGE_KEY) setPinned(read());
    };
    window.addEventListener('storage', onStorage);
    return () => { window.removeEventListener('storage', onStorage); };
  }, []);

  const toggle = useCallback((id: string): void => {
    setPinned((prev) => {
      const next = prev.includes(id) ? prev.filter((x) => x !== id) : [id, ...prev].slice(0, MAX_PINS);
      write(next);
      return next;
    });
  }, []);

  const remove = useCallback((id: string): void => {
    setPinned((prev) => {
      const next = prev.filter((x) => x !== id);
      write(next);
      return next;
    });
  }, []);

  const clear = useCallback((): void => {
    setPinned([]);
    write([]);
  }, []);

  const isPinned = useCallback((id: string): boolean => pinned.includes(id), [pinned]);

  return { pinned, isPinned, toggle, remove, clear };
}
