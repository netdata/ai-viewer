import { QueryClient } from '@tanstack/react-query';

// Single shared QueryClient. SSE invalidation (api/sse.ts) and the React tree
// (main.tsx) operate on the same instance. Defaults: server data is considered
// fresh briefly (SSE drives invalidation, so aggressive refetch is unnecessary)
// and failed queries retry twice.
export function createAppQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        retry: 2,
        refetchOnWindowFocus: false,
      },
    },
  });
}
