import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from '../ui/command';
import {
  LayoutGrid,
  Network,
  BarChart3,
  Database,
  Bot,
  Brain,
  Wrench,
  Moon,
  Sun,
  Monitor,
  Eye,
} from 'lucide-react';

// CommandPalette — global ⌘K palette. Provides navigation to every primary
// and secondary page, plus theme switching. Search matches the page label
// and the description text.
//
// The palette is mounted once at the app root (in Layout) and opens via
// `open` prop. The parent owns the open state; keyboard shortcut handling
// lives here via useEffect on document keydown.
//
// SOW-0073: this is the foundation; further SOWs will add session-jump,
// filter-set, and content-search commands.

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (next: boolean) => void }) {
  const navigate = useNavigate();

  // ⌘K / Ctrl-K to toggle. Plain Esc closes via the dialog's own handling.
  useEffect(() => {
    const handler = (e: KeyboardEvent): void => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        onOpenChange(!open);
      }
    };
    document.addEventListener('keydown', handler);
    return () => { document.removeEventListener('keydown', handler); };
  }, [open, onOpenChange]);

  const go = (path: string) => (): void => {
    onOpenChange(false);
    void navigate(path);
  };

  // Theme commands resolve via the dark/light document attribute so we don't
  // need to import the theme provider here; the data-theme persistence
  // lives in the existing state/theme.ts.
  const setTheme = (theme: 'auto' | 'dark' | 'light'): (() => void) => {
    return (): void => {
      onOpenChange(false);
      if (theme === 'auto') {
        try {
          window.localStorage.removeItem('aiViewerTheme');
        } catch {
          /* ignore */
        }
        const dark = window.matchMedia('(prefers-color-scheme: dark)').matches;
        document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
      } else {
        try {
          window.localStorage.setItem('aiViewerTheme', theme);
        } catch {
          /* ignore */
        }
        document.documentElement.setAttribute('data-theme', theme);
      }
    };
  };

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange} title="Command palette" description="Navigate, search, switch theme">
      <CommandInput placeholder="Type a command or search…" />
      <CommandList>
        <CommandEmpty>No results.</CommandEmpty>

        <CommandGroup heading="Navigate">
          <NavRow Icon={LayoutGrid} label="Sessions" hint="All sessions, live + history" onSelect={go('/')} shortcut="G S" />
          <NavRow Icon={Network} label="Topology" hint="Actor + tool call graph" onSelect={go('/topology')} shortcut="G T" />
          <NavRow Icon={BarChart3} label="Statistics" hint="Cost, tokens, failures over time" onSelect={go('/stats')} shortcut="G A" />
          <NavRow Icon={Database} label="Sources" hint="Configured source paths and health" onSelect={go('/sources')} shortcut="G O" />
          <NavRow Icon={Bot} label="Agents" hint="Agent-name drill-down" onSelect={go('/agents')} />
          <NavRow Icon={Brain} label="Models" hint="Model usage breakdown" onSelect={go('/models')} />
          <NavRow Icon={Wrench} label="Tools" hint="Tool usage breakdown" onSelect={go('/tools')} />
        </CommandGroup>

        <CommandGroup heading="Theme">
          <ActionRow Icon={Monitor} label="Auto (follow OS)" onSelect={setTheme('auto')} />
          <ActionRow Icon={Moon} label="Dark" onSelect={setTheme('dark')} />
          <ActionRow Icon={Sun} label="Light" onSelect={setTheme('light')} />
        </CommandGroup>

        <CommandGroup heading="About">
          <ActionRow Icon={Eye} label="ai-viewer" hint="Read-only AI session explorer" onSelect={() => undefined} />
        </CommandGroup>
      </CommandList>
    </CommandDialog>
  );
}

function NavRow({
  Icon,
  label,
  hint,
  onSelect,
  shortcut,
}: {
  Icon: typeof LayoutGrid;
  label: string;
  hint?: string;
  onSelect: () => void;
  shortcut?: string;
}) {
  return (
    <CommandItem onSelect={onSelect} value={`${label} ${hint ?? ''}`}>
      <Icon className="size-4 text-muted-foreground" aria-hidden />
      <span className="flex-1 truncate">{label}</span>
      {hint ? <span className="hidden text-xs text-muted-foreground sm:inline">{hint}</span> : null}
      {shortcut ? <CommandShortcut>{shortcut}</CommandShortcut> : null}
    </CommandItem>
  );
}

function ActionRow({
  Icon,
  label,
  hint,
  onSelect,
}: {
  Icon: typeof Moon;
  label: string;
  hint?: string;
  onSelect: () => void;
}) {
  return (
    <CommandItem onSelect={onSelect} value={`${label} ${hint ?? ''}`}>
      <Icon className="size-4 text-muted-foreground" aria-hidden />
      <span className="flex-1 truncate">{label}</span>
      {hint ? <span className="hidden text-xs text-muted-foreground sm:inline">{hint}</span> : null}
    </CommandItem>
  );
}

// Helper hook for consumers that just want the boolean state.
export function useCommandPaletteState(): [boolean, (next: boolean) => void] {
  return useState(false);
}