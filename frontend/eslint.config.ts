import js from '@eslint/js';
import globals from 'globals';
import tseslint from 'typescript-eslint';
import react from 'eslint-plugin-react';
import reactHooks from 'eslint-plugin-react-hooks';

// Flat config (ESLint v9). The repo enforces a zero-warnings policy
// (`eslint . --max-warnings 0`), so every rule below is error-level or off;
// there are deliberately no "warn" levels. Type-aware linting is enabled via
// projectService so unsafe-any / floating-promise classes are caught.
//
// The React and React-Hooks plugins are registered explicitly and their
// recommended rule sets applied in a single project block. Their published
// flat-config *wrapper objects* are NOT spread into tseslint.config() because
// their loosely-typed `plugins` field conflicts with the strict
// ConfigWithExtends type of the config() helper; pulling the rules out avoids
// that friction while keeping identical coverage.
export default tseslint.config(
  {
    // `scripts/` holds standalone Node build-tooling (e.g. the SOW-0012
    // bundle-size gate) that lives OUTSIDE the app's tsconfig project, so the
    // type-checked rule set has no type info for it; it is exercised by its own
    // self-test + actionlint, not by app-source ESLint. Mirrors ignoring the
    // other non-source dirs below.
    ignores: ['dist', 'coverage', 'node_modules', 'playwright-report', 'test-results', 'scripts'],
  },
  js.configs.recommended,
  ...tseslint.configs.recommendedTypeChecked,
  {
    files: ['**/*.{ts,tsx}'],
    plugins: {
      react,
      'react-hooks': reactHooks,
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
      '@typescript-eslint/consistent-type-imports': [
        'error',
        { prefer: 'type-imports', fixStyle: 'inline-type-imports' },
      ],
    },
  },
);
