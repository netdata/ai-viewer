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
//   - The budget for each MAIN/LAZY entry is measured against the gzip sum of the
//     entry's own file PLUS the TRANSITIVE CLOSURE of its STATIC `imports` (follow
//     manifest[key].imports recursively; each imported key contributes its `.file`).
//     This matters because a Rollup-split SHARED chunk is neither isEntry nor
//     isDynamicEntry — it is reachable only via an entry's `imports` array — yet the
//     browser TRANSFERS it together with the entry when that route loads. Budgeting
//     the entry's `.file` alone would let a small lazy route that statically imports
//     a huge shared chunk pass (the chunk would only show up as "ungated"), a
//     fail-open. Files are DE-DUPLICATED within a single entry's closure (a shared
//     chunk reached by two import edges counts once); each entry's closure is summed
//     independently (a chunk shared by two entries is budgeted under each — that is
//     the real per-route transfer cost). `dynamicImports` are NOT followed: those
//     point at separately-budgeted LAZY chunks the browser does not fetch on the
//     entry's initial load.
//   - Every JS file under <distDir>/assets/ is MEASURED for the report. A file that is
//     neither classified as an entry NOR pulled into any entry's static-import closure
//     (a `?worker` bundle such as forceWorker-*.js instantiated via `new Worker()` and
//     absent from the manifest, or a chunk only ever reached via dynamicImports) is
//     reported as "ungated" — the spec's two budgets are defined only for HTML-entry
//     and route-lazy chunks (and what they statically pull in). A budget for
//     worker-only chunks is a separate SOW.
//   - gzipped sizes use Node's zlib.gzipSync at its default level — the same level a
//     CDN/`gzip -c` uses for the size report; the budget is about transferred bytes.
//
// No silent failures (fail-closed): a missing/empty distDir, a missing or invalid
// .vite/manifest.json (including a JSON array rather than an object), a manifest that
// classifies ZERO JS chunks, a manifest with NO main (isEntry) chunk at all (this SPA
// always emits exactly one — its absence is a broken build), or a manifest entry
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
  // closed rather than skip it (skipping could vacuously pass the gate). Memoized
  // so a chunk in several entries' closures is read+gzipped once.
  const gzipCache = new Map(); // relFile -> gz bytes
  function gzipOf(relFile) {
    const cached = gzipCache.get(relFile);
    if (cached !== undefined) {
      return cached;
    }
    const abs = path.join(distDir, relFile);
    if (!fs.existsSync(abs)) {
      fatal(`manifest references a file that is absent on disk: ${relFile}\n        (stale or incomplete build under ${distDir})`);
    }
    const gz = zlib.gzipSync(fs.readFileSync(abs)).length;
    gzipCache.set(relFile, gz);
    return gz;
  }

  // A well-formed manifest object value. Vite's ManifestChunk has a string `file`;
  // `imports`/`dynamicImports` are optional string arrays of OTHER manifest KEYS.
  function isChunk(v) {
    return v !== null && typeof v === 'object' && typeof v.file === 'string';
  }

  // Transitive closure of an entry's STATIC imports, returned as the set of
  // distinct chunk FILES the browser transfers with that entry. We walk
  // manifest[key].imports recursively (each element is another manifest KEY),
  // de-duplicating by manifest key so a diamond import graph (shared chunk reached
  // via two paths) is visited once. dynamicImports are deliberately NOT walked —
  // they are separately-budgeted LAZY chunks, not part of this entry's initial
  // transfer. A non-.js chunk in the closure (CSS) is skipped (the budget is the
  // JS budget). Returns { files: Set<relFile>, gzip: total bytes }.
  function staticClosure(rootKey) {
    const seenKeys = new Set();
    const files = new Set();
    const stack = [rootKey];
    while (stack.length > 0) {
      const key = stack.pop();
      if (seenKeys.has(key)) {
        continue;
      }
      seenKeys.add(key);
      const entry = manifest[key];
      if (!isChunk(entry)) {
        // A manifest referencing an import key that is missing/!chunk is a broken
        // build (Vite always emits the imported chunk's entry). Fail closed rather
        // than silently undercount the closure.
        fatal(
          `manifest import graph references a missing/invalid chunk key: ${JSON.stringify(key)}\n` +
            `        (reachable from entry ${JSON.stringify(rootKey)} in ${manifestPath})`,
        );
      }
      if (entry.file.endsWith('.js')) {
        files.add(entry.file);
      }
      if (Array.isArray(entry.imports)) {
        for (const imp of entry.imports) {
          if (typeof imp === 'string') {
            stack.push(imp);
          }
        }
      }
    }
    let gzip = 0;
    for (const f of files) {
      gzip += gzipOf(f);
    }
    return { files, gzip };
  }

  // Classify JS chunks from the manifest. `file` paths are relative to outDir
  // (e.g. "assets/index-*.js"); only .js chunks carry a size budget. Each
  // MAIN/LAZY entry's budget is its transitive static-import closure (entry.file
  // + everything its `imports` reach), so a fat shared chunk a small entry pulls
  // in is gated, not swept into "ungated".
  const mainChunks = []; // { file, gzip, files: Set }  (isEntry, closure)
  const lazyChunks = []; // { file, gzip, files: Set }  (isDynamicEntry, closure)
  const coveredFiles = new Set(); // every file inside ANY gated entry's closure
  for (const key of Object.keys(manifest)) {
    const entry = manifest[key];
    if (!isChunk(entry)) {
      continue;
    }
    if (!entry.file.endsWith('.js')) {
      continue; // CSS / asset entries are not part of the JS budget.
    }
    const isMain = entry.isEntry === true;
    const isLazy = entry.isDynamicEntry === true;
    if (!isMain && !isLazy) {
      // Non-entry, non-dynamic-entry JS chunks (shared splits) are accounted for
      // via the closures of the entries that import them, not classified here.
      continue;
    }
    const closure = staticClosure(key);
    const chunk = { file: entry.file, gzip: closure.gzip, files: closure.files };
    (isMain ? mainChunks : lazyChunks).push(chunk);
    for (const f of closure.files) {
      coveredFiles.add(f);
    }
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

  // This SPA always emits exactly one HTML entry (index.html -> isEntry). A
  // manifest that classifies LAZY chunks but no MAIN entry is a broken/partial
  // build (the HTML entry vanished); certifying a pass off the lazy chunks alone
  // would be a fail-open. Fail closed.
  if (mainChunks.length === 0) {
    fatal(
      `manifest classifies no MAIN (isEntry) chunk: ${manifestPath}\n` +
        `        This SPA must emit exactly one HTML entry — a build without it is invalid.`,
    );
  }

  // Sweep every JS file actually on disk under assets/ so the report covers
  // chunks NOT inside any gated entry's closure (Vite's ?worker bundles are
  // emitted but NOT listed in the manifest; a chunk reached only via
  // dynamicImports without also being a dynamic-entry would land here too). These
  // are reported as "ungated" — visibility without a budget. A file pulled into a
  // closure is NOT re-counted here (it is already gated under its entry).
  const ungated = []; // { file, gzip }
  const assetsDir = path.join(distDir, 'assets');
  if (fs.existsSync(assetsDir) && fs.statSync(assetsDir).isDirectory()) {
    for (const name of fs.readdirSync(assetsDir)) {
      if (!name.endsWith('.js')) {
        continue;
      }
      const rel = path.posix.join('assets', name);
      if (coveredFiles.has(rel)) {
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
      // For gated entries c.gzip is the transitive static-import closure total;
      // note the import count so the number is legible (entry file + N imports).
      const nImports = c.files ? c.files.size - 1 : 0;
      const closureNote = nImports > 0 ? ` ${GRAY}(entry +${nImports} import${nImports === 1 ? '' : 's'})${NC}` : '';
      process.stdout.write(`  [${label}] ${tag}  ${fmtKB(c.gzip).padStart(10)} gz${limit}  ${c.file}${closureNote}\n`);
      if (over) {
        const detail = nImports > 0 ? ` (entry + ${nImports} static import${nImports === 1 ? '' : 's'})` : '';
        violations.push(`${c.file} (${label})${detail} is ${fmtKB(c.gzip)} gz, over the ${fmtKB(budget)} budget by ${fmtKB(c.gzip - budget)}`);
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
