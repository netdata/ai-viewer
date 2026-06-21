import { useParams, useSearchParams } from 'react-router-dom';
import { Pin, PinOff } from 'lucide-react';
import { LoadingState, ErrorState, EmptyState } from '../../components/StatusViews';
import { useSessionDetail } from '../../api/sessions';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { usePinned } from '../../state/pinned';
import { ApiError } from '../../api/client';
import { UnifiedView } from './UnifiedView';
import { SessionBreadcrumb } from './SessionBreadcrumb';
import { Button } from '../../components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger } from '../../components/ui/tooltip';

// Session detail page (ui-turn-view.md §ui-session-unified-view). The
// SOW-0078 through SOW-0087 era shipped this page as 7 tabs
// (Overview, Trace, Turns, Topology, Timeline, Logs, Raw). Per the
// operator's verbatim feedback ("I have the impression that all the
// different session views should only be one"), SOW-0088 chunk 4
// collapses every per-session view into the UnifiedView shell:
//   - OverviewTiles row (status / duration / tokens / cost / failures / context)
//   - Resizable body:
//       - Left column (viz tabs: Waterfall default + Topology + Timeline + Stats)
//         above an Event list / Logs / Raw bottom panel
//       - Right column: Turn View (reads ?op=<id> from URL, scrolls + pulses)
//
// The unified view is the only render mode of /sessions/:id. There is no
// "use the old tabs" escape hatch — the legacy tabs are GONE. (Existing
// bookmarks to ?tab=trace fall through to the unified view because the
// SessionDetail page ignores the `tab` query param now.)

export function SessionDetail() {
  const { id = '' } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  void searchParams; // legacy ?tab= is intentionally ignored

  const { data, isPending, isError, error } = useSessionDetail(id);
  void error; // referenced below via ApiError check
  const pinned = usePinned();
  void error;

  // Live refresh of the open session; session_changed invalidates ['session', id].
  useLiveUpdates({ session_id: id });

  const notFound = isError && error instanceof ApiError && error.status === 404;

  // Breadcrumb (SOW-0083 D1): show the session's position in the tree.
  const session = data?.session;
  const parentSessionId = session?.parent_session_id ?? null;

  return (
    <section aria-labelledby="session-detail-title" className="flex flex-col gap-4 px-6 py-5">
      <div>
        <SessionBreadcrumb
          parentSessionId={parentSessionId}
          currentLabel={session?.agent_name ? `${session.agent_name} (${id.slice(0, 8)}…)` : id}
        />
        <h1 id="session-detail-title" className="mt-3 text-2xl font-semibold tracking-tight">
          {session?.agent_name ?? 'Session detail'}
        </h1>
        <div className="mt-2">
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                onClick={() => { pinned.toggle(id); }}
                aria-pressed={pinned.isPinned(id)}
                aria-label={pinned.isPinned(id) ? 'Unpin this session' : 'Pin this session'}
              >
                {pinned.isPinned(id) ? (
                  <>
                    <PinOff className="size-3.5" aria-hidden />
                    Unpin
                  </>
                ) : (
                  <>
                    <Pin className="size-3.5" aria-hidden />
                    Pin
                  </>
                )}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {pinned.isPinned(id)
                ? 'Remove from your pinned list'
                : 'Pin this session — appears at the top of /sessions'}
            </TooltipContent>
          </Tooltip>
        </div>
        <p className="mt-1 flex items-center gap-2 font-mono text-xs text-muted-foreground">
          <span className="text-[10px] font-sans uppercase tracking-wider">id</span>
          <code className="rounded bg-muted px-1.5 py-0.5 text-foreground">{id}</code>
        </p>
      </div>

      {notFound ? (
        <div className="rounded-lg border border-dashed border-border bg-card/50 p-12">
          <EmptyState>Session not found.</EmptyState>
        </div>
      ) : isPending ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <LoadingState label="Loading session…" />
        </div>
      ) : isError ? (
        <div className="rounded-lg border border-border bg-card p-12">
          <ErrorState error={error} title="Failed to load session" />
        </div>
      ) : (
        <UnifiedView detail={data} />
      )}
    </section>
  );
}