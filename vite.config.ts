import { fileURLToPath, URL } from 'node:url';

import { defineConfig } from 'vite';

const webRoot = fileURLToPath(new URL('./web', import.meta.url));

export default defineConfig({
  root: webRoot,
  base: '/console/',
  build: {
    emptyOutDir: true,
    license: {
      fileName: 'assets/licenses.md',
    },
    outDir: fileURLToPath(new URL('./server/web/dist', import.meta.url)),
    rollupOptions: {
      input: {
        auth: fileURLToPath(new URL('./web/auth.html', import.meta.url)),
        console: fileURLToPath(new URL('./web/console.html', import.meta.url)),
      },
    },
    sourcemap: false,
  },
});
