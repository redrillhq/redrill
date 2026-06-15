import type { Result } from '../api'
import { resultBadge } from '../format'

const isResult = (s: string): s is Result =>
  s === 'pass' || s === 'fail' || s === 'error' || s === 'skipped'

export function ResultBadge({ result }: { result: Result }) {
  return (
    <span
      className={`inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium uppercase tracking-wide ring-1 ring-inset ${resultBadge[result]}`}
    >
      {result}
    </span>
  )
}

// StatusPill colors a known result string; anything else (a step kind, say) shows
// neutral so the taxonomy stays honest rather than guessing a verdict.
export function StatusPill({ status }: { status: string }) {
  if (isResult(status)) return <ResultBadge result={status} />
  return (
    <span className="inline-flex items-center rounded-md bg-slate-500/15 px-2 py-0.5 text-xs font-medium uppercase tracking-wide text-slate-300 ring-1 ring-inset ring-slate-500/30">
      {status}
    </span>
  )
}

export function LevelBadge({ level, proven }: { level: string; proven: boolean }) {
  const cls = proven
    ? 'bg-emerald-500/10 text-emerald-300 ring-emerald-500/30'
    : 'bg-slate-700/40 text-slate-400 ring-slate-600/40'
  return (
    <span
      className={`inline-flex items-center rounded px-1.5 py-0.5 text-xs font-semibold uppercase ring-1 ring-inset ${cls}`}
      title={proven ? `${level} proven` : `${level} not yet proven`}
    >
      {level}
    </span>
  )
}

export function WeakBadge() {
  return (
    <span
      className="inline-flex items-center rounded bg-slate-700/40 px-1.5 py-0.5 text-[10px] font-medium uppercase text-slate-400 ring-1 ring-inset ring-slate-600/40"
      title="Weak comfort — not a strong guarantee on its own"
    >
      weak
    </span>
  )
}
