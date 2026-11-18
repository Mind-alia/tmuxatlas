import { useEffect, useState } from 'react'

export interface HostTrust {
  hub?: string
  local: boolean
  trusted: string[]
}

/**
 * Whether a host runs under an account that agents and guests do not control:
 * the hub itself or a pinned sudo peer. Unknown means untrusted, so the
 * warning shows while loading and when the server cannot answer.
 */
export function isTrustedHost(trust: HostTrust | null, hostId: string): boolean {
  if (!trust) return false
  if (hostId === '' || (trust.hub && hostId === trust.hub)) return trust.local
  return trust.trusted.includes(hostId)
}

export function useHostTrust(hostId: string): boolean {
  const [trust, setTrust] = useState<HostTrust | null>(null)
  useEffect(() => {
    let cancelled = false
    const load = () =>
      fetch('/api/hosts/trust', { credentials: 'same-origin' })
        .then(response => (response.ok ? (response.json() as Promise<HostTrust>) : null))
        .then(value => {
          if (!cancelled) setTrust(value && Array.isArray(value.trusted) ? value : null)
        })
        .catch(() => {
          if (!cancelled) setTrust(null)
        })
    void load()
    const interval = setInterval(() => void load(), 60_000)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [])
  return isTrustedHost(trust, hostId)
}
