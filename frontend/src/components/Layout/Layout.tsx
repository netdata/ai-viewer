import { useState } from 'react';
import { Outlet } from 'react-router-dom';
import { Sheet, SheetContent, SheetTrigger, SheetHeader, SheetTitle } from '../ui/sheet';
import { Button } from '../ui/button';
import { SlidersHorizontal } from 'lucide-react';
import { FilterBar } from '../FilterBar';
import { AppSidebar } from './AppSidebar';
import { AppTopbar } from './AppTopbar';
import { useLiveUpdates } from '../../state/useLiveUpdates';
import { filtersToSubscription } from '../../state/filters';
import { useHealth } from '../../api/sources';

// App shell (SOW-0073). Sidebar (left) + Topbar (top) + content area. The
// sidebar is permanent on lg+ and collapses into a Sheet on mobile. The
// topbar holds page title, live indicator, theme menu, and command-palette
// trigger. The ⌘K palette itself is mounted once at the root.
//
// The filter bar still lives in the header area on desktop (the existing
// FilterBar component); on mobile it moves into the mobile sidebar sheet.
// We keep the existing FilterBar unchanged for now — it will be migrated
// in a follow-up chunk.

const APP_VERSION = '0.1.0';

export function Layout() {
  // One SSE subscription for the active filter; keeps the live indicator
  // honest regardless of which page is mounted.
  const sseStatus = useLiveUpdates(filtersToSubscription({ agents: [], models: [], tools: [], sources: [], status: [], q: '' }));
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const { data: health } = useHealth();
  const healthOk = (health?.status ?? 'ok') !== 'degraded';

  return (
    <div className="flex h-dvh w-full overflow-hidden bg-background text-foreground">
      {/* Desktop sidebar */}
      <div className="hidden lg:flex">
        <AppSidebar healthOk={healthOk} version={APP_VERSION} onNavigate={undefined} />
      </div>

      {/* Mobile sidebar sheet */}
      <Sheet open={mobileSidebarOpen} onOpenChange={setMobileSidebarOpen}>
        <SheetContent side="left" className="w-60 p-0" showCloseButton={false}>
          <AppSidebar
            healthOk={healthOk}
            version={APP_VERSION}
            onNavigate={() => { setMobileSidebarOpen(false); }}
          />
        </SheetContent>
      </Sheet>

      {/* Main column: topbar + content */}
      <div className="flex min-w-0 flex-1 flex-col">
        <AppTopbar
          sseStatus={sseStatus}
          onOpenMobileSidebar={() => { setMobileSidebarOpen(true); }}
          onOpenCommandPalette={undefined}
          rightSlot={<FilterSheet />}
        />
        <main className="min-h-0 flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

// FilterSheet — wraps the existing FilterBar in a side sheet so the filter
// controls are reachable from the topbar without taking up global chrome.
// Will be replaced with a redesigned filter panel in a follow-up chunk.
function FilterSheet() {
  return (
    <Sheet>
      <SheetTrigger asChild>
        <Button variant="outline" size="sm" className="gap-2">
          <SlidersHorizontal className="size-4" aria-hidden />
          <span>Filters</span>
        </Button>
      </SheetTrigger>
      <SheetContent side="right" className="w-[28rem] max-w-full overflow-y-auto p-0">
        <SheetHeader className="border-b border-border px-4 py-3">
          <SheetTitle>Filters</SheetTitle>
        </SheetHeader>
        <div className="p-4">
          <FilterBar />
        </div>
      </SheetContent>
    </Sheet>
  );
}