/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import type { ReactNode } from 'react'

export function Spinner({ label = 'Loading…' }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 py-12 text-slate-400">
      <span className="h-4 w-4 animate-spin rounded-full border-2 border-slate-600 border-t-slate-300" />
      {label}
    </div>
  )
}

export function ErrorBanner({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-rose-500/40 bg-rose-500/10 px-4 py-3 text-rose-200">
      <span>{message}</span>
      {onRetry && (
        <button
          onClick={onRetry}
          className="rounded-md border border-rose-400/40 px-2 py-1 text-sm hover:bg-rose-500/20"
        >
          Retry
        </button>
      )}
    </div>
  )
}

export function EmptyState({ children }: { children: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-800 px-6 py-12 text-center text-slate-500">
      {children}
    </div>
  )
}

export function Panel({ title, children }: { title?: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-slate-800 bg-slate-900/60">
      {title && (
        <h2 className="border-b border-slate-800 px-4 py-3 text-sm font-semibold text-slate-300">
          {title}
        </h2>
      )}
      <div className="p-4">{children}</div>
    </section>
  )
}
