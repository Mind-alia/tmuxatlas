import { cleanup, render, screen } from '@testing-library/react'
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

  it('garde les alertes et dit quand le collecteur a fait son dernier relevé', () => {
    render(<OpsView data={response()} />)
    expect(screen.getByText('Conteneur worker arrêté')).toBeInTheDocument()
    expect(screen.getByText(/relevé il y a 10 s/)).toBeInTheDocument()
    // La liste des conteneurs est désormais l’onglet Conteneurs, lu en direct dans Docker.
    expect(screen.queryByRole('region', { name: 'Conteneurs' })).toBeNull()
  })

  it('signale un relevé périmé', () => {
    render(<OpsView data={response({ containers: { ...response().containers, stale: true } })} />)
    expect(screen.getByText(/le collecteur ne répond plus/)).toBeInTheDocument()
  })

  it('explique comment configurer le suivi quand le hub ne lit aucun dossier', () => {
    render(<OpsView data={{ configured: false, containers: { available: false, stale: false, docker_ok: false, items: [] }, alerts: [] }} />)
    expect(screen.getByText(/ne sont pas configurées/)).toBeInTheDocument()
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

describe('BackupsPanel', () => {
  afterEach(cleanup)
  const run = {
    job: 'db-nightly', run_id: 'r1', result: 'success' as const, started_at: '', finished_at: '', exit_code: 0,
    local: { ok: true, file: 'db.sql.zst', size: 2048, sha256: 'abc', retention_days: 3, count: 3 },
    remote: { provider: 'r2', attempted: true, ok: true, verified: true, rclone_exit_code: 0, retention_days: 14, count: 14 },
  }

  it('affiche la dernière sauvegarde, la copie R2 et la rétention', () => {
    render(<OpsView data={response({ backups: {
      state: 'ok', tone: 'ok', current: run, last_success: run, last_success_at: now - 7200,
      next_expected_by: now + 3600 * 23, local_retention_days: 3, r2_retention_days: 14, r2_ok: true, r2_verified: true,
    } })} />)
    const panel = screen.getByRole('region', { name: 'Sauvegardes' })
    expect(panel).toHaveTextContent('à jour')
    expect(panel).toHaveTextContent('il y a 2 h')
    expect(panel).toHaveTextContent('copiée et vérifiée')
    expect(panel).toHaveTextContent('3 jours · 3 copies')
    expect(panel).toHaveTextContent('14 jours · 14 copies')
    expect(panel).toHaveTextContent('db.sql.zst · 2')
  })

  it('signale une copie R2 en échec', () => {
    render(<OpsView data={response({ backups: {
      state: 'r2-failed', tone: 'critical', reason: 'copie locale faite, mais échec à l’étape r2-upload',
      current: { ...run, result: 'partial', remote: { ...run.remote, ok: false, verified: false } },
      local_retention_days: 3, r2_retention_days: 14, r2_ok: false, r2_verified: false,
    } })} />)
    const panel = screen.getByRole('region', { name: 'Sauvegardes' })
    expect(panel).toHaveTextContent('copie R2 en échec')
    expect(panel).toHaveTextContent('r2-upload')
    expect(panel).toHaveTextContent('jamais')
  })
})
