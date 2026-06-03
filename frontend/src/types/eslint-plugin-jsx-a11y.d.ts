// Ambient declaration for `eslint-plugin-jsx-a11y`, which ships no type
// declarations of its own and has no `@types/...` package (TS7016). Declared to
// the shape `eslint.config.ts` actually consumes: a default export whose
// `flatConfigs.recommended` is an ESLint flat-config object (and `.rules` a
// rule record). Typed via `eslint/config`'s `Config` so the spread is
// assignable inside `defineConfig()`. Narrow on purpose — extend only when the
// config begins using more of the plugin's surface.
declare module 'eslint-plugin-jsx-a11y' {
  import type { Config } from 'eslint/config';

  interface JsxA11yFlatConfig extends Config {
    rules: NonNullable<Config['rules']>;
  }

  const plugin: {
    readonly flatConfigs: {
      readonly recommended: JsxA11yFlatConfig;
      readonly strict: JsxA11yFlatConfig;
    };
  };

  export default plugin;
}
