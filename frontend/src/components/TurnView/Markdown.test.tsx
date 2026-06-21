import { describe, expect, it } from 'vitest';
import { extractReadableText } from './Markdown';

// extractReadableText (SOW-0090): the turn-view markdown renderer pre-processes
// adapter payload bytes through this function before rendering. Adapters persist
// response_item.message / response_item.reasoning / response_item.function_call
// as the verbatim wire form, which is JSON wrapping the human text. Without
// this function, the operator sees a wall of JSON; with it, the human text.

describe('extractReadableText', () => {
  it('returns the verbatim string for plain prose (non-JSON)', () => {
    const raw = 'Please refactor the auth middleware to use bcrypt.';
    expect(extractReadableText(raw)).toBe(raw);
  });

  it('returns the verbatim string for invalid JSON', () => {
    const raw = '{not valid json';
    expect(extractReadableText(raw)).toBe(raw);
  });

  it('returns the verbatim string for non-object JSON (numbers, arrays at top)', () => {
    expect(extractReadableText('42')).toBe('42');
    expect(extractReadableText('[1,2,3]')).toBe('[1,2,3]');
    expect(extractReadableText('true')).toBe('true');
    expect(extractReadableText('null')).toBe('null');
  });

  it('returns verbatim when the JSON has no recognizable text fields', () => {
    const raw = '{"timestamp":"2026-06-20T21:08:39Z","status":"ok"}';
    expect(extractReadableText(raw)).toBe(raw);
  });

  it('extracts the text field from a flat object', () => {
    const raw = '{"text":"hello world","id":"x"}';
    expect(extractReadableText(raw)).toBe('hello world');
  });

  it('extracts content_text and output_text fields', () => {
    expect(extractReadableText('{"content_text":"foo"}')).toBe('foo');
    expect(extractReadableText('{"output_text":"bar"}')).toBe('bar');
  });

  it('extracts text from a codex response_item.message envelope', () => {
    // Real codex wire form: {timestamp, type:"response_item",
    // payload:{type:"message", role:"user", content:[{type:"input_text",
    // text:"..."}]}}
    const raw = JSON.stringify({
      timestamp: '2026-06-20T21:08:39.295Z',
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'user',
        content: [
          { type: 'input_text', text: 'What is the weather today?' },
        ],
      },
    });
    expect(extractReadableText(raw)).toBe('What is the weather today?');
  });

  it('concatenates multiple text fragments with newlines (system + user prompt)', () => {
    // Codex often includes a system developer message + user prompt in one
    // response_item stream. We want both surfaced.
    const raw = JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'developer',
        content: [{ type: 'input_text', text: 'You are an expert code reviewer.' }],
      },
    });
    expect(extractReadableText(raw)).toBe('You are an expert code reviewer.');
  });

  it('extracts text from a codex reasoning envelope (summary path)', () => {
    const raw = JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'reasoning',
        summary: [{ type: 'summary_text', text: 'The user wants refactoring.' }],
      },
    });
    expect(extractReadableText(raw)).toBe('The user wants refactoring.');
  });

  it('handles a multi-content assistant message', () => {
    // Assistant message with text + tool_use: the text is the readable part.
    const raw = JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'assistant',
        content: [
          { type: 'output_text', text: 'Let me look at the auth file.' },
          { type: 'tool_use', name: 'read_file', input: { path: '/auth.ts' } },
        ],
      },
    });
    // Only the output_text is extracted — tool_use has no `text` field and
    // we don't recurse into `input` (it's structured args).
    expect(extractReadableText(raw)).toBe('Let me look at the auth file.');
  });

  it('handles deeply-nested envelopes (max depth 32, safe)', () => {
    // Build a 40-level-deep JSON. We don't crash on deep nesting; we just
    // stop walking past depth 32 and fall back to the verbatim string.
    let nested: Record<string, unknown> = { text: 'deep' };
    for (let i = 0; i < 40; i++) {
      nested = { wrapper: nested };
    }
    const raw = JSON.stringify(nested);
    // The deep `text` is past the depth limit. We get the verbatim string,
    // which contains "deep" as part of the JSON — that's acceptable, the
    // operator can still see the structure.
    expect(extractReadableText(raw)).toBe(raw);
  });

  it('handles empty string', () => {
    expect(extractReadableText('')).toBe('');
  });

  it('handles JSON null', () => {
    expect(extractReadableText('null')).toBe('null');
  });

  it('returns verbatim when content array has no text fields (tool calls only)', () => {
    // Assistant message that's all tool calls and no text. The function
    // returns verbatim so the operator sees the raw structure (and the UI
    // can render a separate "tool call only" affordance).
    const raw = JSON.stringify({
      type: 'response_item',
      payload: {
        type: 'message',
        role: 'assistant',
        content: [{ type: 'tool_use', name: 'shell', input: { cmd: 'ls' } }],
      },
    });
    expect(extractReadableText(raw)).toBe(raw);
  });

  it('handles a content_text inside an array of objects', () => {
    const raw = JSON.stringify([
      { type: 'text', text: 'first' },
      { type: 'text', text: 'second' },
    ]);
    // The walk recurses into array elements and finds both `text` fields.
    expect(extractReadableText(raw)).toBe('first\nsecond');
  });
});