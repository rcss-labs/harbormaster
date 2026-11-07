/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: { '/api': 'http://127.0.0.1:8480' },
  },
  build: {
    outDir: '../internal/web/dist',
    emptyOutDir: true,
    // The CSP (font-src 'self') blocks data: fonts, so woff files must be emitted as files, never inlined.
    assetsInlineLimit: (filePath) => (/\.woff2?$/.test(filePath) ? false : undefined),
  },
  test: {
    environment: 'jsdom',
    environmentOptions: { jsdom: { url: 'http://localhost:3000/' } },
    setupFiles: ['src/test/setup.ts'],
    // The first render of a heavy page in a file (charts, forms, a whole app tree) can pass the 5 s default when the machine is busy.
    testTimeout: 15_000,
    css: { modules: { classNameStrategy: 'non-scoped' } },
    exclude: ['e2e/**', 'e2e-floci/**', 'node_modules/**'],
  },
})
