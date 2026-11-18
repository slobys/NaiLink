import path from 'node:path'
import { fileURLToPath } from 'node:url'

import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'
import { version } from './package.json'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

export default defineConfig(() => {
  return {
    base: './',
    resolve: {
      alias: [
        { find: '~', replacement: path.resolve(__dirname, 'src') },
        {
          find: '@daeuniverse/dae-node-parser',
          replacement: path.resolve(__dirname, '../../packages/dae-node-parser/src/index.ts'),
        },
        {
          find: '@daeuniverse/dae-editor',
          replacement: path.resolve(__dirname, '../../packages/dae-editor/src/index.ts'),
        },
        // Keep the editing contributions. Routing and DNS register their own languages.
        { find: /^monaco-editor$/, replacement: 'monaco-editor/esm/vs/editor/edcore.main.js' },
      ],
    },
    plugins: [react(), tailwindcss()],
    build: {
      chunkSizeWarningLimit: 10 * 1024 * 1024,
    },
    define: {
      'import.meta.env.APP_VERSION': JSON.stringify(version),
    },
    test: { globals: true },
  }
})
