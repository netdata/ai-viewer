import { Route, Routes } from 'react-router-dom';
import { Layout } from './components/Layout';
import { SessionsList } from './pages/SessionsList';
import { SessionDetail } from './pages/SessionDetail';
import { Sources } from './pages/Sources';
import { Topology } from './pages/Topology';
import { Stats } from './pages/Stats';
import { NotFound } from './pages/NotFound';

// Route table. Every route nests under Layout (header + global FilterBar +
// content outlet). Real routes: /, /sessions/:id, /sources, /topology (the
// cross-session topology page), and /stats (the analytics dashboard —
// line/bar charts + deep search).
export function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<SessionsList />} />
        <Route path="sessions/:id" element={<SessionDetail />} />
        <Route path="sources" element={<Sources />} />
        <Route path="topology" element={<Topology />} />
        <Route path="stats" element={<Stats />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
