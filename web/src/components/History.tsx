import { api } from '../api'
import { useFetch } from '../useFetch'
import { Spinner, ErrorBanner, EmptyState, Panel } from './ui'
import { RunStrip } from './RunStrip'
import { ResultBadge } from './Badges'
import { navigate, links } from '../router'
import { localTime, humanDuration, humanBytes } from '../format'

export function History({ drill }: { drill: string }) {
  const { data, error, loading, reload } = useFetch((s) => api.runs(drill, 50, s), [drill])

  if (loading && !data) return <Spinner label="Loading history…" />
  if (error && !data) return <ErrorBanner message={error} onRetry={reload} />

  const runs = data ?? []

  return (
    <div className="space-y-5">
      <div>
        <button
          onClick={() => navigate(links.board)}
          className="text-sm text-slate-400 hover:text-slate-200"
        >
          ← Board
        </button>
        <h1 className="mt-1 font-mono text-2xl font-semibold text-slate-100">{drill}</h1>
      </div>

      <Panel title="Proof chain">
        {runs.length === 0 ? (
          <EmptyState>No runs yet.</EmptyState>
        ) : (
          <RunStrip runs={runs} size="lg" />
        )}
      </Panel>

      <Panel title={`Run history (${runs.length})`}>
        {runs.length === 0 ? (
          <EmptyState>No runs yet.</EmptyState>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="text-xs uppercase tracking-wide text-slate-500">
                <tr>
                  <th className="py-2 pr-4 font-medium">Run</th>
                  <th className="py-2 pr-4 font-medium">Result</th>
                  <th className="py-2 pr-4 font-medium">Level</th>
                  <th className="py-2 pr-4 font-medium">Trigger</th>
                  <th className="py-2 pr-4 font-medium">Started</th>
                  <th className="py-2 pr-4 font-medium">Duration</th>
                  <th className="py-2 pr-4 font-medium">Restored</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((r) => (
                  <tr
                    key={r.id}
                    onClick={() => navigate(links.run(r.id))}
                    className="cursor-pointer border-t border-slate-800 hover:bg-slate-800/40"
                  >
                    <td className="py-2 pr-4 font-mono text-slate-400">#{r.id}</td>
                    <td className="py-2 pr-4">
                      <ResultBadge result={r.result} />
                    </td>
                    <td className="py-2 pr-4 uppercase text-slate-400">{r.level_reached || '—'}</td>
                    <td className="py-2 pr-4 text-slate-400">{r.trigger}</td>
                    <td className="py-2 pr-4 text-slate-300">{localTime(r.started_at)}</td>
                    <td className="py-2 pr-4 text-slate-300">{humanDuration(r.duration_ms)}</td>
                    <td className="py-2 pr-4 text-slate-300">{humanBytes(r.bytes_restored)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </div>
  )
}
