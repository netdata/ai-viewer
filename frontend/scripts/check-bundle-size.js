#!/usr/bin/env node
// Enforce the frontend bundle-size budget on the gzipped output of `vite build`.
//
// Authoritative gate: .agents/sow/specs/quality-gates.md "Frontend — Bundle Size".
//
// Policy (SOW-0012):
//   - Chunk classification is MANIFEST-DRIVEN, not filename-heuristic. vite.config.ts
//     sets `build.manifest: true`, so Vite emits <distDir>/.vite/manifest.json with a
//     per-chunk `isEntry` / `isDynamicEntry` flag (Vite's documented ManifestChunk
//     contract). We classify from those flags so the gate survives any future
//     code-splitting without a fragile `index-*.js` name match.
//       * isEntry        -> MAIN chunk        -> budget MAIN_MAX_GZIP  (500 KB gz)
//       * isDynamicEntry  -> per-route LAZY chunk -> budget LAZY_MAX_GZIP (200 KB gz)
//   - Every JS file under <distDir>/assets/ is MEASURED for the report. A file that
//     the manifest does NOT classify (a `?worker` bundle such as forceWorker-*.js
//     that is instantiated via `new Worker()` and is absent from the manifest, or a
//     non-entry shared chunk Rollup split out) is reported as "ungated" — the spec's
//     two budgets are defined only for HTML-entry and route-lazy chunks. A budget for
//     worker/shared chunks is a separate SOW.
//   - gzipped sizes use Node's zlib.gzipSync at its default level — the same level a
//     CDN/`gzip -c` uses for the size report; the budget is about transferred bytes.
//
// No silent failures (fail-closed): a missing/empty distDir, a missing or invalid
// .vite/manifest.json, a manifest that classifies ZERO JS chunks, or a manifest entry
// whose file is absent on disk each EXIT NON-ZERO. The gate never certifies "within
// budget" without measuring real, classified chunks.
//
// Usage:  node scripts/check-bundle-size.js [distDir]   (default: <script>/../dist)
// Exit:   0 = every gated chunk within budget; 1 = a budget violation;
//         2 = usage / fail-closed input error (missing dist, bad manifest, ...).

// ESM module: frontend/package.json declares "type": "module", so this .js file
// is loaded as ESM. Built-ins are imported; no third-party dependency is used.
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// Budgets are the contract from quality-gates.md, expressed in BYTES of gzipped
// output. Named constants — never inline a magic number, never loosen to pass.
const KB = 1024;
const MAIN_MAX_GZIP = 500 * KB; // HTML entry chunk (isEntry)
const LAZY_MAX_GZIP = 200 * KB; // per-route lazy chunk (isDynamicEntry)

// ANSI colors for a transparent, readable report (matches the repo's bash gates).
const RED = '\x1b[0;31m';
const GREEN = '\x1b[0;32m';
const YELLOW = '\x1b[1;33m';
const GRAY = '\x1b[0;90m';
const NC = '\x1b[0m';

/** Print an error and exit fail-closed (code 2 — never a silent pass). */
function fatal(msg) {
  process.stderr.write(`${RED}[ERROR]${NC} ${msg}\n`);
  process.exit(2);
}

function fmtKB(bytes) {
  return `${(bytes / KB).toFixed(1)} KB`;
}

function main() {
  // Default to the real build output relative to THIS script, so the gate works
  // from any CWD; the optional arg lets the self-test point at a fixture dir.
  const distDir = process.argv[2]
    ? path.resolve(process.argv[2])
    : path.resolve(__dirname, '..', 'dist');

  if (!fs.existsSync(distDir) || !fs.statSync(distDir).isDirectory()) {
    fatal(
      `dist directory not found: ${distDir}\n` +
        `        Build first: (cd frontend && npm run build), then re-run the gate.`,
    );
  }

  // Read the Vite manifest. Its absence is fail-closed: without it we cannot
  // classify entry vs dynamic-entry chunks, and a heuristic guess would be a
  // silent downgrade of the gate.
  const manifestPath = path.join(distDir, '.vite', 'manifest.json');
  if (!fs.existsSync(manifestPath)) {
    fatal(
      `Vite manifest not found: ${manifestPath}\n` +
        `        Set build.manifest: true in vite.config.ts so chunks can be classified.`,
    );
  }
  let manifest;
  try {
    manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  } catch (err) {
    fatal(`Vite manifest is not valid JSON: ${manifestPath}\n        ${err.message}`);
  }
  if (manifest === null || typeof manifest !== 'object' || Array.isArray(manifest)) {
    fatal(`Vite manifest is not a JSON object: ${manifestPath}`);
  }

  // Gzipped size of one chunk file, resolved relative to distDir. A manifest
  // entry pointing at a file absent on disk is a stale/broken build — fail
  // closed rather than skip it (skipping could vacuously pass the gate).
  function gzipOf(relFile) {
    const abs = path.join(distDir, relFile);
    if (!fs.existsSync(abs)) {
      fatal(`manifest references a file that is absent on disk: ${relFile}\n        (stale or incomplete build under ${distDir})`);
    }
    return zlib.gzipSync(fs.readFileSync(abs)).length;
  }

  // Classify JS chunks from the manifest. `file` paths are relative to outDir
  // (e.g. "assets/index-*.js"); only .js chunks carry a size budget.
  const mainChunks = []; // { file, gzip }  (isEntry)
  const lazyChunks = []; // { file, gzip }  (isDynamicEntry)
  const classifiedFiles = new Set();
  for (const key of Object.keys(manifest)) {
    const entry = manifest[key];
    if (entry === null || typeof entry !== 'object' || typeof entry.file !== 'string') {
      continue;
    }
    if (!entry.file.endsWith('.js')) {
      continue; // CSS / asset entries are not part of the JS budget.
    }
    if (entry.isEntry === true) {
      mainChunks.push({ file: entry.file, gzip: gzipOf(entry.file) });
      classifiedFiles.add(entry.file);
    } else if (entry.isDynamicEntry === true) {
      lazyChunks.push({ file: entry.file, gzip: gzipOf(entry.file) });
      classifiedFiles.add(entry.file);
    }
    // Non-entry, non-dynamic-entry JS chunks (shared splits) are reported in the
    // "ungated" sweep below alongside worker bundles, so they need no marking here.
  }

  // A manifest that classifies ZERO gated JS chunks must never certify a pass —
  // there would be nothing to gate (empty/`{}` manifest, or a build that emitted
  // no entry). Fail closed.
  if (mainChunks.length === 0 && lazyChunks.length === 0) {
    fatal(
      `manifest classifies no JS entry/dynamic-entry chunks: ${manifestPath}\n` +
        `        Nothing to gate — refusing to pass vacuously.`,
    );
  }

  // Sweep every JS file actually on disk under assets/ so the report covers
  // chunks the manifest omits (Vite's ?worker bundles are emitted but NOT listed
  // in the manifest; non-entry shared chunks are listed only indirectly). These
  // are reported as "ungated" — visibility without a budget.
  const ungated = []; // { file, gzip }
  const assetsDir = path.join(distDir, 'assets');
  if (fs.existsSync(assetsDir) && fs.statSync(assetsDir).isDirectory()) {
    for (const name of fs.readdirSync(assetsDir)) {
      if (!name.endsWith('.js')) {
        continue;
      }
      const rel = path.posix.join('assets', name);
      if (classifiedFiles.has(rel)) {
        continue;
      }
      ungated.push({ file: rel, gzip: zlib.gzipSync(fs.readFileSync(path.join(assetsDir, name))).length });
    }
  }

  // --- Report + gate ---------------------------------------------------------
  process.stdout.write(`${GRAY}bundle-size gate — dist: ${distDir}${NC}\n`);
  const violations = [];

  const report = (label, chunks, budget) => {
    for (const c of chunks.sort((a, b) => b.gzip - a.gzip)) {
      const over = budget !== null && c.gzip > budget;
      const tag = over ? `${RED}FAIL${NC}` : `${GREEN}ok${NC}`;
      const limit = budget !== null ? ` / ${fmtKB(budget)}` : '';
      process.stdout.write(`  [${label}] ${tag}  ${fmtKB(c.gzip).padStart(10)} gz${limit}  ${c.file}\n`);
      if (over) {
        violations.push(`${c.file} (${label}) is ${fmtKB(c.gzip)} gz, over the ${fmtKB(budget)} budget by ${fmtKB(c.gzip - budget)}`);
      }
    }
  };

  report('main', mainChunks, MAIN_MAX_GZIP);
  report('lazy', lazyChunks, LAZY_MAX_GZIP);
  if (ungated.length > 0) {
    report('ungt', ungated, null); // worker / shared chunks: reported, not gated
  }

  if (violations.length > 0) {
    process.stderr.write(`${RED}BUNDLE SIZE GATE: FAIL${NC}\n`);
    for (const v of violations) {
      process.stderr.write(`  ${v}\n`);
    }
    process.stderr.write(
      `${GRAY}Budgets (gzipped): main isEntry <= ${fmtKB(MAIN_MAX_GZIP)}, per-route lazy isDynamicEntry <= ${fmtKB(LAZY_MAX_GZIP)}.\n` +
        `If a larger chunk is genuinely required, open a SOW with justification — do not raise the threshold.${NC}\n`,
    );
    process.exit(1);
  }

  const lazyNote = lazyChunks.length === 0 ? ` ${YELLOW}(no route-lazy chunks yet)${NC}` : '';
  process.stdout.write(
    `${GREEN}BUNDLE SIZE GATE: PASS${NC} (main <= ${fmtKB(MAIN_MAX_GZIP)} gz, lazy <= ${fmtKB(LAZY_MAX_GZIP)} gz)${lazyNote}\n`,
  );
}

main();
