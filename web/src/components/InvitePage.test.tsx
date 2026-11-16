import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { InvitePage, invitationTokenFromHash } from './InvitePage'

const token = 'A'.repeat(43)

afterEach(() => {
  vi.restoreAllMocks()
})

describe('InvitePage', () => {
  it('reads only a well-formed token from the fragment', () => {
    expect(invitationTokenFromHash(`#${token}`)).toBe(token)
    expect(invitationTokenFromHash('#court')).toBe('')
    expect(invitationTokenFromHash('#a b<script>')).toBe('')
  })

  it('sends the token in the body, never in the URL, and sets the password', async () => {
    const calls: Array<[string, RequestInit | undefined]> = []
    vi.spyOn(window, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input)
      calls.push([url, init])
      if (url.endsWith('/inspect')) {
        return new Response(JSON.stringify({ username: 'alice', expires_at: '', has_password: false }), { status: 200 })
      }
      return new Response(null, { status: 204 })
    })
    render(<InvitePage hash={`#${token}`} onDone={() => {}} />)
    await screen.findByText('alice')
    const [first, second] = screen.getAllByDisplayValue('')
    fireEvent.change(first, { target: { value: 'un mot de passe long' } })
    fireEvent.change(second, { target: { value: 'un mot de passe long' } })
    fireEvent.click(screen.getByRole('button', { name: /Enregistrer/ }))
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Mot de passe enregistré'))
    for (const [url, init] of calls) {
      expect(url).not.toContain(token)
      expect(String(init?.body)).toContain(token)
    }
    expect(window.location.hash).toBe('')
  })
})
