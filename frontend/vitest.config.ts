import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

// Vitest config kept separate from vite.config.ts so the production build
// never pulls in test-only globals/jsdom. Coverage is scoped to the source
// dirs implemented in this chunk; placeholder pages and Phase-2 stubs are
// excluded so the gate measures real, exercised code (AGENTS.md gate:
// >= 80% lines on implemented component/lib dirs).
export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: true,
    // Unit tests are co-located under src/; the Playwright E2E specs live in
    // tests/ and must NOT be swept up by vitest's default glob (they use the
    // @playwright/test runner, not vitest). Scope discovery to src/ only.
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'text-summary', 'html'],
      include: [
        'src/state/**/*.ts',
        'src/lib/**/*.ts',
        'src/api/client.ts',
        'src/api/sse.ts',
        'src/api/sessions.ts',
        'src/api/stats.ts',
        'src/api/sources.ts',
        'src/api/logs.ts',
        'src/components/FilterBar/**/*.{ts,tsx}',
        'src/components/SessionRow/**/*.{ts,tsx}',
        'src/components/ThemeToggle/**/*.{ts,tsx}',
        'src/components/Tabs/**/*.{ts,tsx}',
        'src/components/LogRow/**/*.{ts,tsx}',
        'src/components/LoadMore/**/*.{ts,tsx}',
        'src/components/StatusViews/**/*.{ts,tsx}',
        'src/components/SpanDetailDrawer/**/*.{ts,tsx}',
        'src/viz/**/*.{ts,tsx}',
        'src/pages/SessionsList/**/*.{ts,tsx}',
        'src/pages/SessionDetail/**/*.{ts,tsx}',
        'src/pages/Sources/**/*.{ts,tsx}',
      ],
      thresholds: {
        lines: 80,
        statements: 80,
        functions: 80,
        branches: 75,
      },
    },
  },
});
