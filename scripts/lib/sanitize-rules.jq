# sanitize-rules.jq
#
# JSON-level redaction rules for fixture sanitization.
#
# Loaded by scripts/sanitize-fixture.sh. The wrapping shell script handles
# - per-line jsonl iteration (aiagent_v3 ledgers)
# - whole-file decode/encode (aiagent_v2 single gzipped JSON)
# - UUID mapping (pre-built externally, passed in as $id_map)
# - string-level rules that act on the raw bytes BEFORE jq sees the JSON
#   (operator HOME path, emails, API URLs, bearer tokens, classic secret
#    patterns) so unstructured payload bodies are also covered.
#
# The library exposes two entry points used from the shell wrapper:
#   sanitize_v3_record     # one ledger record (object), returns sanitized object
#   sanitize_v2_snapshot   # whole v2 snapshot ({version,reason,opTree})
#
# Contract:
# - Schema shape is preserved (every key the producer wrote is still present).
# - UUID-shaped strings inside known id fields are looked up in $id_map; if
#   absent (mapping pre-pass missed them), they are passed through unchanged
#   so the wrapper can detect the gap and warn rather than silently destroy
#   linkage.
# - User/assistant/tool/reasoning bodies are wholesale replaced with the
#   project-standard placeholders defined in AGENTS.md and security.md.

# --- helpers -----------------------------------------------------------------

def is_uuid_string:
  type == "string"
  and test("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$");

# Replace ALL UUID-shaped substrings inside a string using $id_map. Used
# both for whole-string id fields (e.g. originId) and for compound strings
# that EMBED a UUID (e.g. "session/<sessionId>.jsonl", "Bearer ...uuid...").
# Unmapped UUIDs are returned unchanged.
def remap_uuids_in_string:
  if type != "string" then .
  else
    . as $s
    | ([ scan("[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}") ] | unique) as $found
    | reduce $found[] as $u ($s; gsub($u; ($id_map[$u] // $u)))
  end;

# Walk a value recursively and apply a per-leaf rewrite.
def walk_leaves(rewrite):
  if type == "object" then
    with_entries(.value |= walk_leaves(rewrite))
  elif type == "array" then
    map(walk_leaves(rewrite))
  else
    rewrite
  end;

# Replace any UUID-shaped substring anywhere in the subtree, using $id_map.
def remap_uuids_deep:
  walk_leaves(remap_uuids_in_string);

# --- claude-code sanitization ------------------------------------------------
#
# Per spec adapter-claude-code.md, a claude-code transcript is JSONL: one
# record per line with a `type` discriminator. Operator data lives in:
#   user.message.content              (string prompt, OR array of tool_result/
#                                       text/image blocks)
#   assistant.message.content[]       (text / thinking / tool_use.input blocks)
#   toolUseResult                     (structured tool echo, varies by tool)
#   system.content                    (hook/error/command free text)
#   attachment.attachment.*           (injected file/dir/skill content)
#   queue-operation.content           (queued prompt)
#   last-prompt.lastPrompt            (last user prompt snapshot)
#   ai-title.aiTitle / custom-title.customTitle  (titles, may leak)
#   compactMetadata (preserved* uuids only — structural, kept)
# id-bearing fields (UUID-shaped, remapped to preserve linkage):
#   uuid, parentUuid, logicalParentUuid, sessionId, and any UUID inside
#   slug / preservedSegment / preservedMessages.
# NOT remapped (not UUID-shaped, not sensitive): agentId (15-hex), requestId,
#   tool_use ids (toolu_*), message ids (msg_*), promptId.
# Sensitive flags redacted per spec §3.8: permissionMode == "bypassPermissions"
#   is rewritten to "default" so a committed fixture never advertises that the
#   operator authorized YOLO mode.

# Redact a content value that is either a string (prompt) or an array of
# Anthropic content blocks. Strings become a placeholder; arrays have each
# block's free-text redacted while preserving block type + join ids.
def redact_content:
  if type == "string" then "[REDACTED_USER_MESSAGE]"
  elif type == "array" then
    map(
      if type == "object" then
        (if .type == "text" then .text = "[REDACTED_TEXT]" else . end)
        | (if .type == "thinking" then
             (.thinking = "[REDACTED_REASONING]")
             | (if has("signature") then .signature = "[REDACTED_SIGNATURE]" else . end)
           else . end)
        | (if .type == "tool_use" and has("input") then .input = {} else . end)
        | (if .type == "tool_result" and has("content") then
             .content = "[REDACTED_TOOL_OUTPUT]"
           else . end)
        | (if .type == "image" then
             (if has("source") then .source = {"type": "redacted"} else . end)
           else . end)
      else . end
    )
  else . end;

def sanitize_claude_code_record:
  # 1) Remap all UUID-shaped strings anywhere (envelope ids + embedded ids).
  remap_uuids_deep

  # 2) Redact message bodies for user / assistant records.
  | (if (.type == "user" or .type == "assistant")
        and (.message | type) == "object"
        and (.message | has("content"))
     then .message.content |= redact_content
     else . end)

  # 3) Redact the structured tool echo.
  | (if has("toolUseResult") and (.toolUseResult != null)
     then .toolUseResult = "[REDACTED_TOOL_OUTPUT]"
     else . end)

  # 4) Redact system free-text content (keep subtype + structural metadata).
  | (if .type == "system" and (.content | type) == "string"
     then .content = "[REDACTED_LOG_LINE]"
     else . end)

  # 5) Redact attachment payloads (keep the attachment.type discriminator).
  | (if .type == "attachment" and (.attachment | type) == "object"
     then .attachment = {"type": (.attachment.type // "redacted")}
     else . end)

  # 6) Redact queued-prompt content.
  | (if .type == "queue-operation" and (.content | type) == "string"
     then .content = "[REDACTED_USER_MESSAGE]"
     else . end)

  # 7) Redact metadata-snapshot free text.
  | (if .type == "last-prompt" and has("lastPrompt") then .lastPrompt = "[REDACTED_USER_MESSAGE]" else . end)
  | (if .type == "ai-title" and has("aiTitle") then .aiTitle = "[REDACTED_TITLE]" else . end)
  | (if .type == "custom-title" and has("customTitle") then .customTitle = "[REDACTED_TITLE]" else . end)

  # 8) Sensitive permission mode (§3.8): never advertise bypassPermissions.
  | (if .type == "permission-mode" and .permissionMode == "bypassPermissions"
     then .permissionMode = "default"
     else . end)

  # 9) pr-link carries a real repo + URL; redact identifying fields but keep
  #    the record shape (prNumber is harmless).
  | (if .type == "pr-link" then
       (if has("prUrl") then .prUrl = "https://example.invalid/pr" else . end)
       | (if has("prRepository") then .prRepository = "owner/repo" else . end)
     else . end)
;

# --- v3 sanitization ---------------------------------------------------------
#
# Per spec adapter-aiagent-v3.md the relevant id-bearing fields are:
#   originId, sessionId, parentSessionId          (record envelope + childSessions)
#   ops[i].opId                                   (NOT a UUID; passed through)
#   ops[i].childSessions[].sessionId/originId/parentSessionId
#   payloadRefs[].path                            (contains <sessionId>)
# User/assistant/tool/reasoning bodies are NOT in the ledger; they live in
# payload .gz files (handled at the gzip layer by the shell wrapper).
# Free-text fields that may leak operator data live in:
#   turn_end.warnings[]   - WRN log lines
#   turn_end.errors[]     - ERR log lines (can be huge and leak operator data)
#   session_summary.error - terminal failure message
#   session_error.error   - terminal failure message
# These are wholesale replaced with the placeholder.

def sanitize_v3_record:
  # First, remap all UUID-looking strings anywhere in the record.
  # This catches originId / sessionId / parentSessionId in the envelope
  # AND inside attributes/childSessions/payloadRefs without us having to
  # know the exact key paths.
  remap_uuids_deep

  # Now wholesale-redact known free-text fields. Use `?` so missing keys
  # leave the path unchanged (jq's `|=` on a missing path inserts null; the
  # `try/catch` would silently swallow type errors. `if has(...)` is the
  # explicit form.)
  | (if .recordType == "turn_end" then
       (if has("warnings") and (.warnings | type) == "array" then
          .warnings |= (if length > 0 then map("[REDACTED_LOG_LINE]") else . end)
        else . end)
       | (if has("errors") and (.errors | type) == "array" then
            .errors |= (if length > 0 then map("[REDACTED_LOG_LINE]") else . end)
          else . end)
     else . end)

  | (if (.recordType == "session_summary" or .recordType == "session_error")
       and (.error | type) == "string"
       and (.error | length) > 0
     then .error = "[REDACTED_ERROR_MESSAGE]"
     else . end)

  # finalReport.captured is structural; finalReport itself never carries the
  # report bytes in v3 (those live as payload .gz), so we keep it as-is.
;

# --- v2 sanitization ---------------------------------------------------------
#
# v2 snapshots ({version, reason, opTree}) embed the full SessionNode tree.
# Per spec adapter-aiagent-v2.md, operator data appears in:
#   opTree.error                        (free-form, may leak)
#   opTree.sessionTitle                 (free-form, may leak)
#   opTree.attributes.*                 (free-form values)
#   opTree.turns[].ops[].request        ({kind, payload, size})
#   opTree.turns[].ops[].response       ({payload, size, truncated?})
#   opTree.turns[].ops[].reasoning      ({chunks[], final, ...})
#   opTree.turns[].ops[].logs[]         (LogEntry — message + payload fields)
#   opTree.turns[].ops[].accounting[]   (mostly numbers, but command/error free-form)
#   opTree.turns[].ops[].childSession   (recursive — full SessionNode)
#   opTree.steps[].ops[]                (same shape as turns)
#   opTree.finalReport                  (may carry actual report content)
#   opTree.pluginMetas.*                (free-form)
# id-bearing fields:
#   opTree.traceId                      (this session's id)
#   any childSession.traceId            (recursive)
#   any childSessionRef.{sessionId, originId, parentSessionId, parentOpId}
# opTree.id is a SessionTreeBuilder internal uid (NOT a UUID) — passed through.
# op.opId is also a SessionTreeBuilder uid — passed through.

# For v2, we walk the WHOLE document with jq's built-in `walk` and apply
# per-node redactions based on shape. This avoids mutually-recursive `def`
# (which jq does not support) and naturally handles arbitrary nesting of
# childSession trees.

# Per-node redactions for any object that looks like an op (has opId+kind).
def sanitize_v2_op_node:
  (if has("request") and (.request | type) == "object" then
     .request.payload = "[REDACTED_TOOL_REQUEST]"
   else . end)
  | (if has("response") and (.response | type) == "object" then
       .response.payload = "[REDACTED_TOOL_OUTPUT]"
     else . end)
  | (if has("reasoning") and (.reasoning | type) == "object" then
       .reasoning |= (
         (if has("final") then .final = "[REDACTED_REASONING]" else . end)
         | (if has("chunks") and (.chunks | type) == "array" then
              .chunks = []
            else . end)
       )
     else . end)
  | (if has("logs") and (.logs | type) == "array" then
       .logs |= map(
         if type == "object" then
           (if has("message") then .message = "[REDACTED_LOG_LINE]" else . end)
           | (if has("llmRequestPayload") then .llmRequestPayload = "[REDACTED_TOOL_REQUEST]" else . end)
           | (if has("llmResponsePayload") then .llmResponsePayload = "[REDACTED_TOOL_OUTPUT]" else . end)
           | (if has("details") then .details = "[REDACTED_LOG_LINE]" else . end)
         else . end
       )
     else . end)
  | (if has("accounting") and (.accounting | type) == "array" then
       .accounting |= map(
         if type == "object" then
           (if has("command") and (.command | type) == "string" then .command = "[REDACTED_TOOL_REQUEST]" else . end)
           | (if has("error") and (.error | type) == "string" then .error = "[REDACTED_ERROR_MESSAGE]" else . end)
         else . end
       )
     else . end)
;

# Per-node redactions for any object that looks like a SessionNode (has
# traceId AND turns). These overwrite session-level free-form fields.
def sanitize_v2_session_node:
  (if has("sessionTitle") and (.sessionTitle | type) == "string" and (.sessionTitle | length) > 0 then
     .sessionTitle = "[REDACTED_USER_MESSAGE]"
   else . end)
  | (if has("error") and (.error | type) == "string" and (.error | length) > 0 then
       .error = "[REDACTED_ERROR_MESSAGE]"
     else . end)
  | (if has("finalReport") and (.finalReport != null) then
       .finalReport = "[REDACTED_FINAL_REPORT]"
     else . end)
  | (if has("pluginMetas") and (.pluginMetas | type) == "object" then
       .pluginMetas |= with_entries(.value = "[REDACTED_PLUGIN_META]")
     else . end)
;

def sanitize_v2_snapshot:
  # Pass 1: rewrite UUIDs everywhere (envelope + opTree depth).
  remap_uuids_deep
  # Pass 2: walk every node and apply per-node redactions based on shape.
  | walk(
      if type == "object" then
        # Looks like an op? (op nodes have opId + kind, but NOT traceId.)
        (if has("opId") and has("kind") and (has("traceId") | not) then
           sanitize_v2_op_node
         else . end)
        # Looks like a session node? (has traceId AND turns array.)
        | (if has("traceId") and has("turns") then
             sanitize_v2_session_node
           else . end)
      else . end
    )
;
