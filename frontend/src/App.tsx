import { Route, Routes } from 'react-router-dom';
import { Layout } from './components/Layout';
import { SessionsList } from './pages/SessionsList';
import { SessionDetail } from './pages/SessionDetail';
import { Sources } from './pages/Sources';
import { Topology } from './pages/Topology';
import { Tools } from './pages/Tools';
import { Models } from './pages/Models';
import { Agents } from './pages/Agents';
import { NotFound } from './pages/NotFound';

// Route table. Every route nests under Layout (header + global FilterBar +
// content outlet). Phase-1 routes (/, /sessions/:id, /sources) are real; the
// Phase-2/3 routes (/topology, /tools, /models, /agents) render placeholders.
// The router PROVIDER lives in main.tsx so this <Routes> tree can be mounted
// under a MemoryRouter in tests.
export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<SessionsList />} />
        <Route path="sessions/:id" element={<SessionDetail />} />
        <Route path="sources" element={<Sources />} />
        <Route path="topology" element={<Topology />} />
        <Route path="tools" element={<Tools />} />
        <Route path="models" element={<Models />} />
        <Route path="agents" element={<Agents />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
