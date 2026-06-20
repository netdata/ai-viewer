import { Gauge } from 'lucide-react';
import { formatNumber } from '../../lib/format';
import { cn } from '../../lib/utils';

// ContextPressure — SOW-0087 chunk 6 (A7).
//
// A small badge that shows how much of a model's context window has
// been consumed by this session. Computation: tokens_in +
// tokens_cache_read (re-using the cache isn't free for the model,
// so it counts toward the budget) divided by the model's known
// context window size.
//
// Models we know about (heuristic — the data layer doesn't track
// context windows today; this is a frontend-only approximation).
// Unknown models show '—' so we never make up a number.

interface ModelSpec {
  /** Maximum input tokens for a single request. */
  contextWindow: number;
  /** Human-readable label for the badge. */
  label: string;
}

// Curated model catalog (last reviewed 2026-06). Each model has its
// published max input window. Add models here as the operator
// starts using them.
const MODEL_CATALOG: Readonly<Record<string, ModelSpec>> = {
  // OpenAI
  'gpt-4':                       { contextWindow: 8_192,     label: 'GPT-4 (8K)' },
  'gpt-4-32k':                   { contextWindow: 32_768,    label: 'GPT-4 32K' },
  'gpt-4-turbo':                 { contextWindow: 128_000,   label: 'GPT-4 Turbo (128K)' },
  'gpt-4o':                      { contextWindow: 128_000,   label: 'GPT-4o (128K)' },
  'gpt-4o-mini':                 { contextWindow: 128_000,   label: 'GPT-4o mini (128K)' },
  'o1-preview':                  { contextWindow: 128_000,   label: 'o1-preview (128K)' },
  'o1-mini':                     { contextWindow: 128_000,   label: 'o1-mini (128K)' },
  // Anthropic
  'claude-3-5-sonnet':           { contextWindow: 200_000,   label: 'Claude 3.5 Sonnet (200K)' },
  'claude-3-5-haiku':            { contextWindow: 200_000,   label: 'Claude 3.5 Haiku (200K)' },
  'claude-3-opus':               { contextWindow: 200_000,   label: 'Claude 3 Opus (200K)' },
  'claude-3-sonnet':             { contextWindow: 200_000,   label: 'Claude 3 Sonnet (200K)' },
  'claude-3-haiku':              { contextWindow: 200_000,   label: 'Claude 3 Haiku (200K)' },
  // Google
  'gemini-1.5-pro':              { contextWindow: 2_000_000, label: 'Gemini 1.5 Pro (2M)' },
  'gemini-1.5-flash':            { contextWindow: 1_000_000, label: 'Gemini 1.5 Flash (1M)' },
};

function lookupModel(model: string): ModelSpec | null {
  // Try exact match first, then prefix match (gpt-4o-2024-05-13 → gpt-4o).
  if (MODEL_CATALOG[model] !== undefined) return MODEL_CATALOG[model] ?? null;
  // Strip a date suffix if present (gpt-4o-2024-05-13).
  const base = model.replace(/-\d{4}-\d{2}-\d{2}$/, '');
  if (MODEL_CATALOG[base] !== undefined) return MODEL_CATALOG[base] ?? null;
  return null;
}

export interface ContextPressureProps {
  model: string;
  /** Cumulative input tokens for the session (fresh + cache-read). */
  tokensIn: number;
  tokensCacheRead: number;
}

export function ContextPressure({ model, tokensIn, tokensCacheRead }: ContextPressureProps) {
  const spec = lookupModel(model);
  if (spec === null) return null;

  // Cache hits count toward the context budget even though they
  // don't add new tokens — the model's KV cache is the context.
  const consumed = tokensIn + tokensCacheRead;
  const pct = (consumed / spec.contextWindow) * 100;

  // 70%+ → warning, 90%+ → critical.
  const tone =
    pct >= 90 ? 'critical' : pct >= 70 ? 'warning' : 'ok';

  return (
    <span
      role="status"
      aria-label={`Context pressure: ${pct.toFixed(1)}% of ${spec.label} window`}
      title={`${formatNumber(consumed)} / ${formatNumber(spec.contextWindow)} tokens (${pct.toFixed(1)}% of ${spec.label})`}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium',
        tone === 'ok' && 'bg-muted text-muted-foreground',
        tone === 'warning' && 'bg-status-running/10 text-status-running',
        tone === 'critical' && 'bg-status-failed/10 text-status-failed',
      )}
    >
      <Gauge className="size-3" aria-hidden />
      <span className="font-mono tabular-nums">{pct.toFixed(0)}%</span>
      <span className="text-[10px] uppercase tracking-wider">ctx</span>
    </span>
  );
}
