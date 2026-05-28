import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

// Deterministic in-memory localStorage. Node 22+ ships an experimental global
// `localStorage` that requires the `--localstorage-file` flag and otherwise
// shadows jsdom's implementation (warning: "localStorage is not available").
// We install our own Storage-compatible shim so theme persistence tests are
// stable regardless of the Node version's web-storage state.
class MemoryStorage implements Storage {
  private map = new Map<string, string>();
  get length(): number {
    return this.map.size;
  }
  clear(): void {
    this.map.clear();
  }
  getItem(key: string): string | null {
    return this.map.has(key) ? (this.map.get(key) ?? null) : null;
  }
  key(index: number): string | null {
    return Array.from(this.map.keys())[index] ?? null;
  }
  removeItem(key: string): void {
    this.map.delete(key);
  }
  setItem(key: string, value: string): void {
    this.map.set(key, String(value));
  }
}

const memoryStorage = new MemoryStorage();
Object.defineProperty(window, 'localStorage', {
  configurable: true,
  value: memoryStorage,
});
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: memoryStorage,
});

// Global test setup: jest-dom matchers + automatic DOM cleanup between tests so
// each render starts from a clean document.
afterEach(() => {
  cleanup();
});
