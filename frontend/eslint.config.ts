import js from '@eslint/js';
import { type Config, defineConfig, globalIgnores } from 'eslint/config';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import react from 'eslint-plugin-react';
import reactHooks from 'eslint-plugin-react-hooks';
import jsxA11y from 'eslint-plugin-jsx-a11y';
import importPlugin from 'eslint-plugin-import';

// eslint-plugin-react-hooks 7.x ships a `configs` shape (`configs.flat.*`) that
// is structurally NOT assignable to ESLint core's strict `Plugin` type used by
// `defineConfig()` (the helper that supersedes the deprecated
// `tseslint.config()`). The plugin works fine at runtime; only its published
// types are too strict to fit. `Plugins` is `Config['plugins']`'s value type,
// reused below to register react-hooks without `any`.
type Plugins = NonNullable<Config['plugins']>;

// Flat config (ESLint v9). The repo enforces a zero-warnings policy
// (`eslint . --max-warnings=0`), so every rule below is error-level or off;
// there are deliberately no "warn" levels. Type-aware linting is enabled via
// projectService so unsafe-any / floating-promise classes are caught.
//
// The config builder is ESLint core's `defineConfig()` (from `eslint/config`)
// rather than `tseslint.config()`: typescript-eslint's helper is `@deprecated`
// in favour of core `defineConfig()` (see typescript-eslint.io/packages/
// typescript-eslint/#config-deprecated), so we use the non-deprecated form and
// keep `tseslint` only for its parser/plugin and shared rule presets.
//
// The React and React-Hooks plugins are registered explicitly and their
// recommended rule sets applied in a single project block. Their published
// flat-config *wrapper objects* are NOT spread into the config builder because
// their loosely-typed `plugins`/`configs` fields conflict with core's strict
// `Plugin` type; pulling the rules out avoids that friction while keeping
// identical coverage.
//
// jsx-a11y and eslint-plugin-import both ship native flat-config support at the
// pinned versions (`jsxA11y.flatConfigs.recommended`,
// `importPlugin.flatConfigs.recommended` / `.typescript`), so no `FlatCompat`
// bridge is needed. The import/typescript preset plus the TypeScript resolver
// (settings below) teach `import/no-unresolved` how to follow `.ts`/`.tsx`
// paths so it does not false-positive on type-only or extensionless imports.
export default defineConfig(
  // `scripts/` holds standalone Node build-tooling (e.g. the SOW-0012
  // bundle-size gate) that lives OUTSIDE the app's tsconfig project, so the
  // type-checked rule set has no type info for it; it is exercised by its own
  // script self-tests/build integration and repository-wide security/spec-drift
  // gates, not by app-source ESLint. `vitest.coverage.mjs` is the same class —
  // a standalone Node config-data module (string arrays) shared by
  // vitest.config.ts and scripts/check-coverage-config.mjs; it carries no app
  // logic, is type-checked via its import in vitest.config.ts, and is not a
  // `.{ts,tsx}` file so the type-aware parserOptions never attach to it (only
  // the type-aware RULES would, and they need type info). `**/*.d.mts` are
  // ambient declaration files with no implementation to lint. The remaining
  // entries are generated/output dirs that must never be linted.
  globalIgnores([
    'dist',
    'coverage',
    'node_modules',
    'playwright-report',
    'test-results',
    'scripts',
    'vitest.coverage.mjs',
    '**/*.d.mts',
  ]),
  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  {
    files: ['**/*.{ts,tsx}'],
    // import/recommended + import/typescript via `extends` keeps the
    // resolver-aware rule set scoped to TS/TSX. The typescript preset disables
    // rules the TS compiler already enforces (e.g. `import/named`) and
    // registers the `.ts`/`.tsx` parser extensions for resolution.
    extends: [importPlugin.flatConfigs.recommended, importPlugin.flatConfigs.typescript],
    plugins: {
      react,
      // Cast: react-hooks 7.x's self-typed `configs` field is not assignable to
      // core's `Plugin` (see the Plugins type note at top); runtime shape is
      // correct. Narrowed to the registry value type, not `any`.
      'react-hooks': reactHooks as Plugins[string],
    },
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser, ...globals.node },
      parserOptions: {
        projectService: true,
        tsconfigRootDir: import.meta.dirname,
      },
    },
    settings: {
      react: { version: 'detect' },
      // Resolve imports through the TypeScript resolver so `import/no-unresolved`
      // follows tsconfig paths and `.ts`/`.tsx`/extensionless specifiers.
      'import/resolver': {
        typescript: { project: import.meta.dirname + '/tsconfig.json' },
        node: true,
      },
    },
    rules: {
      // `react.configs.flat[...]` is typed as possibly-undefined via its index
      // signature; the values exist at runtime, so fall back to {} for TS.
      ...(react.configs.flat.recommended?.rules ?? {}),
      ...(react.configs.flat['jsx-runtime']?.rules ?? {}),
      ...reactHooks.configs.recommended.rules,
      // Types come from TS, not prop-types; automatic runtime needs no import.
      'react/prop-types': 'off',
      'react/react-in-jsx-scope': 'off',
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
      // Codacy parity (SOW-0046): the type/style rules Codacy enforces that
      // were not previously in the local config. Added alongside the matching
      // code fixes so local lint catches the same patterns Codacy reports.
      '@typescript-eslint/array-type': 'error',
      '@typescript-eslint/no-confusing-void-expression': 'error',
      '@typescript-eslint/no-unnecessary-condition': 'error',
      '@typescript-eslint/non-nullable-type-assertion-style': 'error',
      '@typescript-eslint/no-unnecessary-type-arguments': 'error',
      '@typescript-eslint/no-unnecessary-type-assertion': 'error',
      '@typescript-eslint/no-empty-function': 'error',
      '@typescript-eslint/no-invalid-void-type': 'error',
      '@typescript-eslint/consistent-type-imports': [
        'error',
        { prefer: 'type-imports', fixStyle: 'inline-type-imports' },
      ],
      // import/recommended ships these three as 'warn'; under the zero-warnings
      // gate a warning fails the build, so promote them to 'error' to make the
      // intent explicit (no "passing build with warnings" ambiguity).
      'import/no-named-as-default': 'error',
      'import/no-named-as-default-member': 'error',
      'import/no-duplicates': 'error',
    },
  },
  // jsx-a11y's recommended flat config, scoped to the files that contain JSX.
  // Registered after the shared TS/React block so its rules layer cleanly. The
  // one rule override lives in THIS object because a flat-config rule must be
  // set in a config that also registers its plugin (jsx-a11y is registered by
  // the spread).
  {
    ...jsxA11y.flatConfigs.recommended,
    files: ['**/*.tsx'],
    rules: {
      ...jsxA11y.flatConfigs.recommended.rules,
      // Scrollable `role="region"` containers carry `tabIndex={0}` so keyboard-
      // only users can focus and arrow-scroll an overflow area (WAI-ARIA APG
      // scrollable-region pattern). Extend the rule's default allow-list with
      // `region` while preserving recommended's other options (`tags: []`,
      // `allowExpressionValues: true`); the rule stays active everywhere else.
      'jsx-a11y/no-noninteractive-tabindex': [
        'error',
        { tags: [], roles: ['tabpanel', 'region'], allowExpressionValues: true },
      ],
    },
  },
  // The Codacy-parity type/style rules above are scoped to PRODUCT SOURCE.
  // Test files (.test/.spec) use legitimate mock patterns these strict rules
  // flag as noise — empty stub methods (canvas setters, Worker.postMessage),
  // `as` casts on mocks, shorthand `() => mock()` arrows, defensive `??` — so
  // they are turned off for tests. This matches the Codacy review scope (the
  // source-file findings SOW-0046 addresses) and avoids low-value churn across
  // every test mock; the rules still fully cover product source.
  {
    files: ['**/*.test.{ts,tsx}', '**/*.spec.{ts,tsx}'],
    rules: {
      '@typescript-eslint/array-type': 'off',
      '@typescript-eslint/no-confusing-void-expression': 'off',
      '@typescript-eslint/no-unnecessary-condition': 'off',
      '@typescript-eslint/non-nullable-type-assertion-style': 'off',
      '@typescript-eslint/no-unnecessary-type-arguments': 'off',
      '@typescript-eslint/no-unnecessary-type-assertion': 'off',
      '@typescript-eslint/no-empty-function': 'off',
      '@typescript-eslint/no-invalid-void-type': 'off',
    },
  },
  // This flat-config file is itself in the lint set. It legitimately consumes
  // plugin objects that ship no (or `any`-typed) declarations — both
  // `eslint-plugin-import`'s `flatConfigs` and `eslint-plugin-jsx-a11y`'s
  // `flatConfigs` are typed `any`, so accessing/spreading them is unsafe
  // member-access / argument / assignment — and `tseslint.configs` trips the
  // import plugin's named-vs-default-member heuristic (a false positive:
  // `tseslint` exposes `configs` as a real namespace member). These rules are
  // off for THIS FILE ONLY; every app-source file keeps full coverage. This
  // block is last so it overrides the type-checked + import blocks above for
  // the config file.
  {
    files: ['eslint.config.ts'],
    rules: {
      '@typescript-eslint/no-unsafe-argument': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
      '@typescript-eslint/no-unsafe-assignment': 'off',
      'import/no-named-as-default-member': 'off',
    },
  },
);
