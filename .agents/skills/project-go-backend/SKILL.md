---
name: project-go-backend
description: Apply Go-specific patterns used in ai-viewer for fsnotify watching, SQLite access, SSE streaming, and structured logging. Use when editing internal/ingest, internal/store, internal/presenter, internal/adapters, internal/canonical, or internal/obs.
---

# Go Backend Patterns

## Stack Versions

Always pin to the latest stable Go release. Update Go toolchain (`go.mod` `go` directive + CI runner) when a new minor release is GA and has been out for 2+ weeks.

Dependency policy: prefer stdlib. When a library is needed, prefer the canonical pure-Go option:

| Need | Library |
|---|---|
| File watching | `github.com/fsnotify/fsnotify` |
| SQLite | `modernc.org/sqlite` (pure Go, CGO-free, cross-compiles) |
| Logging | stdlib `log/slog` |
| HTTP routing | stdlib `net/http` + `http.ServeMux` (Go 1.22+ pattern routing) |
| UUIDs | `github.com/google/uuid` |
| Testing assertions | stdlib `testing`; resist adding testify |
| YAML config | `gopkg.in/yaml.v3` |

Adding any new dependency requires a one-line note in the SOW under `## Decisions`.

## Concurrency Patterns

### Goroutine ownership

Every goroutine has a clear owner who is responsible for starting and stopping it. Document in the constructor godoc:

```go
// NewIngester constructs an ingester. The caller MUST call Close to stop
// the internal worker goroutines.
func NewIngester(...) *Ingester { ... }
```

### Context propagation

- Every blocking function takes `ctx context.Context` as the first parameter.
- Every goroutine respects `ctx.Done()` for shutdown.
- HTTP handlers use `r.Context()` and pass it through to store/query calls.
- Registered REST handlers (`mux.HandleFunc("/api/...", p.<handler>)`) keep
  their method gate directly visible in the handler body as `r.Method !=
  http.Method...` comparisons. Do not extract the method guard itself into a
  helper: `scripts/spec-drift.sh` intentionally fails closed when it cannot
  prove the registered verb/path contract from the handler body. Extract the
  error writer or response/query work instead.

### Channels

- Direction: prefer `<-chan T` / `chan<- T` in function signatures to make ownership explicit.
- Buffer sizes: documented in code with a comment explaining the choice and the drop policy when full.
- Close: only the sender closes. Document who the sender is.

## SQLite Patterns

### Schema migrations

`internal/store/migrations/NNNN_description.sql`. Run in alphabetical order at startup; tracked in `schema_meta`. Migrations are idempotent (`CREATE TABLE IF NOT EXISTS ...`) where possible; non-idempotent migrations are guarded by version checks.

### Prepared statements

Hold prepared statements in a package-level `*sql.Stmt` cache initialized once. Re-preparing on every call is forbidden.

### Transactions

- Ingest pipeline batches writes in transactions of up to 100 rows or 500 ms, whichever first.
- Keep source-scoped derived-table repair chunks at the same 100-row writer
  budget unless a later SOW proves a larger chunk still leaves Tail
  heartbeat/stale-tail writes within their 30 s liveness budget.
- Server-side reads do not use transactions unless multiple queries must be consistent (rare).
- Always `defer tx.Rollback()` immediately after `Begin`; the deferred call is a no-op if `Commit` succeeded.

### Connection pool

- Ingester uses single-writer (max-open=1) on the write connection; readers can be a separate pool.
- Server uses read-only pool (max-open=8). Tunable later if profiling shows contention.

## fsnotify Patterns

```go
w, err := fsnotify.NewWatcher()
if err != nil { return err }
defer w.Close()

if err := w.Add(rootDir); err != nil { return err }

// For subdirs created at runtime: re-Add when CREATE event fires on a dir.

for {
    select {
    case <-ctx.Done():
        return ctx.Err()
    case ev, ok := <-w.Events:
        if !ok { return nil }
        handle(ev)
    case err, ok := <-w.Errors:
        if !ok { return nil }
        opts.OnError(err)
    }
}
```

- Use `w.Add(dir)` not file paths. Filter events in `handle`.
- Recurse manually: walk the tree once at startup, then `Add` each new dir on `CREATE`.
- Atomic rename pattern (`x.tmp` → `x`): listen for `Rename`/`Create` on the target, not `Write`.
- Inotify watcher limits: on Linux, default user limit is 8192 watches. If the source tree exceeds, log a clear error pointing to `sysctl fs.inotify.max_user_watches`.

## SSE Patterns

stdlib only. Pattern:

```go
func sseHandler(w http.ResponseWriter, r *http.Request) {
    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming unsupported", http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

    sub, err := hub.Subscribe(...)
    if err != nil { http.Error(...); return }
    defer hub.Unsubscribe(sub)

    keepalive := time.NewTicker(15 * time.Second)
    defer keepalive.Stop()

    for {
        select {
        case <-r.Context().Done():
            return
        case ev := <-sub.Events:
            fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, ev.JSON)
            flusher.Flush()
        case <-keepalive.C:
            fmt.Fprint(w, ": keepalive\n\n")
            flusher.Flush()
        }
    }
}
```

## Logging Patterns

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
logger = logger.With("subsystem", "ingest")

logger.Info("source scan complete",
    "source_id", srcID,
    "events", n,
    "duration_us", dur.Microseconds(),
)

logger.Warn("parse error",
    "source_id", srcID,
    "file", path,
    "offset", off,
    "err", err,
)
```

- Never log raw user content from snapshots (PII concerns).
- Always include the subsystem.
- Errors at WARN; system failures at ERROR.

## Error Patterns

```go
// Wrap with context using %w
return fmt.Errorf("scan source %s: %w", srcID, err)

// Sentinel errors for control flow
var ErrCursorBehind = errors.New("cursor behind oldest available record")

// Adapter parse errors call opts.OnError, do not return
opts.OnError(fmt.Errorf("parse record at %s:%d: %w", file, off, err))
continue
```

## Testing Patterns

```go
func TestScanHappyPath(t *testing.T) {
    t.Parallel()

    fixDir := filepath.Join("testdata", "happy_path")
    out := make(chan canonical.Event, 100)
    errs := []error{}
    a := New(fixDir, canonical.AdapterOptions{
        OnError: func(e error) { errs = append(errs, e) },
    })

    if err := a.Scan(context.Background(), canonical.Cursor{}, out); err != nil {
        t.Fatalf("scan: %v", err)
    }
    close(out)

    got := drain(out)
    want := readGolden(t, filepath.Join(fixDir, "expected.jsonl"))
    if diff := cmp.Diff(want, got); diff != "" {
        t.Fatalf("events mismatch (-want +got):\n%s", diff)
    }
    if len(errs) > 0 { t.Fatalf("unexpected errors: %v", errs) }
}
```

- `t.Parallel()` on every test that doesn't share state.
- Use table-driven tests for input variations.
- Use `cmp` for struct/slice comparisons (clearer diffs than `reflect.DeepEqual`).
- Golden files in `testdata/<name>/expected.jsonl`; update with `-update-golden` flag.

## Linting Configuration

`.golangci.yml` (v2) enables the strict set — errcheck, govet, ineffassign, staticcheck, unused, errorlint, gocritic, revive, gocyclo (min-complexity 25), misspell, nilerr, prealloc, unconvert, unparam, whitespace, bodyclose, noctx — plus the gofmt/goimports/gofumpt formatters. `gosimple` is folded into staticcheck (v2); `gosec` runs standalone (not as a golangci linter). Zero-warnings policy. The authoritative gate catalog is `.agents/sow/specs/quality-gates.md` "Go — Lint" and the `project-quality-gates` skill; run locally via `scripts/lint.sh`.
