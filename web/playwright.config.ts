import { defineConfig, devices } from '@playwright/test'

// Runs the e2e specs against the production build (vite preview serves web/dist).
// The API is route-mocked in the specs, so no Go backend is needed — this exercises
// rendering, routing, and the click flows in a real browser.
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  use: {
    baseURL: 'http://localhost:4173',
    trace: 'on-first-retry',
  },
  webServer: {
    command: 'npm run preview',
    url: 'http://localhost:4173',
    reuseExistingServer: true,
    timeout: 60_000,
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
