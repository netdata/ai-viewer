---
name: project-adapters
description: Step-by-step workflow for adding, modifying, or debugging a source-format adapter. Use whenever editing internal/adapters/<name>/, internal/canonical/, or related fixture files.
---

# Adapters

## Mental Model

An adapter is a sealed unit that converts one source format into the canonical event stream. It knows everything about its format and nothing about anything else. See `.agents/sow/specs/adapter-contract.md`.

## Adding a New Adapter

1. **Read the format authoritatively first.** Open the upstream source mirror under `/opt/baddisk/monitoring/repos/ai/<tool>/` and read its persistence code. Sample real files (sanitized) from the operator's environment.

2. **Write the spec first.** Create `.agents/sow/specs/adapter-<name>.md`. Required sections: source format, record shape, watch strategy, cursor design, mapping to canonical events, sub-agent linkage, known edge cases, references. The spec is the source of truth; the code follows.

3. **Create the package.** `internal/adapters/<name>/adapter.go`. Implement the `canonical.Adapter` interface. Keep the file under 400 lines; split helpers into sibling files.

4. **Commit sanitized fixtures.** `testdata/<name>/<scenario>/`. Scenarios are listed in `testing-strategy.md`. Run `scripts/sanitize-fixture.sh` before committing.

5. **Write tests.** Scan, Tail, cursor resume, error handling, idempotency. Use golden files.

6. **Register the factory.** `internal/adapters/registry.go`:
   ```go
   registry["myadapter"] = myadapter.Factory
   ```

7. **Add auto-discovery probe.** `internal/ingest/discover.go`: add a probe entry that detects the format's presence on disk.

8. **Update docs.** `README.md` Source Formats table; `docs/adding-an-adapter.md` if any new patterns emerged.

9. **Open or update a SOW.** Run external reviewers (`project-second-opinions` skill).

## Modifying an Existing Adapter

1. Read the adapter's spec under `.agents/sow/specs/adapter-<name>.md`.
2. Read recent commits to that adapter (`git log -p internal/adapters/<name>/`).
3. Check existing fixtures for coverage of the case you're touching.
4. Make the change.
5. Update the spec **in the same commit** as the code.
6. Run `go test ./internal/adapters/<name>/...` with `-race`.
7. If the canonical mapping changed, refresh golden files with `-update-golden` and inspect the diff manually before committing.

## Common Pitfalls

- **Closing the `out` channel.** Don't. The ingester owns it.
- **Panicking on bad input.** Don't. Call `opts.OnError` and continue.
- **Re-emitting on every Scan call.** Use the cursor; the ingester does dedup but adapter-side dedup is far cheaper.
- **Forgetting to `Add` new subdirs.** fsnotify in Go is not recursive; you must `Add` each subdirectory.
- **Reading symlinks outside the configured root.** Resolve with `filepath.EvalSymlinks` and verify the result is inside the root.
- **Holding files open across goroutine boundaries.** Open, read, close in one goroutine.

## Cursor Design Tips

- A cursor must be **persistable and resumable**. Don't store live channels or file handles.
- **Idempotent on replay**: scanning twice with the same cursor should produce the same events.
- **Survive source rewrites**: if the source overwrites a file, the cursor must detect it (mtime + size + content hash where appropriate).

## Sub-Agent Linkage

The canonical model expresses sub-agents as `SessionStartedEvent` with `ParentNativeID` set. The adapter is responsible for figuring out parent/child relationships from the source format and emitting that field correctly. The ingester does the cross-session resolution; the adapter just provides the native IDs.

## Validation Checklist

Before marking adapter work complete:

- [ ] Spec under `.agents/sow/specs/adapter-<name>.md` matches the implementation.
- [ ] Fixtures cover happy/multi-turn/sub-agents/tools/failures/in-progress/cursor-resume.
- [ ] Golden files reviewed manually.
- [ ] `go test -race ./internal/adapters/<name>/...` passes.
- [ ] `golangci-lint run ./internal/adapters/<name>/...` clean.
- [ ] Auto-discovery probe added.
- [ ] README Source Formats table updated.
- [ ] At least one external reviewer (`project-second-opinions`) approved.
