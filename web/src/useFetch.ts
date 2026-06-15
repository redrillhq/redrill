/*
 * Copyright (C) 2026 Andrew Alyamovsky
 * SPDX-License-Identifier: AGPL-3.0-or-later
 */

import { useCallback, useEffect, useState } from 'react'

export interface FetchState<T> {
  data?: T
  error?: string
  loading: boolean
}

// useFetch runs an abortable loader, re-running when deps change or reload() is
// called. The loader receives the AbortSignal so an unmounted/again-fired request
// is dropped instead of racing a setState.
export function useFetch<T>(
  fn: (signal: AbortSignal) => Promise<T>,
  deps: unknown[],
): FetchState<T> & { reload: () => void } {
  const [state, setState] = useState<FetchState<T>>({ loading: true })
  const [nonce, setNonce] = useState(0)

  useEffect(() => {
    const ac = new AbortController()
    setState((s) => ({ ...s, loading: true, error: undefined }))
    fn(ac.signal)
      .then((data) => {
        if (!ac.signal.aborted) setState({ data, loading: false })
      })
      .catch((err: unknown) => {
        if (ac.signal.aborted) return
        setState({ error: err instanceof Error ? err.message : String(err), loading: false })
      })
    return () => ac.abort()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, nonce])

  const reload = useCallback(() => setNonce((n) => n + 1), [])
  return { ...state, reload }
}
