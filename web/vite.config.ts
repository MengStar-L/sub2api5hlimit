import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { writeFile } from 'node:fs/promises'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [
    vue(),
    {
      name: 'preserve-embedded-output-directory',
      closeBundle: () => writeFile(new URL('../internal/webui/dist/.gitkeep', import.meta.url), ''),
    },
  ],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:2556',
      '/healthz': 'http://127.0.0.1:2556',
      '/readyz': 'http://127.0.0.1:2556',
    },
  },
})
