import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AccountAccessDialog } from './AccountAccessDialog'

afterEach(() => {
  vi.unstubAllGlobals()
})

const alice = {
  id: 'bbbbbbbb22222222bbbbbbbb22222222',
  username: 'alice',
  role: 'guest' as const,
  disabled: false,
  created_at: '2026-01-02T00:00:00Z',
  hosts: [],
  unix_user: 'inv-alice',
}

describe('AccountAccessDialog', () => {
  it('never offers the hub host or a host of another guest, and saves the grants', async () => {
    const patches: unknown[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input)
        if (url.endsWith('/api/admin/hosts')) {
          return new Response(
            JSON.stringify({
              hosts: [
                { id: 'hub', name: 'transcriptflow@srv', online: true, local: true, grantable: false },
                { id: 'h-bob', name: 'inv-bob@srv', online: true, local: false, grantable: true, guest: 'bob-id' },
                { id: 'h-alice', name: 'inv-alice@srv', online: true, local: false, grantable: true },
              ],
            }),
            { status: 200 },
          )
        }
        if (url.endsWith('/pairing')) {
          return new Response(JSON.stringify({ code: 'BABE-DADA', expires_at: '' }), { status: 200 })
        }
        if (init?.method === 'PATCH') {
          patches.push(JSON.parse(String(init.body)))
          return new Response(JSON.stringify(alice), { status: 200 })
        }
        return new Response(null, { status: 404 })
      }),
    )
    const onChanged = vi.fn()
    render(
      <AccountAccessDialog
        user={alice}
        isSelf={false}
        usersById={new Map([['bob-id', 'bob']])}
        onClose={vi.fn()}
        onChanged={onChanged}
      />,
    )
    const hub = await screen.findByRole('checkbox', { name: /transcriptflow@srv/ })
    expect(hub).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: /inv-bob@srv/ })).toBeDisabled()
    fireEvent.click(screen.getByRole('checkbox', { name: /inv-alice@srv/ }))

    fireEvent.click(screen.getByRole('button', { name: /code d’appairage/ }))
    await waitFor(() =>
      expect(screen.getByTestId('linux-setup-commands')).toHaveTextContent('--code BABE-DADA --name inv-alice@'),
    )

    fireEvent.click(screen.getByRole('button', { name: 'Enregistrer' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(patches).toEqual([{ hosts: ['h-alice'], unix_user: 'inv-alice' }])
  })
})
