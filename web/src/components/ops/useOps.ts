import { useCallback, useEffect, useState } from 'react'
import type { OpsResponse } from './types'

const POLL_MS = 15000

export type OpsStatus = 'loading' | 'ready' | 'forbidden' | 'error'

/** Polls GET /api/ops (admin only). Polling pauses while the tab is hidden. */
export function useOps() {
  const [data, setData] = useState<OpsResponse | null>(null)
  const [status, setStatus] = useState<OpsStatus>('loading')
  const [failed, setFailed] = useState(false)

  const fetchOnce = useCallback(async () => {
    try {
      const res = await fetch('/api/ops', { cache: 'no-store' })
      if (res.status === 401 || res.status === 403) {
        setStatus('forbidden')
        return
      }
      if (!res.ok) throw new Error(String(res.status))
      setData((await res.json()) as OpsResponse)
      setStatus('ready')
      setFailed(false)
    } catch {
      setFailed(true)
      setStatus(prev => (prev === 'ready' ? 'ready' : 'error'))
    }
  }, [])

  useEffect(() => {
    void fetchOnce()
    let timer: ReturnType<typeof setInterval> | null = null
    const start = () => {
      if (timer === null) timer = setInterval(() => void fetchOnce(), POLL_MS)
    }
    const stop = () => {
      if (timer !== null) clearInterval(timer)
      timer = null
    }
    const onVisibility = () => {
      if (document.hidden) stop()
      else {
        void fetchOnce()
        start()
      }
    }
    if (!document.hidden) start()
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      stop()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [fetchOnce])

  return { data, status, failed, refresh: fetchOnce }
}
