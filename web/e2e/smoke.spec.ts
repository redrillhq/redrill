/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import { test, expect, type Page } from '@playwright/test'

const drill = {
  drill: 'app-db',
  source: 'pg-dumps',
  headline_level: 'l1',
  stale: false,
  max_proof_age_seconds: 864000,
  last_proven: '2026-06-15T00:00:00Z',
  proofs: { l1: '2026-06-15T00:00:00Z' },
}

const run = {
  id: 1,
  drill: 'app-db',
  result: 'pass',
  trigger: 'schedule',
  level_reached: 'l1',
  started_at: '2026-06-15T00:00:00Z',
  finished_at: '2026-06-15T00:00:02Z',
  duration_ms: 1500,
  bytes_restored: 1000,
  files_restored: 5,
}

const runDetail = {
  ...run,
  steps: [
    {
      idx: 0,
      kind: 'l1',
      started_at: run.started_at,
      finished_at: run.finished_at,
      status: 'pass',
      summary: 'L1 ok',
    },
  ],
  evidence: [
    {
      idx: 0,
      check_kind: 'compression_test',
      target: 'dump.gz',
      expected: 'valid gzip',
      actual: 'ok',
      status: 'pass',
    },
  ],
  artifacts: [],
}

// One route handler stubs the whole API by path + method.
async function mockApi(page: Page) {
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    const method = route.request().method()
    if (path === '/api/v1/drills') return route.fulfill({ json: [drill] })
    if (/^\/api\/v1\/drills\/[^/]+\/runs$/.test(path)) return route.fulfill({ json: [run] })
    if (/^\/api\/v1\/runs\/\d+$/.test(path)) return route.fulfill({ json: runDetail })
    if (method === 'POST' && /^\/api\/v1\/drills\/[^/]+\/run$/.test(path)) {
      return route.fulfill({ status: 202, json: { status: 'started' } })
    }
    return route.fulfill({ status: 404, json: { error: 'not mocked' } })
  })
}

test.beforeEach(async ({ page }) => {
  await mockApi(page)
})

test('board → history → run detail', async ({ page }) => {
  await page.goto('/')

  await expect(page.getByText('1 of 1 datasets proven within SLA')).toBeVisible()
  await expect(page.getByText('app-db')).toBeVisible()

  // Drill → history (proof chain + run table).
  await page.getByText('app-db').click()
  await expect(page.getByText('Proof chain')).toBeVisible()

  // Run row → run detail with the expected/actual evidence.
  await page.getByText('#1').click()
  await expect(page.getByText('Run #1')).toBeVisible()
  await expect(page.getByText('compression_test')).toBeVisible()
  await expect(page.getByText('valid gzip')).toBeVisible()
})

test('"Run now" triggers a run', async ({ page }) => {
  await page.goto('/')
  await page.getByRole('button', { name: 'Run now' }).click()
  await expect(page.getByText('Run started')).toBeVisible()
})
