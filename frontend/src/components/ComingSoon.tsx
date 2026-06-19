import { Construction, Sparkles } from 'lucide-react';
import { cn } from '../lib/utils';

// Shared placeholder for Phase-2/3 routes scaffolded but not yet implemented
// (ui-pages.md §Phase Mapping). Renders a designed empty-state card so the
// route is navigable and obviously a placeholder rather than a broken page.

export function ComingSoon({
  title,
  note,
  description,
}: {
  title: string;
  note?: string;
  description?: string;
}) {
  return (
    <section
      aria-labelledby="coming-soon-title"
      className="flex flex-col gap-6 px-6 py-5"
    >
      <div>
        <h1
          id="coming-soon-title"
          className="text-2xl font-semibold tracking-tight"
        >
          {title}
        </h1>
        {description !== undefined ? (
          <p className="mt-1 text-sm text-muted-foreground">{description}</p>
        ) : null}
      </div>

      <div className="flex flex-col items-center justify-center rounded-lg border border-dashed border-border bg-card/50 px-6 py-16 text-center">
        <div className="grid size-12 place-items-center rounded-full bg-muted text-muted-foreground">
          <Construction className="size-6" aria-hidden />
        </div>
        <h2 className="mt-4 text-base font-semibold text-foreground">
          {note ?? 'This view is planned for a later phase.'}
        </h2>
        <p className="mt-1 max-w-md text-sm text-muted-foreground">
          We&apos;re shipping the foundational dashboards first (Sessions, Topology,
          Statistics, Sources). {title} arrives in a follow-up release with the
          same visual language and the same data plumbing.
        </p>
        <span
          className={cn(
            'mt-4 inline-flex items-center gap-1.5 rounded-full border border-border bg-muted px-2.5 py-0.5',
            'text-[10px] font-medium uppercase tracking-wider text-muted-foreground',
          )}
        >
          <Sparkles className="size-3" aria-hidden />
          Planned
        </span>
      </div>
    </section>
  );
}