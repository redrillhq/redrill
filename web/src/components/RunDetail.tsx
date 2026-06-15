import type { ReactNode } from 'react'
import type { StepView } from '../api'
import { api } from '../api'
import { useFetch } from '../useFetch'
import { Spinner, ErrorBanner, EmptyState, Panel } from './ui'
import { ResultBadge, StatusPill, WeakBadge } from './Badges'
import { navigate, links } from '../router'
import { localTime, humanDuration, humanBytes } from '../format'

function stepDuration(s: StepView): string {
  if (!s.finished_at) return '—'
  const ms = Date.parse(s.finished_at) - Date.parse(s.started_at)
  return Number.isNaN(ms) ? '—' : humanDuration(Math.max(0, ms))
}

export function RunDetail({ id }: { id: number }) {
  const { data, error, loading, reload } = useFetch((s) => api.run(id, s), [id])

  if (loading && !data) return <Spinner label="Loading run…" />
  if (error && !data) return <ErrorBanner message={error} onRetry={reload} />
  if (!data) return null

  const run = data

  return (
    <div className="space-y-5">
      <div>
        <button
          onClick={() => navigate(links.history(run.drill))}
          className="text-sm text-slate-400 hover:text-slate-200"
        >
          ← {run.drill}
        </button>
        <div className="mt-1 flex flex-wrap items-center gap-3">
          <h1 className="font-mono text-2xl font-semibold text-slate-100">Run #{run.id}</h1>
          <ResultBadge result={run.result} />
          <span className="text-sm text-slate-500">via {run.trigger}</span>
        </div>
      </div>

      <dl className="grid grid-cols-2 gap-x-6 gap-y-2 rounded-lg border border-slate-800 bg-slate-900/60 p-4 text-sm sm:grid-cols-4">
        <Field label="Level reached">{run.level_reached || '—'}</Field>
        <Field label="Duration">{humanDuration(run.duration_ms)}</Field>
        <Field label="Bytes restored">{humanBytes(run.bytes_restored)}</Field>
        <Field label="Files restored">{run.files_restored.toLocaleString()}</Field>
        <Field label="Started">{localTime(run.started_at)}</Field>
        <Field label="Finished">{localTime(run.finished_at)}</Field>
      </dl>

      <Panel title="Steps">
        {run.steps.length === 0 ? (
          <EmptyState>No steps recorded.</EmptyState>
        ) : (
          <ol className="space-y-2">
            {run.steps.map((s) => (
              <li
                key={s.idx}
                className="flex items-center justify-between gap-3 rounded-md border border-slate-800 bg-slate-950/40 px-3 py-2"
              >
                <div className="flex items-center gap-3">
                  <StatusPill status={s.status} />
                  <span className="font-mono text-sm uppercase text-slate-300">{s.kind}</span>
                  {s.summary && <span className="text-sm text-slate-500">{s.summary}</span>}
                </div>
                <span className="text-xs text-slate-500">{stepDuration(s)}</span>
              </li>
            ))}
          </ol>
        )}
      </Panel>

      <Panel title="Evidence">
        {run.evidence.length === 0 ? (
          <EmptyState>No evidence rows.</EmptyState>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="text-xs uppercase tracking-wide text-slate-500">
                <tr>
                  <th className="py-2 pr-4 font-medium">Check</th>
                  <th className="py-2 pr-4 font-medium">Target</th>
                  <th className="py-2 pr-4 font-medium">Expected</th>
                  <th className="py-2 pr-4 font-medium">Actual</th>
                  <th className="py-2 pr-4 font-medium">Status</th>
                </tr>
              </thead>
              <tbody>
                {run.evidence.map((e) => (
                  <tr key={e.idx} className="border-t border-slate-800 align-top">
                    <td className="py-2 pr-4">
                      <span className="font-mono text-slate-300">{e.check_kind}</span>
                      {e.weak && (
                        <span className="ml-2 align-middle">
                          <WeakBadge />
                        </span>
                      )}
                    </td>
                    <td className="py-2 pr-4 font-mono text-xs text-slate-400">
                      {e.target || '—'}
                    </td>
                    <td className="py-2 pr-4 font-mono text-xs text-slate-300">
                      {e.expected || '—'}
                    </td>
                    <td className="py-2 pr-4 font-mono text-xs text-slate-300">
                      {e.actual || '—'}
                    </td>
                    <td className="py-2 pr-4">
                      <StatusPill status={e.status} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      {run.artifacts.length > 0 && (
        <Panel title="Artifacts">
          <ul className="space-y-1 text-sm">
            {run.artifacts.map((a) => (
              <li key={a.name} className="flex items-center justify-between">
                <span className="font-mono text-slate-300">{a.name}</span>
                <span className="text-slate-500">{humanBytes(a.bytes)}</span>
              </li>
            ))}
          </ul>
          <p className="mt-3 text-xs text-slate-600">
            Logs stay on the daemon host (redacted at capture); they are not downloadable over the
            API in this version.
          </p>
        </Panel>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-0.5 text-slate-200">{children}</dd>
    </div>
  )
}
