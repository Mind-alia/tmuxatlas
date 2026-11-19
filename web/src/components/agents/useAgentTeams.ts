import { useCallback, useEffect, useState } from 'react'
import type { AgentTeam, AgentTeamsResponse } from './types'

const POLL_MS = 3000

export type AgentTeamsStatus = 'loading' | 'ready' | 'forbidden' | 'error'

/** Polls GET /api/agent-teams (admin only). Polling pauses while the tab is hidden. */
export function useAgentTeams() {
  const [teams, setTeams] = useState<AgentTeam[]>([])
  const [status, setStatus] = useState<AgentTeamsStatus>('loading')
  const [failed, setFailed] = useState(false)

  const fetchOnce = useCallback(async () => {
    try {
      const res = await fetch('/api/agent-teams', { cache: 'no-store' })
      if (res.status === 401 || res.status === 403) {
        setStatus('forbidden')
        return
      }
      if (!res.ok) throw new Error(String(res.status))
      const body = (await res.json()) as AgentTeamsResponse
      setTeams(body.teams ?? [])
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

  return { teams, status, failed }
}
