package codex

import (
	"testing"
)

// FuzzParseLine exercises the JSONL line parser against arbitrary bytes. The
// contract under fuzz is: parseLine never panics regardless of input. It
// returns either a parsed record, a skip, or a wrapped error. Seeds cover every
// top-level RolloutItem type, a representative spread of nested response_item /
// event_msg variants, the polymorphic session_meta source shapes, the
// sandbox_policy variants, malformed JSON, and unknown top-level / nested types.
// Satisfies SOW-0004 acceptance #7 (fuzz target on the JSONL parser). All seeds
// are synthetic with placeholder identities (no real session content).
func FuzzParseLine(f *testing.F) {
	seeds := [][]byte{
		// session_meta, line 1, bare-string source + git block.
		[]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"019aa234-a2a1-75c3-a9bf-d8425e1785f5","timestamp":"2025-11-20T16:59:09.857Z","cwd":"<ROOT>","originator":"codex_exec","cli_version":"0.125.0","source":"exec","model_provider":"openai","git":{"commit_hash":"abc","branch":"main","repository_url":"git@github.com:example/example.git"}}}`),
		// session_meta, {custom} source.
		[]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"sid","source":{"custom":"my_tool"}}}`),
		// session_meta, {internal} source (memory consolidation).
		[]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"sid","source":{"internal":"memory_consolidation"},"thread_source":"memory_consolidation"}}`),
		// session_meta, sub-agent thread_spawn source with parent link.
		[]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"child","thread_source":"subagent","agent_role":"explorer","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent-uuid","depth":1,"agent_nickname":"Tesla","agent_role":"explorer"}}}}}`),
		// session_meta, fork.
		[]byte(`{"timestamp":"2025-11-20T16:59:09.857Z","type":"session_meta","payload":{"id":"forked","forked_from_id":"origin-uuid","source":"cli"}}`),
		// turn_context, new format, workspace-write.
		[]byte(`{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{"turn_id":"turn-1","cwd":"<ROOT>","model":"gpt-5.1-codex-max","effort":"high","approval_policy":"on-request","sandbox_policy":{"type":"workspace-write"}}}`),
		// turn_context, danger-full-access, no turn_id (old format).
		[]byte(`{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{"cwd":"<ROOT>","model":"gpt-5.5","sandbox_policy":{"type":"danger-full-access"}}}`),
		// response_item, assistant message.
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}],"phase":"final_answer"}}`),
		// response_item, user message.
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"do x"}]}}`),
		// response_item, reasoning (summary + encrypted).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}],"content":null,"encrypted_content":"AAAA"}}`),
		// response_item, function_call (arguments is a JSON-encoded string).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\",\"-la\"]}","call_id":"call_1"}}`),
		// response_item, function_call_output (output is a string).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":"total 0"}}`),
		// response_item, function_call_output (output is an object form).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_1","output":{"content":"ok"}}}`),
		// response_item, custom_tool_call / output.
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"custom_tool_call","call_id":"call_2","name":"apply_patch","input":"*** Begin Patch","status":"completed"}}`),
		// response_item, web_search_call WITH a call_id (older shape, tolerated).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"web_search_call","call_id":"ws_1","status":"completed","action":{"type":"search","query":"q"}}}`),
		// response_item, web_search_call with NO id/call_id (real shape, F7).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}}`),
		// response_item, compaction / context_compaction.
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"compaction","encrypted_content":"BBBB"}}`),
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"context_compaction","encrypted_content":null}}`),
		// response_item, ghost_snapshot (catch-all no-op).
		[]byte(`{"timestamp":"2025-11-20T16:59:11.000Z","type":"response_item","payload":{"type":"ghost_snapshot","data":{}}}`),
		// response_item, legacy local_shell_call.
		[]byte(`{"timestamp":"2025-06-26T10:00:00.000Z","type":"response_item","payload":{"type":"local_shell_call","call_id":"ls_1"}}`),
		// event_msg, user_message with images.
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"user_message","message":"hi","images":["a.png"],"local_images":["/x/a.png"]}}`),
		// event_msg, agent_message final_answer.
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"agent_message","message":"answer","phase":"final_answer"}}`),
		// event_msg, agent_reasoning + raw content.
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"agent_reasoning","text":"summary"}}`),
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"agent_reasoning_raw_content","text":"raw cot"}}`),
		// event_msg, token_count.
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":100,"input_tokens":80,"output_tokens":20},"last_token_usage":{"input_tokens":10,"output_tokens":5}},"model_context_window":200000}}`),
		// event_msg, task_started / task_complete (new format turn boundary).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1","started_at":1763664000,"model_context_window":200000}}`),
		[]byte(`{"timestamp":"2025-11-20T17:00:00.000Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"turn-1","last_agent_message":"done","completed_at":"2025-11-20T17:00:00.000Z","duration_ms":1000,"time_to_first_token_ms":120}}`),
		// event_msg, turn_aborted (interrupted).
		[]byte(`{"timestamp":"2025-11-20T17:00:00.000Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"turn-1","reason":"interrupted","duration_ms":500}}`),
		// event_msg, exec_command_end (Extended; aggregated_output only).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"exec_command_end","call_id":"call_1","turn_id":"turn-1","command":["ls"],"exit_code":0,"aggregated_output":"out","duration":{"secs":0,"nanos":1},"status":"completed"}}`),
		// event_msg, mcp_tool_call_end.
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"mcp_tool_call_end","call_id":"call_1","invocation":{"server":"github","tool":"create_issue","arguments":{}}}}`),
		// event_msg, patch_apply_end.
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"patch_apply_end","call_id":"call_1","turn_id":"turn-1","success":true,"status":"completed","changes":{}}}`),
		// event_msg, context_compacted (unit struct).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"context_compacted"}}`),
		// event_msg, web_search_end (carries a call_id; pairs positionally, F7).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"web_search_end","call_id":"ws_1","query":"q","action":{"type":"search"}}}`),
		// event_msg, collab_agent_spawn_end (parent→child via new_thread_id, F3).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"collab_agent_spawn_end","sender_thread_id":"parent-uuid","new_thread_id":"child-uuid","new_agent_nickname":"Dewey","new_agent_role":"explorer","model":"gpt-5.5","reasoning_effort":"high","status":"completed"}}`),
		// event_msg, collab_close_end / collab_waiting_end (recognized, log-only, F3).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"collab_close_end","call_id":"x"}}`),
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"collab_waiting_end","call_id":"y"}}`),
		// event_msg, error (Extended).
		[]byte(`{"timestamp":"2025-11-20T16:59:12.000Z","type":"event_msg","payload":{"type":"error","message":"boom"}}`),
		// compacted top-level line with replacement_history.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"compacted","payload":{"message":"summary","replacement_history":[{"type":"message","role":"user","content":[]}]}}`),
		// compacted, null replacement_history.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"compacted","payload":{"message":"summary","replacement_history":null}}`),
		// unknown top-level type.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"totally_unknown","payload":{}}`),
		// unknown nested payload type (response_item).
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"response_item","payload":{"type":"brand_new_variant","x":1}}`),
		// unknown nested payload type (event_msg).
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"event_msg","payload":{"type":"brand_new_event"}}`),
		// known top-level, absent payload.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"event_msg"}`),
		// known top-level, null payload.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"response_item","payload":null}`),
		// nested type present but body otherwise minimal.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"response_item","payload":{"role":"assistant"}}`),
		// missing top-level type.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","payload":{}}`),
		// malformed JSON.
		[]byte(`{not json`),
		// blank / whitespace.
		[]byte(""),
		[]byte("  \t\n "),
		// embedded control char as a JSON \uXXXX escape in a tool output
		// string (edge case #11): codex serializes ANSI escapes this way and
		// encoding/json must accept them. \u001b is a literal 6-char escape
		// inside this raw-string seed, exactly as it appears on disk.
		[]byte(`{"timestamp":"2025-11-20T16:59:13.000Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c","output":"line1\u001b[0mline2"}}`),
		// sandbox_policy with an unknown future type (must not hard-fail).
		[]byte(`{"timestamp":"2025-11-20T16:59:10.000Z","type":"turn_context","payload":{"model":"m","sandbox_policy":{"type":"brand-new-mode"}}}`),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		// MUST NOT panic on any input. Returned values are not asserted, but the
		// typed helpers reachable from a parsed record must also not panic.
		rec, skip, err := parseLine(data)
		if err == nil && !skip {
			if rec.SessionMeta != nil {
				_, _ = rec.SessionMeta.classifySource()
			}
			if rec.TurnContext != nil {
				_ = rec.TurnContext.sandboxType()
			}
			if rec.Compacted != nil {
				_ = rec.Compacted.replacementHistorySize()
			}
		}
	})
}

// FuzzParseCursor exercises ParseCursor against arbitrary cursor blobs.
// Contract: never panics; either returns a Cursor or a wrapped error.
func FuzzParseCursor(f *testing.F) {
	seeds := []string{
		"",
		"{}",
		`{"version":1}`,
		`{"version":1,"files":{}}`,
		`{"version":1,"files":{"2025/11/20/r.jsonl":{"offset":7,"size":7,"mtime_us":42}}}`,
		`{"version":1,"files":{"2025/11/20/r.jsonl":{"offset":1}},"legacy_json":{"rollout-2025-06-26-x.json":{"ingested":true}}}`,
		`{"version":99}`,
		`{"files":{"x":{}}}`,
		"not json",
		`{"version":1,"files":null,"legacy_json":null}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		c, err := ParseCursor(s)
		if err == nil {
			// A successfully parsed cursor must round-trip and answer After
			// against itself without panicking.
			_ = c.String()
			_ = c.After(c)
		}
	})
}
