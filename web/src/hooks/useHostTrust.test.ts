import { describe, expect, it } from 'vitest'
import { isTrustedHost } from './useHostTrust'

describe('isTrustedHost', () => {
  const trust = { hub: 'hub-id', local: true, trusted: ['hub-id', 'sudo-admin'] }

  it('trusts only the hub and the pinned sudo peers', () => {
    expect(isTrustedHost(trust, 'hub-id')).toBe(true)
    expect(isTrustedHost(trust, '')).toBe(true)
    expect(isTrustedHost(trust, 'sudo-admin')).toBe(true)
    expect(isTrustedHost(trust, 'host-agents')).toBe(false)
  })

  it('fails closed when the answer is unknown', () => {
    expect(isTrustedHost(null, '')).toBe(false)
    expect(isTrustedHost(null, 'hub-id')).toBe(false)
    expect(isTrustedHost({ local: false, trusted: [] }, '')).toBe(false)
  })
})
