/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The build output is embedded into the Go binary via web/embed.go (go:embed dist).
// In dev, `npm run dev` proxies API/infra routes to a locally running `redrill serve`.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': 'http://localhost:8090',
      '/healthz': 'http://localhost:8090',
      '/metrics': 'http://localhost:8090',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test-setup.ts',
    css: false,
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
