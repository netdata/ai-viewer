import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { SubscriptionFilterRequest } from '../api/types';

// Filters live in the URL so every view is shareable (ui-pages.md §Global
// Layout, frontend-architecture.md §State Management — "No filter state in
// components"). useFilters() reads the current filter set from the React Router
// search params and returns a setter that writes back to the URL.
//
// Array dimensions (agents/models/tools/status/sources) are serialized as a
// single comma-joined param (`?agents=a,b`), which the Go list endpoints accept
// (rest-api.md §Conventions: "comma-separated"). Time bounds are UNIX
// microseconds (from/to). q is free-text agent-name search.

/** The array-valued filter keys, serialized comma-joined in the URL. */
export const ARRAY_FILTER_KEYS = [
  'agents',
  'models',
  'tools',
  'status',
  'sources',
] as const;

export type ArrayFilterKey = (typeof ARRAY_FILTER_KEYS)[number];

/** Decoded, component-facing filter state. */
export interface Filters {
  agents: string[];
  models: string[];
  tools: string[];
  status: string[];
  sources: string[];
  /** UNIX microseconds, inclusive lower bound. undefined = no constraint. */
  from?: number;
  /** UNIX microseconds, exclusive/now upper bound. undefined = open-ended. */
  to?: number;
  /** Free-text search over agent name. undefined = no search. */
  q?: string;
}

/**
 * A partial patch applied to the current filters. Every field is optional AND
 * may be explicitly `undefined`/empty to CLEAR that dimension — passing
 * `{ from: undefined }` or `{ agents: [] }` deletes the param. (Plain
 * `Partial<Filters>` would forbid the explicit-undefined value under
 * exactOptionalPropertyTypes, which is exactly the clear gesture we need.)
 */
export interface FilterPatch {
  agents?: string[];
  models?: string[];
  tools?: string[];
  status?: string[];
  sources?: string[];
  from?: number | undefined;
  to?: number | undefined;
  q?: string | undefined;
}

/** parseList splits a comma-joined param into a trimmed, non-empty list. */
const parseList = (raw: string | null): string[] => {
  if (raw === null || raw === '') {
    return [];
  }
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
};

/**
 * parseNumber strictly parses an integer microsecond param, or undefined when
 * the param is absent or not a clean integer. Unlike Number.parseInt it does
 * NOT truncate `123abc` -> 123 or `1.2` -> 1; the backend expects an exact
 * integer microsecond value (rest-api.md §Conventions), so anything that is
 * not a whole, safe integer is ignored rather than silently coerced.
 */
const parseNumber = (raw: string | null): number | undefined => {
  if (raw === null || !/^-?\d+$/.test(raw)) {
    return undefined;
  }
  const n = Number(raw);
  return Number.isSafeInteger(n) ? n : undefined;
};

/** readFilters decodes a URLSearchParams into the typed Filters shape. */
export function readFilters(params: URLSearchParams): Filters {
  const filters: Filters = {
    agents: parseList(params.get('agents')),
    models: parseList(params.get('models')),
    tools: parseList(params.get('tools')),
    status: parseList(params.get('status')),
    sources: parseList(params.get('sources')),
  };
  const from = parseNumber(params.get('from'));
  if (from !== undefined) {
    filters.from = from;
  }
  const to = parseNumber(params.get('to'));
  if (to !== undefined) {
    filters.to = to;
  }
  const q = params.get('q');
  if (q !== null && q.trim().length > 0) {
    filters.q = q;
  }
  return filters;
}

interface BuiltSubscriptionTimeRange {
  from?: number;
  to?: number;
}

const arrayPatchValue = (
  patch: FilterPatch,
  key: ArrayFilterKey,
): string[] | undefined => {
  switch (key) {
    case 'agents':
      return patch.agents;
    case 'models':
      return patch.models;
    case 'tools':
      return patch.tools;
    case 'status':
      return patch.status;
    case 'sources':
      return patch.sources;
  }

  const exhaustive: never = key;
  return exhaustive;
};

const filterArrayValue = (filters: Filters, key: ArrayFilterKey): string[] => {
  switch (key) {
    case 'agents':
      return filters.agents;
    case 'models':
      return filters.models;
    case 'tools':
      return filters.tools;
    case 'status':
      return filters.status;
    case 'sources':
      return filters.sources;
  }

  const exhaustive: never = key;
  return exhaustive;
};

const applyArrayPatches = (
  params: URLSearchParams,
  patch: FilterPatch,
): void => {
  for (const key of ARRAY_FILTER_KEYS) {
    applyArrayPatch(params, key, arrayPatchValue(patch, key));
  }
};

const applyArrayPatch = (
  params: URLSearchParams,
  key: ArrayFilterKey,
  value: string[] | undefined,
): void => {
  if (value === undefined) {
    return;
  }
  if (value.length === 0) {
    params.delete(key);
  } else {
    params.set(key, value.join(','));
  }
};

const setOrDeleteNumber = (
  params: URLSearchParams,
  key: 'from' | 'to',
  value: number | undefined,
): void => {
  if (value === undefined) {
    params.delete(key);
  } else {
    params.set(key, String(value));
  }
};

const setOrDeleteText = (
  params: URLSearchParams,
  key: 'q',
  value: string | undefined,
): void => {
  if (value === undefined || value.trim().length === 0) {
    params.delete(key);
  } else {
    params.set(key, value);
  }
};

const applyScalarPatches = (
  params: URLSearchParams,
  patch: FilterPatch,
): void => {
  if ('from' in patch) {
    setOrDeleteNumber(params, 'from', patch.from);
  }
  if ('to' in patch) {
    setOrDeleteNumber(params, 'to', patch.to);
  }
  if ('q' in patch) {
    setOrDeleteText(params, 'q', patch.q);
  }
};

const addSubscriptionArrays = (
  sub: SubscriptionFilterRequest,
  filters: Filters,
): void => {
  for (const key of ARRAY_FILTER_KEYS) {
    addSubscriptionArray(sub, key, filterArrayValue(filters, key));
  }
};

const addSubscriptionArray = (
  sub: SubscriptionFilterRequest,
  key: ArrayFilterKey,
  value: string[],
): void => {
  if (value.length === 0) {
    return;
  }

  switch (key) {
    case 'agents':
      sub.agents = value;
      return;
    case 'models':
      sub.models = value;
      return;
    case 'tools':
      sub.tools = value;
      return;
    case 'status':
      sub.status = value;
      return;
    case 'sources':
      sub.sources = value;
      return;
  }

  const exhaustive: never = key;
  return exhaustive;
};

const subscriptionTimeRange = (
  filters: Filters,
): BuiltSubscriptionTimeRange | undefined => {
  const from = filters.from;
  const to = filters.to;

  if (from === undefined && to === undefined) {
    return undefined;
  }

  const range: BuiltSubscriptionTimeRange = {};
  if (from !== undefined) {
    range.from = from;
  }
  if (to !== undefined) {
    range.to = to;
  }
  return range;
};

const addSubscriptionTimeRange = (
  sub: SubscriptionFilterRequest,
  filters: Filters,
): void => {
  const timeRange = subscriptionTimeRange(filters);
  if (timeRange !== undefined) {
    sub.time_range = timeRange;
  }
};

/**
 * applyPatch produces the next URLSearchParams from the current params and a
 * patch. Each provided key is written; empty arrays / undefined scalars delete
 * the param so the URL stays clean (an absent key means "no constraint", which
 * is exactly the REST contract). Keys absent from the patch are left untouched.
 */
export function applyPatch(
  current: URLSearchParams,
  patch: FilterPatch,
): URLSearchParams {
  const next = new URLSearchParams(current);
  applyArrayPatches(next, patch);
  applyScalarPatches(next, patch);
  return next;
}

/**
 * filtersToSubscription maps the URL-synced Filters into the SSE subscription
 * filter shape (POST /api/subscriptions — rest-api.md). Only non-empty
 * dimensions are included; empty arrays / undefined scalars are omitted (an
 * absent key = no constraint, and the server rejects a present-but-empty array).
 * `q` has no subscription equivalent (the SSE filter has no free-text field),
 * so it is intentionally dropped — the live stream is scoped by the structured
 * dimensions; the list query still applies `q` on refetch.
 */
export function filtersToSubscription(filters: Filters): SubscriptionFilterRequest {
  const sub: SubscriptionFilterRequest = {};
  addSubscriptionArrays(sub, filters);
  addSubscriptionTimeRange(sub, filters);
  return sub;
}

export interface UseFiltersResult {
  filters: Filters;
  /** Merge a patch into the URL search params (pushes a history entry). */
  setFilters: (_patch: FilterPatch) => void;
  /** Clear every filter, leaving any non-filter params intact. */
  clearFilters: () => void;
}

/**
 * useFilters is the single source of truth for the active filter set. It reads
 * from the URL and writes back through React Router's setSearchParams, so a
 * filter change updates the address bar (and thus is shareable / back-button
 * friendly). Components never hold filter state of their own.
 */
export function useFilters(): UseFiltersResult {
  const [searchParams, setSearchParams] = useSearchParams();

  const filters = useMemo(() => readFilters(searchParams), [searchParams]);

  const setFilters = useCallback(
    (patch: FilterPatch): void => {
      setSearchParams((prev) => applyPatch(prev, patch));
    },
    [setSearchParams],
  );

  const clearFilters = useCallback((): void => {
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      for (const key of ARRAY_FILTER_KEYS) {
        next.delete(key);
      }
      next.delete('from');
      next.delete('to');
      next.delete('q');
      // The FilterBar time-range preset mirrors its choice in a `range` param
      // (SOW-0067); clear it too so "Clear filters" leaves a clean URL and the
      // preset select does not display a value that no longer applies.
      next.delete('range');
      return next;
    });
  }, [setSearchParams]);

  return { filters, setFilters, clearFilters };
}
