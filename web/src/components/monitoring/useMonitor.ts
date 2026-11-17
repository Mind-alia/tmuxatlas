import { useCallback, useEffect, useRef, useState } from 'react'
import { rangeSeconds, type MonitorRange, type MonitorResponse, type MonitorSample } from './types'

const POLL_MS = 5000

export type MonitorStatus = 'loading' | 'ready' | 'unavailable' | 'error'

/**
 * Polls GET /api/monitor. The series is fetched once per range, then only
 * points newer than the last one received are requested and appended.
 * Polling pauses while the tab is hidden.
 */
export function useMonitor(range: MonitorRange, enabled = true) {
  const [data, setData] = useState<MonitorResponse | null>(null)
  const [series, setSeries] = useState<MonitorSample[]>([])
  const [status, setStatus] = useState<MonitorStatus>('loading')
  const [lastUpdate, setLastUpdate] = useState<number>(0)
  const [failed, setFailed] = useState(false)
  const [manualRefreshing, setManualRefreshing] = useState(false)
  const lastT = useRef(0)
  const rangeRef = useRef(range)

  const fetchOnce = useCallback(async (forceRefresh = false) => {
    if (!enabled) return
    const currentRange = rangeRef.current
    try {
      const url = `/api/monitor?range=${currentRange}&after=${lastT.current}${forceRefresh ? '&refresh=1' : ''}`
      const res = await fetch(url, { cache: 'no-store' })
      if (res.status === 404) {
        setStatus('unavailable')
        setManualRefreshing(false)
        return
      }
      if (!res.ok) throw new Error(String(res.status))
      const body = (await res.json()) as MonitorResponse
      if (rangeRef.current !== currentRange) return // range changed mid-flight
      if (!body.supported) {
        setStatus('unavailable')
        setManualRefreshing(false)
        return
      }
      const incoming = body.series ?? []
      const cutoff = Date.now() - rangeSeconds[currentRange] * 1000
      setSeries(prev => {
        const merged = lastT.current === 0 ? incoming : prev.concat(incoming)
        const firstKept = merged.findIndex(s => s.t >= cutoff)
        return firstKept <= 0 ? merged : merged.slice(firstKept)
      })
      if (incoming.length > 0) lastT.current = incoming[incoming.length - 1].t
      setData(body)
      setStatus('ready')
      setLastUpdate(Date.now())
      setFailed(false)
      setManualRefreshing(Boolean(body.quota_refreshing))
    } catch {
      setFailed(true)
      setManualRefreshing(false)
      setStatus(prev => (prev === 'ready' ? 'ready' : 'error'))
    }
  }, [enabled])

  const refreshing = manualRefreshing || Boolean(data?.quota_refreshing)

  const refresh = useCallback(() => fetchOnce(false), [fetchOnce])

  const refreshAll = useCallback(async () => {
    setManualRefreshing(true)
    await fetchOnce(true)
  }, [fetchOnce])

  useEffect(() => {
    if (!enabled) return
    rangeRef.current = range
    // The previous series stays on screen until the new range arrives.
    lastT.current = 0
    void fetchOnce()
  }, [range, fetchOnce, enabled])

  useEffect(() => {
    if (!enabled) return
    const pollMs = refreshing ? 2000 : POLL_MS
    let timer: ReturnType<typeof setInterval> | null = null
    const start = () => {
      if (timer === null) timer = setInterval(() => void fetchOnce(), pollMs)
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
  }, [fetchOnce, enabled, refreshing])

  return { data, series, status, lastUpdate, failed, refreshing, refresh, refreshAll }
}
