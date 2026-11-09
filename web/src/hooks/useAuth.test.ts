import { act, cleanup, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { passkeyError, useAuth } from './useAuth'

// useAuth routes window.fetch through apiFetch; this stands in for the server.
const serverFetch = vi.hoisted(() => vi.fn())
vi.mock('../lib/api', () => ({ apiFetch: serverFetch, setCSRFToken: vi.fn() }))
vi.mock('../lib/webauthn', () => ({ authenticatePasskey: vi.fn(), registerPasskey: vi.fn() }))

describe('passkeyError', () => {
  it('explains common browser and setup errors in French', () => {
    expect(passkeyError(new Error('invalid setup token or session'), 'Erreur')).toBe(
      'Le jeton de configuration ou la session est invalide.',
    )
    expect(passkeyError(new DOMException('cancelled', 'NotAllowedError'), 'Erreur')).toBe(
      'La demande de passkey a été annulée ou a expiré.',
    )
    expect(passkeyError(new Error('This browser does not support passkeys'), 'Erreur')).toBe(
      'Ce navigateur ne prend pas en charge les passkeys.',
    )
    expect(passkeyError(new Error('untranslated server error'), 'Erreur')).toBe('Erreur')
  })
})

describe('useAuth', () => {
  const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
  const signedIn = { mode: 'accounts', authenticated: true, username: 'aris', role: 'user', csrf_token: 'c' }

  beforeEach(() => {
    vi.useFakeTimers()
    serverFetch.mockReset()
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
  })

  it('retries instead of showing sign-in when the server is unreachable at load', async () => {
    serverFetch.mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValue(json(signedIn))
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.loading).toBe(true)
    expect(result.current.authenticated).toBe(false)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })
    expect(result.current.loading).toBe(false)
    expect(result.current.authenticated).toBe(true)
  })

  it('keeps the session through a network error or a proxy error', async () => {
    serverFetch.mockResolvedValue(json(signedIn))
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.authenticated).toBe(true)

    serverFetch.mockRejectedValueOnce(new TypeError('Failed to fetch'))
    let still: boolean | undefined
    await act(async () => {
      still = await result.current.refreshAuthStatus()
    })
    expect(still).toBe(true)
    expect(result.current.authenticated).toBe(true)

    serverFetch.mockResolvedValueOnce(new Response('Bad Gateway', { status: 502 }))
    await act(async () => {
      await result.current.refreshAuthStatus()
    })
    expect(result.current.authenticated).toBe(true)
    expect(result.current.username).toBe('aris')
  })

  it('shows passkey sign-in when the server answers 401 to the check', async () => {
    serverFetch.mockImplementation(async (url: string) =>
      url.includes('/api/auth/status')
        ? json({ auth_required: true, needs_setup: false })
        : json({ authenticated: false }, 401),
    )
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.loading).toBe(false)
    expect(result.current.authMode).toBe('passkey')
    expect(result.current.authenticated).toBe(false)
  })

  it('retries the passkey check while the proxy cannot reach the server', async () => {
    let checks = 0
    serverFetch.mockImplementation(async (url: string) => {
      if (url.includes('/api/auth/status')) return json({ auth_required: true, needs_setup: false })
      checks += 1
      return checks === 1 ? new Response('Bad Gateway', { status: 502 }) : json({ authenticated: true, csrf_token: 'c' })
    })
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(result.current.loading).toBe(true)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000)
    })
    expect(result.current.loading).toBe(false)
    expect(result.current.authenticated).toBe(true)
  })

  it('signs out when the server says the session is over', async () => {
    serverFetch.mockResolvedValue(json(signedIn))
    const { result } = renderHook(() => useAuth())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    serverFetch.mockResolvedValueOnce(json({ mode: 'accounts', authenticated: false }))
    await act(async () => {
      await result.current.refreshAuthStatus()
    })
    expect(result.current.authenticated).toBe(false)
  })
})
