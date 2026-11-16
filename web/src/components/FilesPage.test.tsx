import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FilesPage } from './FilesPage'

afterEach(() => {
  vi.unstubAllGlobals()
})

function mockFiles(modeByHost: Record<string, 'ro' | 'rw'>) {
  const urls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      urls.push(url)
      const host = new URL(url, 'http://atlas').searchParams.get('host_id') ?? ''
      if (url.startsWith('/api/files/hosts')) {
        return new Response(
          JSON.stringify({
            hosts: [
              { id: 'hub', name: 'transcriptflow@srv', online: true, local: true },
              { id: 'agt', name: 'agents@srv', online: true, local: false },
            ],
          }),
        )
      }
      if (url.startsWith('/api/files/info')) {
        return new Response(JSON.stringify({ mode: modeByHost[host], root: `/home/${host}` }))
      }
      if (url.startsWith('/api/files/list')) {
        return new Response(
          JSON.stringify({ path: '/', truncated: false, entries: [{ name: `${host}.txt`, type: 'file', size: 3, mtime: '', mode: '' }] }),
        )
      }
      return new Response(null, { status: 404 })
    }),
  )
  return urls
}

describe('FilesPage', () => {
  it('lists the accounts and browses the chosen one, with its own ceiling', async () => {
    const urls = mockFiles({ hub: 'ro', agt: 'rw' })
    render(<FilesPage />)
    expect(await screen.findByText('hub.txt')).toBeInTheDocument()
    expect(screen.getByTestId('files-mode')).toHaveTextContent('lecture seule')
    expect(screen.queryByRole('button', { name: 'Envoyer' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Supprimer/ })).not.toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Compte dont parcourir les fichiers'), { target: { value: 'agt' } })
    expect(await screen.findByText('agt.txt')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('files-mode')).toHaveTextContent('lecture et écriture'))
    expect(screen.getByRole('button', { name: 'Envoyer' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Télécharger agt.txt' })).toHaveAttribute(
      'href',
      '/api/files/raw?host_id=agt&path=%2Fagt.txt',
    )
    for (const url of urls.filter(value => !value.startsWith('/api/files/hosts'))) {
      expect(url).toMatch(/host_id=(hub|agt)/)
    }
  })
})
