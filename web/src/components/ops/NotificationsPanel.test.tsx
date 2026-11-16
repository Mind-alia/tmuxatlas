import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NotificationsPanel } from './NotificationsPanel'

const iso = (secondsAgo: number) => new Date(Date.now() - secondsAgo * 1000).toISOString()

describe('NotificationsPanel', () => {
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
  })

  it('montre les canaux, les alertes suivies et les dernières notifications', () => {
    render(
      <NotificationsPanel
        notifications={{
          channels: ['webpush', 'ntfy'],
          alerts: [{ key: 'ops:container:web:down', severity: 'critical', title: 'Conteneur web arrêté',
            phase: 'firing', since: iso(600), notified: true }],
          recent: [{ key: 'ops:container:web:down', kind: 'firing', severity: 'critical',
            title: 'Conteneur web arrêté', body: '', at: iso(540) }],
        }}
      />,
    )
    expect(screen.getByText('Notifications du navigateur (PWA)')).toBeInTheDocument()
    expect(screen.getByText('ntfy')).toBeInTheDocument()
    const followed = screen.getByRole('list', { name: 'Alertes suivies' })
    expect(followed).toHaveTextContent('notifiée')
    expect(followed).toHaveTextContent('il y a 10 min')
    expect(screen.getByText('Dernières notifications (1)')).toBeInTheDocument()
  })

  it('envoie une notification de test et affiche les erreurs par canal', async () => {
    const fetch = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ channels: ['webpush'], errors: { webpush: 'aucun appareil abonné' } }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetch)
    render(<NotificationsPanel notifications={{ channels: ['webpush'], alerts: [], recent: [] }} />)
    fireEvent.click(screen.getByRole('button', { name: 'Envoyer une notification de test' }))
    expect(await screen.findByRole('status')).toHaveTextContent('webpush : aucun appareil abonné')
    expect(fetch).toHaveBeenCalledWith('/api/ops/notifications/test', expect.objectContaining({ method: 'POST' }))
  })

  it('désactive le test sans canal', () => {
    render(<NotificationsPanel notifications={{ channels: null, alerts: null, recent: null }} />)
    expect(screen.getByText('aucun canal configuré')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Envoyer une notification de test' })).toBeDisabled()
  })
})
