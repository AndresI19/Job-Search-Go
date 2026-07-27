import { defineConfig } from 'vite';
import { resolve } from 'node:path';

// The URL prefix this app is mounted under. Defaults to the ROOT — on its own the
// Job Searcher is a whole page. The platform that hosts it behind a path supplies
// BASE_PATH=/job-searcher/ at build time, the SAME value the Go server uses at run
// time: Vite bakes it into the index.html asset URLs and import.meta.env.BASE_URL,
// the server mounts its routes beneath it, and a mismatch would 404 every asset.
const BASE = process.env.BASE_PATH || '/';

export default defineConfig({
  base: BASE,
  root: resolve(__dirname, 'web'),
  build: {
    outDir: resolve(__dirname, 'web/dist'),
    emptyOutDir: true,
  },
});
