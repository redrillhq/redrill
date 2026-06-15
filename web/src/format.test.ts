/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import { describe, it, expect } from 'vitest'
import type { DrillView } from './api'
import { relativeAge, untilNext, humanBytes, humanDuration, proofState } from './format'

const now = Date.parse('2026-06-15T12:00:00Z')

describe('relativeAge', () => {
  it.each([
    [undefined, 'never'],
    ['2026-06-15T11:59:30Z', '30s ago'],
    ['2026-06-15T11:30:00Z', '30m ago'],
    ['2026-06-15T06:00:00Z', '6h ago'],
    ['2026-06-12T12:00:00Z', '3d ago'],
  ])('%s -> %s', (iso, want) => {
    expect(relativeAge(iso, now)).toBe(want)
  })

  it('clamps a future timestamp to 0s', () => {
    expect(relativeAge('2026-06-15T12:00:10Z', now)).toBe('0s ago')
  })
})

describe('untilNext', () => {
  it.each([
    [undefined, '—'],
    ['2026-06-15T11:00:00Z', 'due'],
    ['2026-06-15T12:30:00Z', 'in 30m'],
    ['2026-06-15T14:00:00Z', 'in 2h'],
    ['2026-06-18T12:00:00Z', 'in 3d'],
  ])('%s -> %s', (iso, want) => {
    expect(untilNext(iso, now)).toBe(want)
  })
})

describe('humanBytes', () => {
  it.each([
    [0, '0 B'],
    [512, '512 B'],
    [1024, '1.0 KiB'],
    [1536, '1.5 KiB'],
    [1073741824, '1.0 GiB'],
  ])('%d -> %s', (n, want) => {
    expect(humanBytes(n)).toBe(want)
  })
})

describe('humanDuration', () => {
  it.each([
    [1, '1ms'],
    [1500, '1.5s'],
    [65000, '1m 5s'],
    [3700000, '1h 1m'],
  ])('%d -> %s', (ms, want) => {
    expect(humanDuration(ms)).toBe(want)
  })
})

describe('proofState', () => {
  const base: DrillView = { drill: 'app-db', source: 'pg', stale: false }

  it('never when no proof exists', () => {
    expect(proofState(base, now)).toBe('never')
  })
  it('stale when the API says so', () => {
    expect(proofState({ ...base, stale: true, last_proven: '2026-05-01T00:00:00Z' }, now)).toBe(
      'stale',
    )
  })
  it('proven when fresh within SLA', () => {
    expect(
      proofState(
        { ...base, last_proven: '2026-06-15T11:00:00Z', max_proof_age_seconds: 864000 },
        now,
      ),
    ).toBe('proven')
  })
  it('aging past 75% of the SLA', () => {
    expect(
      proofState(
        { ...base, last_proven: '2026-06-06T12:00:00Z', max_proof_age_seconds: 864000 },
        now,
      ),
    ).toBe('aging')
  })
})
