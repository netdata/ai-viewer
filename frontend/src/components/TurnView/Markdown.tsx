// Shared markdown renderer used by UserPrompt / Reasoning / Assistant step
// bodies (ui-turn-view.md §Markdown rendering). react-markdown + remark-gfm +
// rehype-highlight give us prose + tables + fenced-code highlighting in ~30 KB
// gz total — well under the 500 KB bundle budget.
import { useMemo } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeHighlight from 'rehype-highlight';
import 'highlight.js/styles/github-dark.css';
import styles from './Markdown.module.css';

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
  // We pass the rehype-highlight plugin once via the `remarkPlugins` array.
  // rehype-highlight mutates the AST in-place, applying CSS classes from
  // highlight.js's `github-dark` theme imported above. This gives consistent
  // contrast in both light and dark mode.
  const plugins = useMemo(() => [remarkGfm, rehypeHighlight], []);
  return (
    <div className={[styles.prose, className].filter(Boolean).join(' ')}>
      <ReactMarkdown remarkPlugins={plugins}>{source}</ReactMarkdown>
    </div>
  );
}