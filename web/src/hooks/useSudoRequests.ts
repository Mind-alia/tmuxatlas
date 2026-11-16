import { useCallback, useEffect, useRef, useState } from 'react'
import { SudoUnavailableError, requestsForSession, sudoApi, type SudoRequest } from '../lib/sudoApi'

const POLL_MS = 3000

/**
 * Sudo requests of one session, polled while the page is visible. Stops for
 * good when the server says the sudo terminal is not offered to this user.
 */
export function useSudoRequests(hostId: string, sessionName: string, enabled: boolean) {
  const [requests, setRequests] = useState<SudoRequest[]>([])
  const [available, setAvailable] = useState(enabled)
  const [error, setError] = useState('')
  const availableRef = useRef(enabled)

  const refresh = useCallback(async () => {
    if (!availableRef.current) return
    try {
      const all = await sudoApi.list()
      setRequests(requestsForSession(all, hostId, sessionName))
      setError('')
    } catch (err) {
      if (err instanceof SudoUnavailableError) {
        availableRef.current = false
        setAvailable(false)
        setRequests([])
        return
      }
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [hostId, sessionName])

  useEffect(() => {
    availableRef.current = enabled
    setAvailable(enabled)
    setRequests([])
    if (!enabled) return
    void refresh()
    const timer = window.setInterval(() => {
      if (!document.hidden) void refresh()
    }, POLL_MS)
    return () => window.clearInterval(timer)
  }, [enabled, refresh])

  return { requests, available, error, refresh }
}
