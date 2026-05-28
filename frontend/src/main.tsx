import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { App } from './App';
import { ThemeProvider } from './state/theme';
import { createAppQueryClient } from './api/queryClient';
import './theme/tokens.css';
import './theme/global.css';

// App entry. Provider order: QueryClient (server state) → Theme (data-theme on
// <html>) → Router → App. The no-flash theme script in index.html has already
// set the initial data-theme; ThemeProvider takes over and keeps it in sync.

const container = document.getElementById('root');
if (container === null) {
  throw new Error('root element not found');
}

const queryClient = createAppQueryClient();

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
);
