import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The Go binary serves the built SPA and the /api surface from the same
// origin, so production fetches are relative (`/api/...`). In dev, Vite
// proxies /api to the local ai-viewer-serve process so the same relative
// URLs work without CORS. The dev backend port matches the serve default
// documented in rest-api.md / sse-protocol.md (127.0.0.1:7710).
const DEV_API_TARGET = process.env.AI_VIEWER_API ?? 'http://127.0.0.1:7710';

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: DEV_API_TARGET,
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
});
