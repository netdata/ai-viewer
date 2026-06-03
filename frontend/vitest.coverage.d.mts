// Type declarations for vitest.coverage.mjs so vitest.config.ts imports the
// shared coverage lists type-checked under the project's strict tsconfig
// (moduleResolution: bundler, verbatimModuleSyntax). Only SHAPES are declared
// here; the data itself lives solely in vitest.coverage.mjs, so there is no value
// to drift between the two files (SOW-0012 review F3).
export declare const PER_DIR_LINES: number;
export declare const COVERAGE_INCLUDE: readonly string[];
export declare const PER_DIR_GLOBS: readonly string[];
