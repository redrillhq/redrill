import type { RunView } from '../api'
import { resultColor, humanDuration, localTime } from '../format'
import { navigate, links } from '../router'

// RunStrip renders runs as a row of colored cells (oldest → newest) — the proof
// chain whose unbroken green is the psychological hook. Each cell links to its run.
export function RunStrip({ runs, size = 'sm' }: { runs: RunView[]; size?: 'sm' | 'lg' }) {
  if (runs.length === 0) {
    return <div className="text-xs text-slate-600">no runs yet</div>
  }
  const ordered = [...runs].reverse()
  const dims = size === 'lg' ? 'h-8 w-4' : 'h-5 w-2.5'
  return (
    <div className="flex flex-wrap items-end gap-1">
      {ordered.map((r) => (
        <button
          key={r.id}
          onClick={() => navigate(links.run(r.id))}
          title={`#${r.id} · ${r.result} · ${localTime(r.finished_at ?? r.started_at)} · ${humanDuration(r.duration_ms)}`}
          className={`${dims} rounded-sm ${resultColor[r.result]} opacity-80 transition hover:opacity-100`}
          aria-label={`run ${r.id} ${r.result}`}
        />
      ))}
    </div>
  )
}
