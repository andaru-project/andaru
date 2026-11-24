// SPDX-License-Identifier: AGPL-3.0-or-later
import { existsSync, renameSync } from 'node:fs';
import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';
import { defineConfig, type Plugin } from 'vite';

function renameNetplaneHtml(): Plugin {
  return {
    name: 'rename-netplane-html',
    apply: 'build',
    closeBundle() {
      const outDir = resolve(import.meta.dirname, 'dist-netplane');
      const src = resolve(outDir, 'netplane.html');
      if (existsSync(src)) {
        renameSync(src, resolve(outDir, 'index.html'));
      }
    },
  };
}

export default defineConfig({
  // Relative base ensures Home Assistant Ingress (/api/hassio_ingress/<token>/) and standalone (/) both work.
  base: './',
  plugins: [react(), renameNetplaneHtml()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8099',
    },
  },
  build: {
    outDir: 'dist-netplane',
    emptyOutDir: true,
    sourcemap: false,
    rollupOptions: {
      input: resolve(import.meta.dirname, 'netplane.html'),
    },
  },
});
