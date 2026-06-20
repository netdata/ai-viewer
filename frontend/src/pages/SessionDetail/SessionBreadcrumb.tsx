import { Link } from 'react-router-dom';
import { ChevronRight, ArrowLeft, Home } from 'lucide-react';
import { cn } from '../../lib/utils';

// SessionBreadcrumb — SOW-0083 D1
//
// Shows the session's position in the tree above the H1 on SessionDetail:
//   Sessions / [root agent name] / [parent agent name if different] / [current id]
//
// A root session (parent_session_id === null) renders just two segments.
// Each segment is a Link (chevron between them); the last segment is the
// current page (not a link, just text).

export interface SessionBreadcrumbProps {
  parentSessionId: string | null;
  /** Short label to show for the current session (typically the agent_name
   *  or a derived label like "Sub-session"). If omitted, the session id is
   *  used as the trailing segment. */
  currentLabel?: string;
  /** The page the operator likely came from; default = '/'. Used by the
   *  Back-to-list link next to the breadcrumb. */
  backHref?: string;
}

export function SessionBreadcrumb({
  parentSessionId,
  currentLabel,
  backHref = '/',
}: SessionBreadcrumbProps) {
  return (
    <div className="flex flex-wrap items-center gap-3">
      <Link
        to={backHref}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-3" aria-hidden />
        Back to sessions
      </Link>
      <span aria-hidden className="text-border">|</span>
      <nav aria-label="Breadcrumb" className="flex flex-wrap items-center gap-1 text-xs">
        <Link
          to="/"
          className="inline-flex items-center gap-1 text-muted-foreground hover:text-foreground"
          aria-label="Sessions home"
        >
          <Home className="size-3" aria-hidden />
          Sessions
        </Link>
        <ChevronSeparator />
        {parentSessionId !== null ? (
          <>
            <Link
              to={`/sessions/${encodeURIComponent(parentSessionId)}`}
              className="text-muted-foreground hover:text-foreground"
            >
              Sub-session
            </Link>
            <ChevronSeparator />
          </>
        ) : null}
        <span
          aria-current="page"
          className={cn(
            'max-w-[16rem] truncate font-mono text-foreground',
            currentLabel === undefined && 'text-muted-foreground',
          )}
          title={currentLabel}
        >
          {currentLabel ?? 'this session'}
        </span>
      </nav>
    </div>
  );
}

function ChevronSeparator() {
  return <ChevronRight aria-hidden className="size-3 shrink-0 text-muted-foreground/50" />;
}
