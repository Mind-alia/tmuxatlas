import { useCallback, useEffect, useRef, useState } from 'react'
import { DockerApiError, fetchContainers } from './api'
import type { DockerList, DockerStats } from './types'

export const POLL_MS = 3000
/** Samples kept per container for the charts: 5 minutes at one every 3 s. */
export const HISTORY_SIZE = 100

export type DockerStatus = 'loading' | 'ready' | 'forbidden' | 'error'

export type StatsHistory = ReadonlyMap<string, readonly DockerStats[]>

/** Adds the new samples of a listing to the history, dropping removed containers. */
export function appendHistory(previous: StatsHistory, list: DockerList): StatsHistory {
  const next = new Map<string, readonly DockerStats[]>()
  for (const c of list.containers ?? []) {
    const kept = previous.get(c.id) ?? []
    if (!c.stats) {
      if (kept.length) next.set(c.id, kept)
      continue
    }
    const last = kept[kept.length - 1]
    next.set(c.id, last && last.at === c.stats.at ? kept : [...kept, c.stats].slice(-HISTORY_SIZE))
  }
  return next
}

/**
 * Polls GET /api/docker/containers every 3 s while the page is visible. A
 * new request only starts once the previous one has answered, and none runs
 * while the tab is hidden or after unmount.
 */
export function useDockerContainers(intervalMs = POLL_MS) {
  const [data, setData] = useState<DockerList | null>(null)
  const [history, setHistory] = useState<StatsHistory>(() => new Map())
  const [status, setStatus] = useState<DockerStatus>('loading')
  const [failure, setFailure] = useState<string | null>(null)
  const controllerRef = useRef<AbortController | null>(null)
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const fetchOnce = useCallback(async () => {
    controllerRef.current?.abort()
    const controller = new AbortController()
    controllerRef.current = controller
    try {
      const list = await fetchContainers(controller.signal)
      if (controller.signal.aborted) return
      setData(list)
      setHistory(previous => appendHistory(previous, list))
      setStatus('ready')
      setFailure(null)
    } catch (error) {
      if (controller.signal.aborted) return
      if (error instanceof DockerApiError && (error.status === 401 || error.status === 403)) {
        setStatus('forbidden')
        return
      }
      setFailure(error instanceof Error ? error.message : 'Erreur inconnue')
      setStatus(previous => (previous === 'ready' ? 'ready' : 'error'))
    } finally {
      if (controllerRef.current === controller) controllerRef.current = null
    }
  }, [])

  useEffect(() => {
    // Each start of the loop gets a generation: an older loop never
    // schedules again, so visibility changes cannot double the polling.
    let generation = 0
    let active = true
    const clear = () => {
      if (timerRef.current !== null) clearTimeout(timerRef.current)
      timerRef.current = null
    }
    const tick = async (loop: number) => {
      clear()
      await fetchOnce()
      if (active && loop === generation && document.visibilityState !== 'hidden') {
        timerRef.current = setTimeout(() => void tick(loop), intervalMs)
      }
    }
    const onVisibility = () => {
      generation++
      clear()
      if (document.visibilityState === 'hidden') controllerRef.current?.abort()
      else void tick(generation)
    }
    if (document.visibilityState !== 'hidden') void tick(generation)
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      active = false
      generation++
      clear()
      controllerRef.current?.abort()
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [fetchOnce, intervalMs])

  return { data, history, status, failure, refresh: fetchOnce }
}
