import { useState } from 'react'
import type { ReactNode } from 'react'
import type { DrillView, TriggerOutcome } from '../api'
import { api } from '../api'
import { useFetch } from '../useFetch'
import {
  proofState,
  proofDot,
  proofText,
  relativeAge,
  untilNext,
  humanBytes,
  humanDuration,
} from '../format'
import { navigate, links } from '../router'
import { LevelBadge, ResultBadge } from './Badges'
import { RunStrip } from './RunStrip'

const LEVELS = ['l1', 'l2', 'l3']

export function DrillCard({ drill, now }: { drill: DrillView; now: number }) {
  const runsState = useFetch((s) => api.runs(drill.drill, 20, s), [drill.drill])
  const runs = runsState.data ?? []
  const latest = runs[0]
  const state = proofState(drill, now)
  const provenLevels = new Set(Object.keys(drill.proofs ?? {}))

  const [triggering, setTriggering] = useState(false)
  const [toast, setToast] = useState<string>()

  async function runNow() {
    setTriggering(true)
    setToast(undefined)
    const outcome = await api.trigger(drill.drill)
    setTriggering(false)
    setToast(triggerMessage(outcome))
    if (outcome === 'started') {
      window.setTimeout(() => runsState.reload(), 2000)
    }
  }

  return (
    <div className="flex flex-col gap-3 rounded-xl border border-slate-800 bg-slate-900/60 p-4">
      <div className="flex items-start justify-between gap-2">
        <button onClick={() => navigate(links.history(drill.drill))} className="text-left">
          <div className="font-semibold text-slate-100 hover:text-white">{drill.drill}</div>
          <div className="text-xs text-slate-500">{drill.source}</div>
        </button>
        <div className="flex shrink-0 gap-1">
          {LEVELS.filter((l) => provenLevels.has(l) || drill.headline_level === l).map((l) => (
            <LevelBadge key={l} level={l} proven={provenLevels.has(l)} />
          ))}
        </div>
      </div>

      <div className="flex items-center gap-2">
        <span className={`h-2.5 w-2.5 rounded-full ${proofDot[state]}`} />
        <span className={`text-sm font-medium ${proofText[state]}`}>
          {state === 'never'
            ? 'Never proven'
            : `Last proven: ${relativeAge(drill.last_proven, now)}`}
        </span>
      </div>

      <RunStrip runs={runs} />

      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs">
        <Stat label="Last run">{latest ? <ResultBadge result={latest.result} /> : '—'}</Stat>
        <Stat label="Next run">{untilNext(drill.next_run, now)}</Stat>
        <Stat label="Duration">{latest ? humanDuration(latest.duration_ms) : '—'}</Stat>
        <Stat label="Restored">{latest ? humanBytes(latest.bytes_restored) : '—'}</Stat>
      </dl>

      <div className="mt-auto flex items-center justify-between gap-2 pt-1">
        <button
          onClick={runNow}
          disabled={triggering}
          className="rounded-md bg-slate-100 px-3 py-1.5 text-sm font-medium text-slate-900 transition hover:bg-white disabled:opacity-50"
        >
          {triggering ? 'Starting…' : 'Run now'}
        </button>
        {toast && <span className="text-xs text-slate-400">{toast}</span>}
      </div>
    </div>
  )
}

function Stat({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between">
      <dt className="text-slate-500">{label}</dt>
      <dd className="font-medium text-slate-300">{children}</dd>
    </div>
  )
}

function triggerMessage(outcome: TriggerOutcome): string {
  switch (outcome) {
    case 'started':
      return 'Run started'
    case 'busy':
      return 'A run is already in flight'
    case 'disabled':
      return 'Triggering is disabled'
    case 'error':
      return 'Failed to start run'
  }
}
