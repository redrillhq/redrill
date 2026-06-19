/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import { useEffect } from 'react'
import { api } from '../api'
import { useFetch } from '../useFetch'
import { DrillCard } from './DrillCard'
import { Spinner, ErrorBanner, EmptyState } from './ui'

export function Board() {
  const { data, error, loading, reload } = useFetch((s) => api.drills(s), [])

  useEffect(() => {
    const id = window.setInterval(reload, 20_000)
    return () => window.clearInterval(id)
  }, [reload])

  if (loading && !data) return <Spinner label="Loading drills…" />
  if (error && !data) return <ErrorBanner message={error} onRetry={reload} />

  const drills = data ?? []
  if (drills.length === 0) {
    return <EmptyState>No drills configured. Add some to the config and reload.</EmptyState>
  }

  const now = Date.now()
  const ok = drills.filter((d) => !d.stale).length
  const allOk = ok === drills.length

  return (
    <div className="space-y-5">
      <div
        className={`rounded-lg border px-4 py-3 text-sm font-medium ${
          allOk
            ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-300'
            : 'border-amber-500/30 bg-amber-500/10 text-amber-200'
        }`}
      >
        {ok} of {drills.length} datasets proven within SLA
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        {drills.map((d) => (
          <DrillCard key={d.drill} drill={d} now={now} />
        ))}
      </div>
    </div>
  )
}
