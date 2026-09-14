import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath, URL } from 'node:url';

import { defineConfig } from 'vite';

const webRoot = fileURLToPath(new URL('./web', import.meta.url));
const outDir = fileURLToPath(new URL('./server/web/dist', import.meta.url));

export default defineConfig({
  root: webRoot,
  base: '/console/',
  plugins: [
    {
      name: 'slider-go-template-paths',
      closeBundle() {
        for (const fileName of ['auth.html', 'console.html']) {
          const filePath = join(outDir, fileName);
          const html = readFileSync(filePath, 'utf8');
          writeFileSync(
            filePath,
            html.replaceAll('/console/assets/', '{{.ConsoleAssetsPath}}'),
          );
        }
      },
    },
  ],
  build: {
    emptyOutDir: true,
    license: {
      fileName: 'assets/licenses.md',
    },
    outDir,
    rollupOptions: {
      input: {
        auth: fileURLToPath(new URL('./web/auth.html', import.meta.url)),
        console: fileURLToPath(new URL('./web/console.html', import.meta.url)),
      },
    },
    sourcemap: false,
  },
});
