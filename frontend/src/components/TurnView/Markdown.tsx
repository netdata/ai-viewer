// Shared markdown renderer used by UserPrompt / Reasoning / Assistant step
// bodies (ui-turn-view.md §Markdown rendering). react-markdown + remark-gfm +
// rehype-highlight give us prose + tables + fenced-code highlighting in ~30 KB
// gz total — well under the 500 KB bundle budget.
import { useMemo } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import styles from './Markdown.module.css';

/**
 * extractReadableText (SOW-0090) unwraps the JSON envelope that every adapter
 * persists for response_item.message / response_item.function_call / etc. so
 * the operator sees the human-readable text rather than the JSON wire form.
 *
 * Adapter payload shapes we recognize:
 *   - codex response_item.message: {type, payload: {type:'message', role,
 *     content:[{type:'input_text'|'output_text', text:string}, ...]}}
 *     → concatenated `text` fields
 *   - codex response_item.reasoning: {type, payload: {type:'reasoning',
 *     summary:[{text:string}], content:[...], encrypted_content:string}}
 *     → summary text or content text
 *   - claude_code / opencode: similar JSON with a `message.content` or
 *     `parts[].text` array → concatenated text
 *   - legacy: raw user-pasted prose
 *
 * The function is a best-effort heuristic. When the payload ISN'T a
 * recognizable envelope, we return it verbatim so the operator still sees
 * something useful (even raw JSON is more informative than a blank block).
 */
export function extractReadableText(raw: string): string {
  if (!raw) return raw;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return raw;
  }
  if (!parsed || typeof parsed !== 'object') return raw;
  const text = collectText(parsed);
  if (text.length === 0) return raw;
  return text.join('\n');
}

/** collectText walks the parsed JSON looking for known text-bearing fields.
 *  Returns concatenated text snippets joined with newlines. Empty array
 *  means no recognizable fields were found. */
function collectText(parsed: unknown): string[] {
  const out: string[] = [];
  walk(parsed, out, 0);
  return out;
}

function walk(node: unknown, out: string[], depth: number): void {
  // Guard against pathological deep JSON (cycle guard via depth limit).
  if (depth > 32 || node === null || typeof node !== 'object') return;
  if (Array.isArray(node)) {
    for (const child of node) walk(child, out, depth + 1);
    return;
  }
  const obj = node as Record<string, unknown>;
  // Text-bearing field names we recognize, in priority order.
  const text = obj['text'] ?? obj['content_text'] ?? obj['output_text'];
  if (typeof text === 'string' && text.length > 0) {
    out.push(text);
    // Don't recurse — we found the leaf text. Recursing would re-emit
    // ancestor fields like `role: "developer"` as text fragments.
    return;
  }
  for (const [k, v] of Object.entries(obj)) {
    if (k === 'text' || k === 'content_text' || k === 'output_text') continue;
    walk(v, out, depth + 1);
  }
}

/** language: detect fenced-block language hint, default to 'text' for
 *  inline / unknown. Used so the wrapping <pre> can advertise the
 *  language for screen readers + our copy button label. */
export function Markdown({
  source,
  className,
}: {
  source: string;
  className?: string;
}) {
  // SOW-0090: pre-extract readable text from the JSON envelope. Adapters
  // persist response_item.message as the verbatim wire form, so without
  // this step the user sees a wall of JSON. extractReadableText is
  // best-effort: if the payload isn't a recognized envelope, the original
  // text passes through verbatim.
  const displaySource = useMemo(() => extractReadableText(source), [source]);
  const plugins = useMemo(() => [remarkGfm, rehypeHighlight], []);
  return (
    <div className={[styles.prose, className].filter(Boolean).join(' ')}>
      <ReactMarkdown remarkPlugins={plugins}>{displaySource}</ReactMarkdown>
    </div>
  );
}