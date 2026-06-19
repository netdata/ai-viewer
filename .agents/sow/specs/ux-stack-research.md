# UX/UI Stack Research Notes (2026-06-19)

Online research pass for the ai-viewer visual overhaul. Date-stamped for the record.

## Current frontend stack (read-only)
- React 19.2.7, react-dom 19.2.7
- Vite 8.0.16 (latest minor)
- TypeScript 6.0.3
- react-router-dom 7.18.0
- @tanstack/react-query 5.101.0
- D3 modules (array/force/hierarchy/scale/selection/zoom) — custom viz in `src/viz/`
- CSS Modules + custom tokens in `src/theme/tokens.css`
- Theme system: dark/light via `data-theme` on `<html>` (good — keep)
- Test stack: Vitest 4.1.9, Playwright 1.61.0, jest-axe, axe-core (good — keep)

## Decisions

### Styling: **Tailwind CSS v4.3.1** + `@tailwindcss/vite`
- Current stable: 4.3.1 (published ~1 week ago, weekly downloads ~121M)
- Vite plugin: `@tailwindcss/vite` — official, fastest path, no PostCSS
- CSS-first config via `@theme` directive; no JS config file
- shadcn/ui is fully compatible ("All components are updated for Tailwind v4 and React 19")
- Why over CSS Modules: faster to iterate to a polished result, easier consistency, smaller CSS surface (Tailwind purges), and we need a design-system overhaul anyway
- Migration plan: new components in Tailwind; existing CSS Modules files replaced as we touch each component (no big-bang rewrite of untouched components)

### Component primitives: **shadcn/ui** on **Radix UI primitives**
- shadcn is the most-shipped React component pattern in 2026
- Radix Primitives v1.x (June 2026 release added controlled Context Menu + perf fixes) — full React 19 compatibility
- Why: accessible by default, unstyled, copy-into-project (no runtime dep on a styled lib), Tailwind-based, MIT/ISC
- Install via `npx shadcn@latest init` then `npx shadcn@latest add <component>`
- Rejected: **Mantine** (too opinionated, hard to strip styles), **MUI** (heavy, vendor lock-in), **Chakra v3** (less momentum), **Ant Design** (visual identity too Chinese-B2B), **react-aria-components** (Adobe; technically excellent but less ecosystem and less pre-built patterns)

### Icons: **lucide-react 1.21.0**
- Current stable: 1.21.0 (published 1 day ago)
- 1000+ icons, tree-shakable, ISC-licensed, React 19-compatible
- The standard modern icon set; shadcn ships with it by default
- Rejected: **heroicons** (smaller set), **phosphor** (different style), **react-icons** (mixed quality)

### Animations: **motion v12.x** (formerly framer-motion)
- Current: motion 12.x, framer-motion 12.x — both maintained
- Use `motion/react` import path (the new naming)
- React 19-compatible since v12
- Used by: Vercel, Linear-style micro-interactions, layout transitions, spring physics for charts
- Rejected: **react-spring** (steeper API), **@react-spring/web** (overlap), **auto-animate** (too simple)

### Tables: **@tanstack/react-table 8.21.3**
- Current stable: 8.21.3
- Headless, React 19-compatible (React Compiler has minor issues we won't hit)
- The right primitive for the Sessions table — headless so we control visuals 100%
- v9 is in alpha/beta per the V9 RFC — stay on v8 stable for now
- Rejected: **AG Grid Community** (heavier, opinionated visuals), **Mantine DataTable** (lock-in)

### Charts: **@visx/visx 4.0.0** + existing d3 modules
- visx 4.0.0 just released (8 days ago), full React 19 support
- We keep the existing d3 modules (hierarchy/force/zoom) for the topology viz — visx for line/bar/area on the Stats page
- Rejected: **recharts** (heavier, more opinionated visuals, harder to match a polished design system), **nivo** (similar), **apache-echarts** (canvas, harder to style consistently), **chart.js** (canvas + accessibility concerns)

### Fonts: **Geist Sans + Geist Mono** (Vercel)
- Current: `geist` npm package; Geist Sans + Geist Mono + Geist Pixel
- SIL Open Font License — free, embeddable
- Self-host via `@fontsource/geist-sans` + `@fontsource/geist-mono` (Vite-friendly, no Google Fonts CDN dependency)
- Why: designed for screens, neutral, well-spaced, pairs with our dashboard density
- Rejected: **Inter** (good, but Geist is more current and reads as "now"), **IBM Plex Sans** (more corporate), **SF Pro** (Apple-only legally)

### Date/time: **date-fns** (latest, tree-shakable)
- Current stable; tree-shakable; ESM-friendly
- We already format dates inline; date-fns gives us `formatDistanceToNowStrict`, `format`, etc. without bloat

### State helpers (already installed)
- **@tanstack/react-query 5.101.0** — keep, used everywhere for fetching/SSE
- **react-router-dom 7.18.0** — keep, declarative routing

### Utility helpers to add
- **class-variance-authority (cva)** — shadcn-style variant API for our own components (Button, Card, Badge, etc.)
- **clsx** + **tailwind-merge** — conditional class joining (shadcn ships with both)

## Notes / Risks

- **Tailwind v4 + Vite plugin** requires updating `vite.config.ts` and replacing CSS entry; existing `theme/tokens.css` becomes a `@theme` block in the new CSS entry
- **Radix Primitives** still has occasional React 19 compat chatter on GH but the June 2026 release + shadcn shipping on it confirm production-readiness
- **Bundle size**: must stay under the existing 500 KB main-chunk budget. Tailwind purges; visx imports individual packages; we'll need to verify with `npm run build` after each SOW
- **No runtime effect on backend**; this is frontend-only
- **Migration order**: design tokens first → primitives wrappers (Button, Card, Badge, etc.) → Layout shell → Sessions page (proof) → remaining pages
- **No breaking renames of existing exports** until a component is touched — keeps tests passing during the migration

## What we are NOT adopting (and why)
- **No CSS-in-JS** (styled-components, emotion) — Tailwind v4 is faster, no runtime cost
- **No Material/Chakra/Mantine/AntD** — too opinionated for a "modern, polished, professional" target that doesn't read as any of those brands
- **No Bootstrap/Tailwind UI Kit** — generic look, hard to make distinctive
- **No state management library** (Redux/Zustand/Jotai) — react-query + URL state is sufficient for this app
- **No routing library beyond react-router-dom 7** — already excellent

## Reference product targets (for the designer's eye, not code)
- **Linear** (linear.app) — best-in-class data table, sidebar nav, dark/light parity
- **Vercel** (vercel.com) — clean type, restrained color, motion polish
- **Stripe Dashboard** — information density, structured tables, status systems
- **Datadog APM** — spans, traces, similar data domain to ours
- **Grafana** — observability dashboard conventions

These are mental targets, not source code to copy. The result must be "looks like a modern SaaS", not "looks like Linear specifically".