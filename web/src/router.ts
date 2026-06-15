/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import { useSyncExternalStore } from 'react'

// Hash routing keeps the server side trivial: it only ever serves index.html, so
// deep links survive any reverse-proxy setup with no SPA-fallback config.
export type Route =
  | { name: 'board' }
  | { name: 'history'; drill: string }
  | { name: 'run'; id: number }

function subscribe(cb: () => void): () => void {
  window.addEventListener('hashchange', cb)
  return () => window.removeEventListener('hashchange', cb)
}

function currentHash(): string {
  return window.location.hash.replace(/^#/, '') || '/'
}

export function usePath(): string {
  return useSyncExternalStore(subscribe, currentHash, currentHash)
}

export function navigate(path: string): void {
  window.location.hash = path
}

export function parseRoute(path: string): Route {
  const parts = path.replace(/^\/+/, '').split('/')
  if (parts[0] === 'drills' && parts[1]) {
    return { name: 'history', drill: decodeURIComponent(parts[1]) }
  }
  if (parts[0] === 'runs' && parts[1]) {
    const id = Number(parts[1])
    if (Number.isInteger(id) && id > 0) return { name: 'run', id }
  }
  return { name: 'board' }
}

export const links = {
  board: '/',
  history: (drill: string) => `/drills/${encodeURIComponent(drill)}`,
  run: (id: number) => `/runs/${id}`,
}
