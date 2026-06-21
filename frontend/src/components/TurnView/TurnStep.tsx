// TurnStep (ui-turn-view.md §Step renderers): one card per op. The renderer
// dispatch is `kind + name` (see ui-turn-view.md table). All step variants
// share a header (kind+name+status+timestamp) and a body region.

import { useEffect, useRef } from 'react';
import { Brain, ChevronRight, MessageSquare, Sparkles, User, Wrench } from 'lucide-react';
import { Link } from 'react-router-dom';
import type { OpDetail } from '../../api/types';
import { usePayloadContent } from './payloadStore';
import { Markdown } from './Markdown';
import { CopyButton } from './CopyButton';
import styles from './TurnStep.module.css';

const ICONS = {
  user: User,
  reasoning: Brain,
  assistant: MessageSquare,
  tool: Wrench,
  session: ChevronRight,
  compaction: Sparkles,
  generic: Sparkles,
} as const;

/** headerFor maps (kind, name) to a step header label + icon. */
function headerFor(op: OpDetail): { label: string; Icon: typeof User; variant: string } {
  if (op.kind === 'internal' && op.name === 'user_input') {
    return { label: 'User', Icon: ICONS.user, variant: 'user' };
  }
  if (op.kind === 'reasoning') {
    return { label: 'Reasoning', Icon: ICONS.reasoning, variant: 'reasoning' };
  }
  if (op.kind === 'llm' && op.name === 'message') {
    return { label: 'Assistant', Icon: ICONS.assistant, variant: 'assistant' };
  }
  if (op.kind === 'tool') {
    return { label: op.name, Icon: ICONS.tool, variant: 'tool' };
  }
  if (op.kind === 'session') {
    return { label: 'Sub-session', Icon: ICONS.session, variant: 'session' };
  }
  if (op.kind === 'compaction') {
    return { label: 'Compaction', Icon: ICONS.compaction, variant: 'compaction' };
  }
  return { label: `${op.kind} · ${op.name}`, Icon: ICONS.generic, variant: 'generic' };
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** detectLanguage returns a best-effort language hint for fenced code blocks. */
function detectLanguage(payloadKind: string | undefined): string {
  if (payloadKind === 'request') return 'json';
  if (payloadKind === 'response') return 'text';
  return 'text';
}

/** wrapAsFenced turns plain text into a fenced code block string so the
 *  rehype-highlight plugin tokenizes it. */
function wrapAsFenced(text: string, language: string): string {
  return `\`\`\`${language}\n${text}\n\`\`\``;
}

/** TruncationFooter renders "Showing first 4 KB of N KB" when the server
 *  flagged the payload as truncated. */
function TruncationFooter({ totalBytes }: { totalBytes: number }) {
  return (
    <p className={styles.truncationFooter}>
      Showing first 4 KB of {formatBytes(totalBytes)} — full content is on disk.
    </p>
  );
}

/** PayloadError renders an error block + Retry button (ui-turn-view.md §Failure modes). */
function PayloadError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className={styles.errorBlock}>
      <p role="alert">Failed to load payload: {message}</p>
      <button type="button" onClick={onRetry}>Retry</button>
    </div>
  );
}

/** ProseBody renders markdown prose (User / Reasoning / Assistant). */
function ProseBody({
  content,
  truncated,
  totalBytes,
}: {
  content: string;
  truncated: boolean;
  totalBytes: number | null;
}) {
  return (
    <div className={styles.proseBody} role="region" aria-label="Prose">
      <Markdown source={content} />
      {truncated && totalBytes !== null ? <TruncationFooter totalBytes={totalBytes} /> : null}
    </div>
  );
}

/** ReasoningBody uses a distinct visual treatment: italic + slight indent +
 *  tinted background, so reasoning reads as "thinking" rather than answer. */
function ReasoningBody({
  content,
  truncated,
  totalBytes,
}: {
  content: string;
  truncated: boolean;
  totalBytes: number | null;
}) {
  return (
    <div className={styles.reasoningBody} role="region" aria-label="Prose">
      <Markdown source={content} />
      {truncated && totalBytes !== null ? <TruncationFooter totalBytes={totalBytes} /> : null}
    </div>
  );
}

/** ToolSection is one of {Params, Response} on a tool step. */
function ToolSection({
  label,
  payloadState,
  payloadKind,
}: {
  label: string;
  payloadState: ReturnType<typeof usePayloadContent>;
  payloadKind: string | undefined;
}) {
  if (payloadState.error !== null) {
    return <PayloadError message={payloadState.error} onRetry={payloadState.retry} />;
  }
  if (payloadState.loading) {
    return <p className={styles.loadingBody}>Loading {label.toLowerCase()}…</p>;
  }
  if (payloadState.content === null) {
    return null;
  }
  return (
    <section className={styles.codeBlock} aria-label={label}>
      <header className={styles.codeHeader}>
        <h4 className={styles.codeLabel}>{label}</h4>
        <CopyButton text={payloadState.content.text} kind="code" />
      </header>
      <Markdown
        source={wrapAsFenced(payloadState.content.text, detectLanguage(payloadKind))}
      />
      {payloadState.content.truncated && payloadState.content.totalBytes !== null ? (
        <TruncationFooter totalBytes={payloadState.content.totalBytes} />
      ) : null}
    </section>
  );
}

function SessionBody({ childSessionId }: { childSessionId: string }) {
  return (
    <div className={styles.sessionBody}>
      <p>
        Spawned sub-session{' '}
        <Link to={`/sessions/${childSessionId}`} className={styles.sessionLink}>
          {childSessionId}
        </Link>
      </p>
    </div>
  );
}

/** TurnStep is the unit of rendering. One TurnDetail.ops entry. */
export function TurnStep({
  op,
  focused,
}: {
  op: OpDetail;
  focused: boolean;
}) {
  const { label, Icon, variant } = headerFor(op);
  const stepRef = useRef<HTMLDivElement | null>(null);

  // Scroll-into-view on focus. Respects prefers-reduced-motion.
  useEffect(() => {
    if (!focused) return;
    const el = stepRef.current;
    if (!el) return;
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    el.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'center' });
  }, [focused]);

  // For tool ops, fetch up to two payloads (request + response) in parallel.
  // For all other ops, fetch the first payload_ref.
  const firstRef = op.payload_refs[0] ?? null;
  const secondRef = op.kind === 'tool' ? (op.payload_refs[1] ?? null) : null;
  const primary = usePayloadContent(firstRef?.id ?? null);
  const secondary = usePayloadContent(secondRef?.id ?? null);

  // Disable the per-step prose Copy button while we don't have content yet.
  const hasProseContent = primary.content !== null && op.kind !== 'tool' && op.kind !== 'session';
  const proseText = primary.content?.text ?? '';

  return (
    <article
      ref={stepRef}
      data-testid={`turn-step-${op.id}`}
      data-op-id={op.id}
      data-kind={op.kind}
      data-focused={focused ? 'true' : 'false'}
      className={[
        styles.step,
        styles[`variant_${variant}`],
        focused ? styles.focused : '',
      ].filter(Boolean).join(' ')}
    >
      <header className={styles.stepHeader}>
        <span className={styles.stepIcon} aria-hidden="true">
          <Icon size={14} />
        </span>
        <h3 className={styles.stepTitle}>{label}</h3>
        <span className={styles.stepMeta}>
          {op.kind === 'tool' && op.duration_us !== null
            ? `${(op.duration_us / 1000).toFixed(0)}ms`
            : null}
          {op.duration_us !== null && op.kind !== 'tool'
            ? `${(op.duration_us / 1000).toFixed(0)}ms`
            : null}
          {op.status !== 'completed' ? <span className={styles.stepError}> · {op.status}</span> : null}
        </span>
        {hasProseContent ? <CopyButton text={proseText} kind="prose" /> : null}
      </header>

      {/* Body dispatch. */}
      {op.kind === 'session' && op.child_session_id !== null ? (
        <SessionBody childSessionId={op.child_session_id} />
      ) : op.kind === 'tool' ? (
        <div className={styles.toolBody}>
          {op.payload_refs[0] ? (
            <ToolSection label="Params" payloadState={primary} payloadKind={op.payload_refs[0].kind} />
          ) : null}
          {op.payload_refs[1] ? (
            <ToolSection label="Response" payloadState={secondary} payloadKind={op.payload_refs[1].kind} />
          ) : null}
          {op.payload_refs.length === 0 ? (
            <p className={styles.emptyBody}>No payloads for this op.</p>
          ) : null}
        </div>
      ) : op.kind === 'reasoning' && primary.content !== null ? (
        <ReasoningBody
          content={primary.content.text}
          truncated={primary.content.truncated}
          totalBytes={primary.content.totalBytes}
        />
      ) : primary.content !== null ? (
        <ProseBody
          content={primary.content.text}
          truncated={primary.content.truncated}
          totalBytes={primary.content.totalBytes}
        />
      ) : primary.error !== null ? (
        <PayloadError message={primary.error} onRetry={primary.retry} />
      ) : primary.loading ? (
        <p className={styles.loadingBody}>Loading…</p>
      ) : op.payload_refs.length === 0 ? (
        <p className={styles.emptyBody}>No payload for this op.</p>
      ) : null}
    </article>
  );
}