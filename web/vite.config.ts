/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5173,
    // The Go server serves the API (and the built app) on :8080.
    proxy: { '/api': 'http://localhost:8080', '/healthz': 'http://localhost:8080' },
  },
  test: { environment: 'jsdom' },
})
