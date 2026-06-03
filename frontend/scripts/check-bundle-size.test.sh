#!/usr/bin/env bash
# Self-test for frontend/scripts/check-bundle-size.js. Builds synthetic dist
# fixtures (a hand-written .vite/manifest.json + JS chunk files) and asserts the
# gate FAILS when a manifest entry's TRANSITIVE-import-closure gz total exceeds
# the 500 KB gz main budget or a dynamic-entry's closure exceeds the 200 KB gz
# lazy budget, PASSES when every gated entry is within budget, and FAILS CLOSED
# on a missing/empty dist, a missing or invalid manifest (object OR array), a
# manifest with zero JS chunks, a manifest with no MAIN (isEntry) chunk at all, a
# manifest whose entry `imports` a key that is absent from the manifest (a broken
# static-import graph), a manifest whose entry `imports` array holds a NON-STRING
# element (a ManifestChunk-contract violation), and a manifest entry whose
# `imports`/`dynamicImports` is PRESENT BUT NOT AN ARRAY (also a contract
# violation — silently ignoring it would undercount the closure). The gate is
# itself code; it must be correct.
#
# gzip shrinks low-entropy bytes (zeros, repeats) to almost nothing, so a budget
# expressed in GZIPPED bytes can only be exercised with HIGH-ENTROPY
# (incompressible) content. Every fixture chunk is filled with cryptographically
# random RAW bytes, which gzip cannot shrink (gzipped size == raw size within
# ~0.1%), so a fixture asked for N KB gzips to ~N KB — mirroring how the Go
# aiagent_v2 benchmark builds high-entropy corpora. (base64 text would still be
# ~20% compressible and undershoot the budget.)
#
# Run: frontend/scripts/check-bundle-size.test.sh   (exit 0 = all assertions pass)
set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; NC='\033[0m'
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CHECK="${SCRIPT_DIR}/check-bundle-size.js"
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
pass=0; fail=0

if ! command -v node >/dev/null 2>&1; then
  echo -e "${RED}[ERROR]${NC} node not found on PATH — required to run the bundle-size gate self-test." >&2
  exit 2
fi

# mkchunk <file> <gz-KB>
# Writes a chunk of N KB of incompressible (cryptographically random raw) bytes,
# so its GZIPPED size is ~N KB too. Node drives the randomness so the test has no
# dependency on /dev/urandom byte semantics across platforms.
mkchunk() {
  local file="$1" kb="$2"
  node -e '
    const fs = require("fs"), crypto = require("crypto");
    const [file, kb] = [process.argv[1], Number(process.argv[2])];
    // Raw random bytes are maximally incompressible: gzip output == input size
    // within ~0.1%, so N KB of them gzips to ~N KB. The over-budget fixtures
    // (520, 230) thus clear their thresholds (500, 200) with a ~20 KB margin.
    fs.writeFileSync(file, crypto.randomBytes(kb * 1024));
  ' "$file" "$kb"
}

# assert <want-exit> <distDir> <desc>
assert() {
  local want="$1" dist="$2" desc="$3" got=0
  node "$CHECK" "$dist" >"$TMP/out" 2>&1 || got=$?
  if [ "$got" -eq "$want" ]; then
    echo -e "  ${GREEN}PASS${NC} (${desc}): exit ${got}"; pass=$((pass+1))
  else
    echo -e "  ${RED}FAIL${NC} (${desc}): want exit ${want}, got ${got}"; sed 's/^/      /' "$TMP/out"; fail=$((fail+1))
  fi
}

# --- (a) main (isEntry) chunk over the 500 KB gz budget -> FAIL ---------------
A="$TMP/a/dist"; mkdir -p "$A/assets" "$A/.vite"
mkchunk "$A/assets/index-AAAA.js" 520            # ~520 KB gz > 500 KB main budget
cat > "$A/.vite/manifest.json" <<'JSON'
{ "index.html": { "file": "assets/index-AAAA.js", "name": "index", "src": "index.html", "isEntry": true } }
JSON
assert 1 "$A" "main chunk 520 KB gz > 500 KB budget"

# --- (b) all chunks within budget -> PASS -------------------------------------
# main entry small, one dynamic-entry lazy chunk under 200 KB, plus an UNGATED
# worker-style chunk that is on disk but absent from the manifest (mirrors how
# Vite emits ?worker bundles): it must be reported, never gate.
B="$TMP/b/dist"; mkdir -p "$B/assets" "$B/.vite"
mkchunk "$B/assets/index-BBBB.js"  120           # main, under 500
mkchunk "$B/assets/route-CCCC.js"  150           # lazy, under 200
mkchunk "$B/assets/worker-DDDD.js"  90           # ungated (not in manifest)
cat > "$B/.vite/manifest.json" <<'JSON'
{
  "index.html":      { "file": "assets/index-BBBB.js", "name": "index", "src": "index.html", "isEntry": true },
  "src/Route.tsx":   { "file": "assets/route-CCCC.js", "name": "Route", "src": "src/Route.tsx", "isDynamicEntry": true }
}
JSON
assert 0 "$B" "all gated chunks within budget (+ ungated worker chunk reported)"

# --- (c) a dynamic-entry (lazy) chunk over the 200 KB gz budget -> FAIL --------
C="$TMP/c/dist"; mkdir -p "$C/assets" "$C/.vite"
mkchunk "$C/assets/index-EEEE.js"  120           # main fine
mkchunk "$C/assets/route-FFFF.js"  230           # ~230 KB gz > 200 KB lazy budget
cat > "$C/.vite/manifest.json" <<'JSON'
{
  "index.html":    { "file": "assets/index-EEEE.js", "name": "index", "src": "index.html", "isEntry": true },
  "src/Route.tsx": { "file": "assets/route-FFFF.js", "name": "Route", "src": "src/Route.tsx", "isDynamicEntry": true }
}
JSON
assert 1 "$C" "lazy chunk 230 KB gz > 200 KB budget"

# --- (c2) lazy entry SMALL on its own but its STATIC import is huge -> FAIL ----
# The route chunk's OWN file is 50 KB gz (under the 200 KB lazy budget), but it
# STATICALLY imports a 230 KB gz shared chunk. The browser transfers BOTH when
# the route loads, so the gate must budget the transitive closure (entry + its
# static imports), not the entry file alone. Without closure accounting this
# fixture would PASS (50 < 200) and a fat shared dependency would slip through —
# the F1 fail-open this case pins shut. The shared chunk is itself neither an
# entry nor a dynamic-entry (a Rollup-split SHARED chunk), reachable only via the
# route entry's `imports` array.
I="$TMP/i/dist"; mkdir -p "$I/assets" "$I/.vite"
mkchunk "$I/assets/index-IIII.js"   120          # main, under 500
mkchunk "$I/assets/route-JJJJ.js"    50          # lazy OWN file under 200...
mkchunk "$I/assets/shared-KKKK.js"  230          # ...but it imports this 230 KB shared chunk
cat > "$I/.vite/manifest.json" <<'JSON'
{
  "index.html":    { "file": "assets/index-IIII.js",  "name": "index",  "src": "index.html",  "isEntry": true },
  "src/Route.tsx": { "file": "assets/route-JJJJ.js",   "name": "Route",  "src": "src/Route.tsx", "isDynamicEntry": true, "imports": ["_shared-KKKK.js"] },
  "_shared-KKKK.js": { "file": "assets/shared-KKKK.js", "name": "shared" }
}
JSON
assert 1 "$I" "lazy entry+static-import closure 50+230 KB gz > 200 KB budget"

# --- (c3) main entry SMALL on its own but a SHARED import double-counted once --
# Two entries (main + lazy) both statically import the SAME shared chunk. The gate
# must de-duplicate files WITHIN a single entry's closure (a shared chunk reached
# by two import edges from one entry counts once), but each entry budgets its own
# closure independently. Here the main entry imports one shared chunk twice (via
# two intermediate chunks), and the closure must total main(120)+shared(300)=420
# < 500 -> PASS, proving de-dup (without it the 300 would be counted twice = 600
# > 500 and falsely FAIL).
L="$TMP/l/dist"; mkdir -p "$L/assets" "$L/.vite"
mkchunk "$L/assets/index-LLLL.js"  120           # main own file
mkchunk "$L/assets/mid1-MMMM.js"    20           # intermediate 1 (imports shared)
mkchunk "$L/assets/mid2-NNNN.js"    20           # intermediate 2 (imports shared)
mkchunk "$L/assets/shared-OOOO.js" 300           # shared, reached via BOTH intermediates
cat > "$L/.vite/manifest.json" <<'JSON'
{
  "index.html": { "file": "assets/index-LLLL.js", "name": "index", "src": "index.html", "isEntry": true, "imports": ["_mid1-MMMM.js", "_mid2-NNNN.js"] },
  "_mid1-MMMM.js": { "file": "assets/mid1-MMMM.js", "name": "mid1", "imports": ["_shared-OOOO.js"] },
  "_mid2-NNNN.js": { "file": "assets/mid2-NNNN.js", "name": "mid2", "imports": ["_shared-OOOO.js"] },
  "_shared-OOOO.js": { "file": "assets/shared-OOOO.js", "name": "shared" }
}
JSON
assert 0 "$L" "main closure de-dups a doubly-imported shared chunk (120+20+20+300 < 500)"

# --- (c4) dynamicImports are NOT followed into the main closure ---------------
# A main entry that DYNAMICALLY imports a huge lazy route must NOT fold that route
# into its own budget (the lazy chunk is separately budgeted, and the browser does
# not transfer it on initial load). Main own file 120, its dynamicImports points
# at a 230 KB lazy chunk that is independently within ITS 200... no, 230 > 200, so
# to isolate the "dynamicImports not followed into MAIN" behavior we keep the lazy
# chunk under its own 200 budget (150) and make the MAIN tiny: if dynamicImports
# were (wrongly) followed, main would be 120+150=270 < 500 and still pass, which
# would not distinguish. So instead: main own 480, lazy 150. Main alone 480 < 500
# PASS; lazy alone 150 < 200 PASS. If dynamicImports were folded into main, main
# would be 480+150=630 > 500 -> FAIL. Expecting PASS proves they are NOT followed.
P="$TMP/p/dist"; mkdir -p "$P/assets" "$P/.vite"
mkchunk "$P/assets/index-PPPP.js"  480           # main own file, just under 500
mkchunk "$P/assets/route-QQQQ.js"  150           # dynamically-imported lazy route, under 200
cat > "$P/.vite/manifest.json" <<'JSON'
{
  "index.html":    { "file": "assets/index-PPPP.js", "name": "index", "src": "index.html", "isEntry": true, "dynamicImports": ["src/Route.tsx"] },
  "src/Route.tsx": { "file": "assets/route-QQQQ.js",  "name": "Route", "src": "src/Route.tsx", "isDynamicEntry": true }
}
JSON
assert 0 "$P" "main does NOT fold its dynamicImports into its own budget (main 480<500, lazy 150<200)"

# --- (d1) dist dir missing entirely -> FAIL CLOSED ----------------------------
assert 2 "$TMP/does-not-exist" "missing dist dir"

# --- (d2) dist present but empty (no manifest, no assets) -> FAIL CLOSED -------
E="$TMP/e/dist"; mkdir -p "$E"
assert 2 "$E" "empty dist (no manifest)"

# --- (d3) manifest present but lists ZERO JS chunks -> FAIL CLOSED ------------
# A manifest that classifies nothing must never certify "within budget".
F="$TMP/f/dist"; mkdir -p "$F/assets" "$F/.vite"
echo '{}' > "$F/.vite/manifest.json"
assert 2 "$F" "manifest with zero JS chunks"

# --- (d3b) manifest classifies a LAZY chunk but NO MAIN entry -> FAIL CLOSED ---
# This SPA always emits exactly one isEntry (the index.html script). A manifest
# with dynamic-entry chunks but ZERO isEntry chunks is a broken/partial build
# (the HTML entry vanished), so the gate must fail closed (exit 2) rather than
# certify a pass off the lazy chunks alone. (F2.)
M="$TMP/m/dist"; mkdir -p "$M/assets" "$M/.vite"
mkchunk "$M/assets/route-RRRR.js" 150            # lazy, within budget, but no main entry exists
cat > "$M/.vite/manifest.json" <<'JSON'
{ "src/Route.tsx": { "file": "assets/route-RRRR.js", "name": "Route", "src": "src/Route.tsx", "isDynamicEntry": true } }
JSON
assert 2 "$M" "manifest with a lazy chunk but no main (isEntry) chunk"

# --- (d3c) manifest parses to a JSON ARRAY -> FAIL CLOSED ---------------------
# A manifest that is valid JSON but not an OBJECT (e.g. `[]`) cannot be a Vite
# chunk map; the script's Array.isArray guard must fail closed (exit 2). (F10.)
N="$TMP/n/dist"; mkdir -p "$N/assets" "$N/.vite"
mkchunk "$N/assets/index-SSSS.js" 120
echo '[]' > "$N/.vite/manifest.json"
assert 2 "$N" "manifest is a JSON array, not an object"

# --- (d4) manifest present but malformed JSON -> FAIL CLOSED ------------------
G="$TMP/g/dist"; mkdir -p "$G/assets" "$G/.vite"
mkchunk "$G/assets/index-GGGG.js" 120
printf '{ not valid json ' > "$G/.vite/manifest.json"
assert 2 "$G" "malformed manifest JSON"

# --- (d5) manifest entry points at a file missing on disk -> FAIL CLOSED ------
# A stale manifest must not silently pass by skipping the unmeasurable chunk.
H="$TMP/h/dist"; mkdir -p "$H/assets" "$H/.vite"
cat > "$H/.vite/manifest.json" <<'JSON'
{ "index.html": { "file": "assets/index-MISSING.js", "name": "index", "src": "index.html", "isEntry": true } }
JSON
assert 2 "$H" "manifest entry file absent on disk"

# --- (d6) entry's `imports` references a key MISSING from the manifest -> FAIL -
# The static-closure walker (staticClosure) follows manifest[key].imports
# recursively; if an imported key has no manifest entry (or is not a chunk), the
# import graph is broken (Vite always emits the imported chunk's entry). The gate
# must fail closed (exit 2) rather than silently undercount the closure and
# vacuously pass. Pins the closure walker's missing-import-key guard (R3-5).
O="$TMP/o/dist"; mkdir -p "$O/assets" "$O/.vite"
mkchunk "$O/assets/index-TTTT.js" 120            # main own file fine; its import is dangling
cat > "$O/.vite/manifest.json" <<'JSON'
{ "index.html": { "file": "assets/index-TTTT.js", "name": "index", "src": "index.html", "isEntry": true, "imports": ["missing-key"] } }
JSON
assert 2 "$O" "entry imports a manifest key that does not exist"

# --- (d7) entry's `imports` holds a NON-STRING element -> FAIL CLOSED ----------
# Vite's ManifestChunk contract is `imports: string[]` (arrays of manifest KEYS).
# A non-string element (here a number) is a contract violation; silently skipping
# it could undercount the closure and vacuously pass the budget. The closure walker
# must fail closed (exit 2) on it rather than drop it (R4-3 / minimax).
T="$TMP/t/dist"; mkdir -p "$T/assets" "$T/.vite"
mkchunk "$T/assets/index-UUUU.js" 120            # main own file fine; one import is non-string
cat > "$T/.vite/manifest.json" <<'JSON'
{ "index.html": { "file": "assets/index-UUUU.js", "name": "index", "src": "index.html", "isEntry": true, "imports": [123] } }
JSON
assert 2 "$T" "entry imports array holds a non-string element"

# --- (d8) entry's `imports` is PRESENT but NOT AN ARRAY -> FAIL CLOSED ----------
# Vite's ManifestChunk contract is `imports?: string[]`. A present-but-non-array
# `imports` (here a bare string) is a contract violation; the closure walker only
# follows `imports` when it is already an array, so a non-array value would be
# SILENTLY IGNORED (the chunk it names never counted) — a fail-open undercount.
# The gate must fail closed (exit 2) on it (R5-2). Pins the imports-shape guard.
U="$TMP/u/dist"; mkdir -p "$U/assets" "$U/.vite"
mkchunk "$U/assets/index-VVVV.js" 120            # main own file fine; imports is a non-array
cat > "$U/.vite/manifest.json" <<'JSON'
{ "index.html": { "file": "assets/index-VVVV.js", "name": "index", "src": "index.html", "isEntry": true, "imports": "_shared.js" } }
JSON
assert 2 "$U" "entry imports is a non-array (bare string)"

# --- (d9) entry's `dynamicImports` is PRESENT but NOT AN ARRAY -> FAIL CLOSED ---
# Same contract (`dynamicImports?: string[]`). A present-but-non-array value is a
# manifest-contract violation the gate must reject rather than ignore (R5-2),
# consistent with the `imports` guard above. (dynamicImports are not followed into
# the closure, but a malformed manifest is still fail-closed.)
V="$TMP/v/dist"; mkdir -p "$V/assets" "$V/.vite"
mkchunk "$V/assets/index-WWWW.js" 120            # main own file fine; dynamicImports is a non-array
cat > "$V/.vite/manifest.json" <<'JSON'
{ "index.html": { "file": "assets/index-WWWW.js", "name": "index", "src": "index.html", "isEntry": true, "dynamicImports": "src/Route.tsx" } }
JSON
assert 2 "$V" "entry dynamicImports is a non-array (bare string)"

echo
if [ "$fail" -eq 0 ]; then
  echo -e "${GREEN}[ok]${NC} check-bundle-size self-test: ${pass}/${pass} assertions pass."
else
  echo -e "${RED}[FAIL]${NC} check-bundle-size self-test: ${fail} failed, ${pass} passed."
  exit 1
fi
