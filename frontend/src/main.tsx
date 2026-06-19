import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { TooltipProvider } from './components/ui/tooltip';
import { App } from './App';
import { ThemeProvider } from './state/theme';
import { createAppQueryClient } from './api/queryClient';
import './theme/app.css';

// App entry. Provider order: QueryClient (server state) → Theme (data-theme on
// <html>) → TooltipProvider (Radix tooltips for the redesigned shell + table
// affordances) → Router → App. The no-flash theme script in index.html has
// already set the initial data-theme; ThemeProvider takes over and keeps it in
// sync.

const container = document.getElementById('root');
if (container === null) {
  throw new Error('root element not found');
}

const queryClient = createAppQueryClient();

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <TooltipProvider delayDuration={150} skipDelayDuration={300}>
          <BrowserRouter>
            <App />
          </BrowserRouter>
        </TooltipProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
);
