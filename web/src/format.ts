/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import type { DrillView, Result } from './api'

// relativeAge renders an RFC3339 timestamp as a compact "3d ago" / "5h ago".
export function relativeAge(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return 'never'
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return iso
  const sec = Math.max(0, Math.round((now - then) / 1000))
  if (sec < 60) return `${sec}s ago`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.round(min / 60)
  if (hr < 48) return `${hr}h ago`
  const day = Math.round(hr / 24)
  return `${day}d ago`
}

// untilNext renders a future RFC3339 timestamp as "in 3h" / "in 2d".
export function untilNext(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return '—'
  const then = Date.parse(iso)
  if (Number.isNaN(then)) return iso
  const sec = Math.round((then - now) / 1000)
  if (sec <= 0) return 'due'
  if (sec < 3600) return `in ${Math.round(sec / 60)}m`
  const hr = Math.round(sec / 3600)
  if (hr < 48) return `in ${hr}h`
  return `in ${Math.round(hr / 24)}d`
}

export function humanBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  const i = Math.min(units.length - 1, Math.floor(Math.log2(Math.abs(n)) / 10))
  const v = n / 1024 ** i
  return `${i === 0 ? v : v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

export function humanDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const sec = ms / 1000
  if (sec < 60) return `${sec.toFixed(1)}s`
  const m = Math.floor(sec / 60)
  const s = Math.round(sec % 60)
  if (m < 60) return `${m}m ${s}s`
  const h = Math.floor(m / 60)
  return `${h}h ${m % 60}m`
}

export function localTime(iso: string | undefined): string {
  if (!iso) return '—'
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return iso
  return new Date(t).toLocaleString()
}

// ProofState classifies a drill's headline proof against its SLA. "aging" is the
// amber warning band (past 75% of max_proof_age but not yet stale).
export type ProofState = 'proven' | 'aging' | 'stale' | 'never'

export function proofState(d: DrillView, now: number = Date.now()): ProofState {
  if (!d.last_proven) return 'never'
  if (d.stale) return 'stale'
  const sla = d.max_proof_age_seconds ?? 0
  if (sla > 0) {
    const ageSec = (now - Date.parse(d.last_proven)) / 1000
    if (ageSec > 0.75 * sla) return 'aging'
  }
  return 'proven'
}

// Tailwind class fragments keyed by semantic state. Kept here so the failure
// taxonomy (pass/fail/error/skipped, never/stale) has one color source of truth.
export const proofDot: Record<ProofState, string> = {
  proven: 'bg-emerald-500',
  aging: 'bg-amber-500',
  stale: 'bg-rose-500',
  never: 'bg-slate-500',
}

export const proofText: Record<ProofState, string> = {
  proven: 'text-emerald-400',
  aging: 'text-amber-400',
  stale: 'text-rose-400',
  never: 'text-slate-400',
}

export const resultColor: Record<Result, string> = {
  pass: 'bg-emerald-500',
  fail: 'bg-rose-500',
  error: 'bg-amber-500',
  skipped: 'bg-slate-600',
}

export const resultBadge: Record<Result, string> = {
  pass: 'bg-emerald-500/15 text-emerald-300 ring-emerald-500/30',
  fail: 'bg-rose-500/15 text-rose-300 ring-rose-500/30',
  error: 'bg-amber-500/15 text-amber-300 ring-amber-500/30',
  skipped: 'bg-slate-500/15 text-slate-300 ring-slate-500/30',
}
