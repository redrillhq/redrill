/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

// Types mirror the JSON shapes served by internal/server (DESIGN §8.3). The UI is
// a thin client over that public API — every field here has a Go counterpart.

export type Result = 'pass' | 'fail' | 'error' | 'skipped'

export interface DrillView {
  drill: string
  source: string
  headline_level?: string
  stale: boolean
  max_proof_age_seconds?: number
  last_result?: Result
  level_reached?: string
  last_run_at?: string
  last_proven?: string
  next_run?: string
  proofs?: Record<string, string>
}

export interface RunView {
  id: number
  drill: string
  result: Result
  trigger: string
  level_reached: string
  started_at: string
  finished_at?: string
  duration_ms: number
  bytes_restored: number
  files_restored: number
}

export interface StepView {
  idx: number
  kind: string
  started_at: string
  finished_at?: string
  status: string
  summary?: string
}

export interface EvidenceView {
  idx: number
  check_kind: string
  target?: string
  expected?: string
  actual?: string
  status: Result
  weak?: boolean
}

export interface ArtifactView {
  name: string
  bytes: number
}

export interface RunDetail extends RunView {
  steps: StepView[]
  evidence: EvidenceView[]
  artifacts: ArtifactView[]
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const resp = await fetch(path, { signal, headers: { Accept: 'application/json' } })
  if (!resp.ok) {
    throw new ApiError(resp.status, await errorMessage(resp))
  }
  return (await resp.json()) as T
}

async function errorMessage(resp: Response): Promise<string> {
  try {
    const body = (await resp.json()) as { error?: string }
    if (body.error) return body.error
  } catch {
    // fall through to the status text
  }
  return resp.statusText || `HTTP ${resp.status}`
}

export type TriggerOutcome = 'started' | 'busy' | 'disabled' | 'error'

export const api = {
  drills: (signal?: AbortSignal) => getJSON<DrillView[]>('/api/v1/drills', signal),

  runs: (drill: string, n = 30, signal?: AbortSignal) =>
    getJSON<RunView[]>(`/api/v1/drills/${encodeURIComponent(drill)}/runs?n=${n}`, signal),

  run: (id: number, signal?: AbortSignal) => getJSON<RunDetail>(`/api/v1/runs/${id}`, signal),

  async trigger(drill: string): Promise<TriggerOutcome> {
    const resp = await fetch(`/api/v1/drills/${encodeURIComponent(drill)}/run`, { method: 'POST' })
    switch (resp.status) {
      case 202:
        return 'started'
      case 409:
        return 'busy'
      case 503:
        return 'disabled'
      default:
        return 'error'
    }
  },
}
