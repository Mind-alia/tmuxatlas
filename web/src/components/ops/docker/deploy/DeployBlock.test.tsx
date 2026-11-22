import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApplicationsPanel } from '../ApplicationsPanel'
import type { DockerStacks } from '../types'
import DeployBlock from './DeployBlock'
import type { DeployRecord, DeployState, DeployStep } from './types'

const BASE = '/api/docker/stacks/shop/deploy'
const FIRST = '20260105T100000.000000000Z-0a0b0c0d'
const SECOND = '20260106T093000.000000000Z-1a1b1c1d'

function steps(overrides: Partial<Record<DeployStep['name'], Partial<DeployStep>>> = {}): DeployStep[] {
  return (['check', 'fetch', 'merge', 'images', 'build', 'up', 'verify', 'prune'] as const).map(name => ({
    name, status: 'done', duration_ms: 1200, ...overrides[name],
  }))
}

function record(overrides: Partial<DeployRecord> = {}): DeployRecord {
  return {
    id: SECOND, kind: 'deploy', stack: 'shop', actor: 'admin', started_at: '2026-01-06T09:30:00Z',
    finished_at: '2026-01-06T09:34:00Z', result: 'success', branch: 'main', upstream: 'origin/main',
    commit_before: { sha: '1'.repeat(40), short: '1111111', subject: 'Add the cart page', date: '2026-01-05T10:00:00Z' },
    commit_after: { sha: '2'.repeat(40), short: '2222222', subject: 'Fix the checkout total', date: '2026-01-06T09:00:00Z' },
    images: [
      { service: 'db', container: 'shop-db-1', image: 'postgres:16', image_id: `sha256:${'a'.repeat(64)}`, status: 'unchanged' },
      { service: 'web', container: 'shop-web-1', image: 'shop-web', image_id: `sha256:${'b'.repeat(64)}`, rollback_tag: 'shop-web:atlas-rollback-20260106-093000', status: 'rebuilt' },
    ],
    steps: steps(),
    ...overrides,
  }
}

function state(overrides: Partial<DeployState> = {}): DeployState {
  return {
    stack: 'shop', enabled: true, working_dir: '/srv/apps/shop', config_files: ['/srv/apps/shop/compose.yaml'],
    services: ['db', 'web'],
    git: {
      repo: true, branch: 'main', upstream: 'origin/main', tracking: true, ahead: 0, behind: 2,
      commit: { sha: '2'.repeat(40), short: '2222222', subject: 'Fix the checkout total', date: '2026-01-06T09:00:00Z' },
      dirty: [], dirty_count: 0,
    },
    history: [],
    ...overrides,
  }
}

interface Call {
  url: string
  method: string
  body?: string
}

function mockApi(current: () => DeployState, routes: Record<string, () => Response> = {}) {
  const calls: Call[] = []
  vi.spyOn(window, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push({ url, method, body: typeof init?.body === 'string' ? init.body : undefined })
    const route = routes[`${method} ${url}`]
    if (route) return route()
    if (method === 'GET' && url === BASE) return new Response(JSON.stringify(current()), { status: 200 })
    return new Response(JSON.stringify({ error: 'inattendu' }), { status: 500 })
  })
  return calls
}

const posts = (calls: Call[]) => calls.filter(call => call.method === 'POST')
const json = (value: unknown, status = 200) => () => new Response(JSON.stringify(value), { status })

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  window.localStorage.clear()
})

describe('DeployBlock', () => {
  it('shows the branch, the commit and how far behind the application is', async () => {
    mockApi(() => state())
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect(await screen.findByText('main')).toBeTruthy()
    expect(screen.getByText('suit origin/main')).toBeTruthy()
    expect(screen.getByText('2222222')).toBeTruthy()
    expect(screen.getByText(/Fix the checkout total/)).toBeTruthy()
    expect(screen.getByText(/2 commits de retard/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Déployer l’application shop' }) as HTMLButtonElement).disabled).toBe(false)
    expect(screen.queryByText(/Historique/)).toBeNull()
  })

  it('says when the application is up to date, ahead, or has uncommitted changes', async () => {
    let current = state({ git: { ...state().git, behind: 0 } })
    mockApi(() => current)
    const view = render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect(await screen.findByText(/À jour avec la branche distante/)).toBeTruthy()
    view.unmount()

    current = state({ git: { ...state().git, behind: 1, ahead: 3, dirty: ['M src/app.py'], dirty_count: 4 } })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect(await screen.findByText(/1 commit de retard, 3 commits locaux non poussés/)).toBeTruthy()
    expect(screen.getByText(/4 fichiers suivis modifiés.*M src\/app\.py…/)).toBeTruthy()
  })

  it('is read-only and explains the switch while TMUXATLAS_DEPLOY is not on', async () => {
    const calls = mockApi(() =>
      state({ enabled: false, disabled_reason: 'déploiement désactivé sur ce serveur (TMUXATLAS_DEPLOY=on pour l’activer)', history: [record()], rollback_id: SECOND }),
    )
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect(await screen.findByText(/Lecture seule/)).toBeTruthy()
    expect(screen.getByText('TMUXATLAS_DEPLOY=on')).toBeTruthy()
    expect(screen.getByText('~/.config/tmuxatlas/.env')).toBeTruthy()
    expect(screen.getByText(/2 commits de retard/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Déployer l’application shop' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: /Historique \(1\)/ }))
    expect(screen.getByText('Déploiement')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Revenir/ })).toBeNull()
    expect(posts(calls)).toEqual([])
  })

  it('refuses to deploy what is blocked or is not a git repository', async () => {
    let current = state({ blocked: 'cette application contient Atlas lui-même (conteneur « shop-atlas-1 »)' })
    mockApi(() => current)
    const view = render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect(await screen.findByText(/Déploiement impossible d’ici : cette application contient Atlas/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Déployer l’application shop' }) as HTMLButtonElement).disabled).toBe(true)
    view.unmount()

    current = state({ git: { repo: false, error: 'ce dossier n’est pas dans un dépôt git', tracking: false, ahead: 0, behind: 0, dirty: [], dirty_count: 0 } })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect(await screen.findByText(/Déploiement depuis git indisponible : ce dossier n’est pas dans un dépôt git/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Déployer l’application shop' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('reports a state that cannot be read', async () => {
    mockApi(() => state(), { [`GET ${BASE}`]: json({ error: 'vue Docker désactivée sur ce serveur (TMUXATLAS_DOCKER=off)' }, 503) })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    expect((await screen.findByRole('alert')).textContent).toContain('Vue Docker désactivée')
  })

  it('deploys only after a confirmation that lists the steps and the services', async () => {
    let current = state()
    const calls = mockApi(() => current, {
      [`POST ${BASE}`]: () => {
        current = state({ current: record({ result: 'running', finished_at: undefined, steps: steps({ check: { status: 'running' } }) }) })
        return new Response(JSON.stringify(current.current), { status: 202 })
      },
    })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Déployer l’application shop' }))
    const dialog = screen.getByRole('alertdialog')
    expect(within(dialog).getByText('Déployer « shop » ?')).toBeTruthy()
    const listed = within(within(dialog).getByRole('list', { name: 'Étapes du déploiement' })).getAllByRole('listitem').map(item => item.textContent)
    expect(listed).toEqual([
      'Vérifier le dépôt git',
      'Récupérer la branche distante (git fetch)',
      'Avancer la branche (git merge --ff-only)',
      'Mémoriser les images en service',
      'Construire (docker compose build)',
      'Relancer les services (docker compose up -d)',
      'Vérifier les conteneurs',
      'Retirer les anciennes étiquettes de retour arrière',
    ])
    expect(within(dialog).getByText(/2 commits à récupérer sur origin\/main/)).toBeTruthy()
    expect(within(dialog).getByText('db, web')).toBeTruthy()
    // The safe choice has the focus, and cancelling sends nothing.
    await waitFor(() => expect(document.activeElement).toBe(within(dialog).getByRole('button', { name: 'Annuler' })))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Annuler' }))
    expect(posts(calls)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Déployer l’application shop' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Déployer' }))
    await waitFor(() => expect(posts(calls).map(call => call.url)).toEqual([BASE]))
    expect(await screen.findByRole('region', { name: 'Opération en cours' })).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Déployer l’application shop' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('follows a running operation found on load, and can cancel it', async () => {
    const running = record({
      result: 'running', finished_at: undefined,
      steps: steps({
        build: { status: 'running', duration_ms: 0, output: '$ docker compose build\n#5 building…\nAPI_TOKEN=***\n', truncated: true },
        up: { status: 'pending', duration_ms: 0 },
        verify: { status: 'pending', duration_ms: 0 },
        prune: { status: 'pending', duration_ms: 0 },
      }),
    })
    let current = state({ current: running, history: [running.steps ? { ...running, steps: running.steps.map(step => ({ ...step, output: undefined })) } : running] })
    const calls = mockApi(() => current, {
      [`POST ${BASE}/cancel`]: () => {
        current = state({ current: { ...running, cancel_requested: true } })
        return new Response(JSON.stringify(current.current), { status: 200 })
      },
    })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    const section = await screen.findByRole('region', { name: 'Opération en cours' })
    expect(within(section).getByText(/Déploiement en cours, lancé par admin/)).toBeTruthy()
    const items = within(section).getAllByRole('listitem')
    expect(items.map(item => item.getAttribute('data-status'))).toEqual(['done', 'done', 'done', 'done', 'running', 'pending', 'pending', 'pending'])
    // The output of the running step is shown without a click.
    const output = within(section).getByLabelText('Sortie de l’étape Construire (docker compose build)')
    expect(output.textContent).toContain('[… début tronqué]')
    expect(output.textContent).toContain('#5 building…')
    // The running operation is not repeated in the history.
    expect(screen.queryByText(/Historique/)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Annuler l’opération en cours sur shop' }))
    await waitFor(() => expect(posts(calls).map(call => call.url)).toEqual([`${BASE}/cancel`]))
    expect(await screen.findByRole('button', { name: 'Annuler l’opération en cours sur shop' })).toHaveProperty('disabled', true)
    expect(screen.getByText('Annulation…')).toBeTruthy()
  })

  it('shows the record of an operation when it ends, and refreshes the containers', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const running = record({ result: 'running', finished_at: undefined, steps: steps({ verify: { status: 'running', duration_ms: 0 } }) })
      let current = state({ current: running })
      const failed = record({
        result: 'failed', error: 'étape « verify » : des conteneurs ne tournent pas correctement : shop-web-1 (arrêté avec le code 1)',
        steps: steps({ verify: { status: 'failed', error: 'des conteneurs ne tournent pas correctement', output: 'En échec : shop-web-1\n' } }),
      })
      mockApi(() => current, { [`GET ${BASE}/history/${SECOND}`]: json(failed) })
      const onChanged = vi.fn()
      render(<DeployBlock stack="shop" onChanged={onChanged} />)
      await screen.findByRole('region', { name: 'Opération en cours' })
      expect(onChanged).not.toHaveBeenCalled()

      current = state({ history: [{ ...failed, steps: steps({ verify: { status: 'failed' } }) }], rollback_id: SECOND })
      await vi.advanceTimersByTimeAsync(2_100)
      const history = await screen.findByRole('list', { name: 'Historique des déploiements' })
      expect(onChanged).toHaveBeenCalledTimes(1)
      expect(screen.queryByRole('region', { name: 'Opération en cours' })).toBeNull()
      expect(within(history).getByText('échec')).toBeTruthy()
      expect(within(history).getByText(/shop-web-1 \(arrêté avec le code 1\)/)).toBeTruthy()
      // Its steps are open, with the output of the failed one.
      expect((await within(history).findByLabelText('Sortie de l’étape Vérifier les conteneurs')).textContent).toContain('En échec : shop-web-1')
      expect(within(history).getByRole('button', { name: 'Revenir à la version d’avant' })).toBeTruthy()
    } finally {
      vi.useRealTimers()
    }
  })

  it('rolls back the latest deployment only, after a confirmation that states the limits', async () => {
    const older = record({ id: FIRST, started_at: '2026-01-05T10:00:00Z', result: 'failed', error: 'étape « build » : docker compose build a échoué' })
    let current = state({ history: [record(), older], rollback_id: SECOND })
    const calls = mockApi(() => current, {
      [`GET ${BASE}/history/${FIRST}`]: json({ ...older, steps: steps({ build: { status: 'failed', output: 'failed to solve\n' }, up: { status: 'skipped' }, verify: { status: 'skipped' } }) }),
      [`POST ${BASE}/rollback`]: () => {
        current = state({ current: record({ kind: 'rollback', result: 'running', rollback_of: SECOND, steps: [{ name: 'images', status: 'running', duration_ms: 0 }] }) })
        return new Response(JSON.stringify(current.current), { status: 202 })
      },
    })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: /Historique \(2\) · dernier : réussi/ }))
    const rows = within(screen.getByRole('list', { name: 'Historique des déploiements' })).getAllByRole('listitem')
    expect(rows.map(row => row.getAttribute('data-record'))).toEqual([SECOND, FIRST])
    expect(within(rows[0]).getByText(/Fix the checkout total/)).toBeTruthy()
    expect(within(rows[0]).getByRole('button', { name: 'Revenir à la version d’avant' })).toBeTruthy()
    expect(within(rows[1]).queryByRole('button', { name: /Revenir/ })).toBeNull()

    // Opening a record fetches its steps and their output.
    fireEvent.click(within(rows[1]).getByRole('button', { expanded: false }))
    expect((await within(rows[1]).findByLabelText('Sortie de l’étape Construire (docker compose build)')).textContent).toContain('failed to solve')
    // It also says which services got a new image and which were left alone.
    const listed = within(within(rows[1]).getByRole('list', { name: 'Images des services' })).getAllByRole('listitem')
    expect(listed.map(item => item.textContent)).toEqual([
      'db : postgres:16 — inchangée',
      'web : shop-web — reconstruite, l’ancienne est gardée',
    ])
    expect(listed.map(item => item.getAttribute('data-image-status'))).toEqual(['unchanged', 'rebuilt'])

    fireEvent.click(within(rows[0]).getByRole('button', { name: 'Revenir à la version d’avant' }))
    const dialog = screen.getByRole('alertdialog')
    expect(within(dialog).getByText(/migrations de base de données déjà appliquées ne sont pas annulées/)).toBeTruthy()
    expect(within(dialog).getByText(/le code sur le disque\s+reste au commit/)).toBeTruthy()
    const images = within(within(dialog).getByRole('list', { name: 'Images remises en service' })).getAllByRole('listitem').map(item => item.textContent)
    // Only the rebuilt image is restored: the unchanged one is not listed.
    expect(images).toEqual(['web : shop-web (bbbbbbbbbbbb)'])
    expect(posts(calls)).toEqual([])
    fireEvent.click(within(dialog).getByRole('button', { name: 'Revenir en arrière' }))
    await waitFor(() => expect(posts(calls)).toEqual([{ url: `${BASE}/rollback`, method: 'POST', body: JSON.stringify({ id: SECOND }) }]))
    expect(await screen.findByText(/Retour arrière en cours/)).toBeTruthy()
  })

  it('disables the rollback, with the reason, when the deployment rebuilt nothing', async () => {
    const nothing = record({
      images: [{ service: 'db', container: 'shop-db-1', image: 'postgres:16', image_id: `sha256:${'a'.repeat(64)}`, status: 'unchanged' }],
    })
    const calls = mockApi(() =>
      state({ history: [nothing], rollback_blocked_id: SECOND, rollback_blocked: 'rien à restaurer : ce déploiement n’a reconstruit aucune image' }),
    )
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: /Historique \(1\)/ }))
    const button = screen.getByRole('button', { name: 'Revenir à la version d’avant' }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    expect(screen.getByText(/Retour arrière indisponible : rien à restaurer/)).toBeTruthy()
    expect(button.getAttribute('aria-describedby')).toBe(`${SECOND}-blocked`)
    fireEvent.click(button)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(posts(calls)).toEqual([])
  })

  it('shows why the server refused an action', async () => {
    const calls = mockApi(() => state(), { [`POST ${BASE}`]: json({ error: 'une opération est déjà en cours sur cette application' }, 409) })
    render(<DeployBlock stack="shop" onChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Déployer l’application shop' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Déployer' }))
    expect((await screen.findByRole('alert')).textContent).toContain('Une opération est déjà en cours')
    expect(posts(calls)).toHaveLength(1)
  })
})

describe('the Déploiement block of an application card', () => {
  const stacks: DockerStacks = {
    available: true, collected_at: 1, standalone: [], routes: [],
    stacks: [{ name: 'shop', working_dir: '/srv/apps/shop', config_files: ['/srv/apps/shop/compose.yaml'], state: 'running', running: 0, total: 0, services: [] }],
  }

  it('is closed until asked for, and stays open across a reload', async () => {
    const calls = mockApi(() => state(), { 'GET /api/docker/stacks': json(stacks) })
    const view = render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    const toggle = await screen.findByRole('button', { name: 'Déploiement' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(calls.some(call => call.url === BASE)).toBe(false)

    fireEvent.click(toggle)
    expect(await screen.findByText(/2 commits de retard/)).toBeTruthy()
    view.unmount()

    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    expect((await screen.findByRole('button', { name: 'Déploiement' })).getAttribute('aria-expanded')).toBe('true')
    expect(await screen.findByText(/2 commits de retard/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Déploiement' }))
    expect(screen.queryByText(/2 commits de retard/)).toBeNull()
    expect(window.localStorage.getItem('atlas-deploy-open:shop')).toBeNull()
  })

  it('is not offered for a stack without working directory', async () => {
    mockApi(() => state(), { 'GET /api/docker/stacks': json({ ...stacks, stacks: [{ ...stacks.stacks![0], working_dir: undefined }] }) })
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    await screen.findByRole('article', { name: 'Application shop' })
    expect(screen.queryByRole('button', { name: 'Déploiement' })).toBeNull()
  })
})
