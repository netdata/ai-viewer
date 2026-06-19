# SOW-0072 — ESLint 10 ecosystem upgrade

## Status

Status: open (pending operator decision — major-version upgrade)

## Requirements

### Purpose

Upgrade the frontend lint stack from ESLint 9 to ESLint 10. ESLint 10 is a
breaking major version that changes the context API (removed `getFilename`,
`getRuleContext`, etc.), which `eslint-plugin-react` and likely
`eslint-plugin-react-hooks` depend on. The upgrade therefore cascades across
the plugin ecosystem, not just the eslint core.

### Background

Dependabot opened PRs #45 (eslint 9→10) and #46 (@eslint/js 9→10). Local
testing confirmed the breakage: `eslint-plugin-react` fails at load time
(`contextOrFilename.getFilename is not a function`). The plugins need upgrading
to eslint-10-compatible versions first, which may change rule behavior or
require config migration.

### Scope

- Upgrade `eslint` 9.39.4 → 10.x
- Upgrade `@eslint/js` 9.39.4 → 10.x
- Upgrade `eslint-plugin-react` to an eslint-10-compatible version
- Upgrade `eslint-plugin-react-hooks` to an eslint-10-compatible version
- Verify `typescript-eslint` 8.61.1 compatibility with eslint 10
- Run the full lint suite; fix any new rule violations
- Update `eslint.config.ts` if the flat-config API changed

### Acceptance Criteria

1. `npx eslint src/` passes with zero warnings under eslint 10.
2. All 683+ frontend tests pass.
3. CI `lint` job green.

## Pre-Implementation Gate / Implementation / Validation / Reviews / Outcome

(Empty placeholders.)
