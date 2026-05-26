# Adapter: claude-code

## Status

**Phase 2 target.** Specification is preliminary — final field mapping will be evidence-driven from sample files at implementation time.

## Source Format

Claude Code stores each session as one JSONL transcript, sharded by working-directory:

```
~/.claude/projects/<encoded-cwd>/<sessionId>.jsonl
```

- `<encoded-cwd>` is the working directory with `/` replaced by `-` (e.g. `/home/costa/src/ai-agent.git` → `-home-costa-src-ai-agent-git`).
- `<sessionId>` is a UUID v4.

Subdirectories of `~/.claude/projects/` also exist for some nested layouts (e.g. `-home-costa-src-ai-agent-git/<sessionId>/`); the adapter must walk recursively.

## Record Shape

Each line is a JSON object with a `type` discriminator. Known types observed from real samples:

- `permission-mode` — session-level setting
- `file-history-snapshot` — file backup tracking
- Plus message/turn records (exact field names TBD when adapter is implemented; will be sampled from real fixture files)

Authoritative source: Claude Code source mirror at `/opt/baddisk/monitoring/repos/ai/claude-code/` (TypeScript). The adapter spec will be confirmed against the upstream-published transcript schema.

## Watch Strategy

- `fsnotify.Add()` recursively on `~/.claude/projects/`.
- React to: new project subdirectories (`CREATE` on directory), new transcript files, appends to existing transcripts.
- Per-file byte-offset cursor, same as ai-agent v3.

## Cursor

```json
{
  "files": {
    "<encoded-cwd>/<sessionId>.jsonl": {
      "offset": <byte_offset>
    }
  },
  "version": 1
}
```

## Mapping to Canonical Events (preliminary)

To be detailed when the adapter is implemented. Expected mapping shape:

- session start → `SessionStartedEvent` (extras_json carries `cwd`)
- user/assistant turn boundary → `TurnStartedEvent` / `TurnFinalizedEvent`
- assistant LLM call → `OpStartedEvent`/`OpFinalizedEvent` with kind=llm
- tool_use → `OpStartedEvent`/`OpFinalizedEvent` with kind=tool
- sub-agent task → `OpStartedEvent` with kind=session + linked child session events

## Sub-Agent Linkage

Claude Code Task tool invocations spawn sub-agents with their own session IDs. These appear in the parent transcript as `tool_use` blocks naming the subagent and produce their own transcript files (or are inlined — TBD at implementation).

## Known Considerations

- **Permissions data may be sensitive.** The `permission-mode` records include the user's permission state. Treat as redactable for fixture files committed to testdata.
- **File contents in tool outputs.** Read/Write/Edit tool outputs may contain user code. Fixtures must redact.
- **Working directory may identify private projects.** `<encoded-cwd>` itself is potentially sensitive. Fixtures use sanitized paths.

## References

- /opt/baddisk/monitoring/repos/ai/claude-code/ — upstream source mirror
- Real samples at `~/.claude/projects/` on the operator's workstation (for development; not committed)
