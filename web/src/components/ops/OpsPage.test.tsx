import { cleanup, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TopBar } from '../TopBar'
import { OpsView } from './OpsPage'
import type { OpsContainer, OpsResponse } from './types'

const now = Math.floor(Date.now() / 1000)

function container(overrides: Partial<OpsContainer>): OpsContainer {
  return {
    id: 'id', short_id: 'id', name: 'web', image: 'nginx:1', state: 'running', health: 'healthy',
    status: 'healthy', tone: 'ok', restart_count: 0, restarts_10m: 0, started_at: '', finished_at: '',
    exit_code: 0, oom_killed: false, uptime_seconds: 7200, ...overrides,
  }
}

function response(overrides: Partial<OpsResponse> = {}): OpsResponse {
  return {
    configured: true,
    containers: {
      available: true, collected_at: now - 10, stale: false, docker_ok: true,
      items: [
        container({ id: 'a', name: 'worker', state: 'exited', status: 'exited', tone: 'critical',
          uptime_seconds: undefined, bad_since: now - 300, exited_desc: 'sorti en erreur (code 1)' }),
        container({ id: 'b', name: 'web', compose_project: 'tf', compose_service: 'web', restarts_10m: 3 }),
      ],
    },
    alerts: [{ key: 'ops:container:worker:down', severity: 'critical', title: 'Conteneur worker arrêté' }],
    ...overrides,
  }
}

describe('OpsView', () => {
  afterEach(cleanup)

  it('liste les conteneurs avec leur état, leurs redémarrages et leur uptime', () => {
    render(<OpsView data={response()} />)
    const table = screen.getByRole('region', { name: 'Conteneurs' })
    const rows = within(table).getAllByRole('row')
    expect(rows).toHaveLength(3)
    expect(rows[1]).toHaveTextContent('arrêté')
    expect(rows[1]).toHaveTextContent('sorti en erreur (code 1)')
    expect(rows[1]).toHaveTextContent('il y a 5 min')
    expect(rows[2]).toHaveTextContent('en bonne santé')
    expect(rows[2]).toHaveTextContent('tf / web')
    expect(rows[2]).toHaveTextContent('3 / 10 min')
    expect(rows[2]).toHaveTextContent('2 h 0 min')
    expect(screen.getByText('Conteneur worker arrêté')).toBeInTheDocument()
  })

  it('signale un relevé périmé', () => {
    render(<OpsView data={response({ containers: { ...response().containers, stale: true } })} />)
    expect(screen.getByText(/le collecteur ne répond plus/)).toBeInTheDocument()
  })

  it('explique comment configurer le suivi quand le hub ne lit aucun dossier', () => {
    render(<OpsView data={{ configured: false, containers: { available: false, stale: false, docker_ok: false, items: [] }, alerts: [] }} />)
    expect(screen.getByText(/n’est pas configuré/)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Conteneurs' })).toBeNull()
  })
})

describe('Onglet Conteneurs', () => {
  afterEach(cleanup)
  const props = {
    currentView: 'overview', sidebarHidden: false, onToggleCollapse: vi.fn(), onOverview: vi.fn(),
    onMonitoring: vi.fn(), onWall: vi.fn(), onOpenPalette: vi.fn(), onSettings: vi.fn(), events: [],
    onJumpToSession: vi.fn(), onDismiss: vi.fn(), onDismissAll: vi.fn(),
  }

  it('n’apparaît que si la vue est autorisée', () => {
    const { rerender } = render(<TopBar {...props} role="user" />)
    expect(screen.queryByRole('button', { name: 'Conteneurs' })).toBeNull()
    const onOps = vi.fn()
    rerender(<TopBar {...props} role="admin" onOps={onOps} />)
    screen.getByRole('button', { name: 'Conteneurs' }).click()
    expect(onOps).toHaveBeenCalled()
  })
})
