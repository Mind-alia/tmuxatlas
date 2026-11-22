import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApplicationsPanel } from './ApplicationsPanel'
import { routeLabel, routePort, stackTargets, stackTone } from './model'
import type { DockerContainer, DockerRoute, DockerStack, DockerStacks } from './types'

const APP = 'a'.repeat(64)
const DB = 'b'.repeat(64)
const JOB = 'c'.repeat(64)
const TOOL = 'd'.repeat(64)

function container(overrides: Partial<DockerContainer>): DockerContainer {
  return {
    id: 'f'.repeat(64), short_id: 'f'.repeat(12), name: 'x', image: 'img', state: 'running', status: 'Up',
    ports: [], created: 0, restart_count: 0, ...overrides,
  }
}

/** Services come from the server in start order: the database before the app. */
function blog(overrides: Partial<DockerStack> = {}): DockerStack {
  return {
    name: 'blog',
    working_dir: '/srv/apps/blog',
    config_files: ['/srv/apps/blog/compose.yaml'],
    state: 'partial',
    running: 2,
    total: 3,
    services: [
      container({ id: DB, name: 'blog-db-1', image: 'postgres:16', compose_project: 'blog', compose_service: 'db', health: 'healthy' }),
      container({
        id: APP, name: 'blog-app-1', image: 'blog:2', compose_project: 'blog', compose_service: 'app',
        ports: [{ ip: '127.0.0.1', private: 3000, public: 8080, type: 'tcp' }],
      }),
      container({ id: JOB, name: 'blog-migrate-1', image: 'blog:2', compose_project: 'blog', compose_service: 'migrate', state: 'exited', exit_code: 0 }),
    ],
    ...overrides,
  }
}

const routes: DockerRoute[] = [
  { source: 'traefik', host: 'blog.example.com', path: '/api', rule: 'Host(`blog.example.com`) && PathPrefix(`/api`)', port: 3000, container: 'blog-app-1', container_id: APP, compose_project: 'blog', state: 'running' },
  { source: 'caddy', host: 'docs.example.com', rule: 'docs.example.com', container: 'tool', container_id: TOOL, state: 'exited' },
  { source: 'port', host: '127.0.0.1:8080', port: 3000, proto: 'tcp', container: 'blog-app-1', container_id: APP, state: 'running' },
]

function stacks(overrides: Partial<DockerStacks> = {}): DockerStacks {
  return {
    available: true,
    collected_at: 1,
    stacks: [blog()],
    standalone: [container({ id: TOOL, name: 'tool', image: 'alpine', state: 'exited', exit_code: 0 })],
    routes,
    ...overrides,
  }
}

interface Call {
  url: string
  method: string
}

function mockApi(list: () => DockerStacks, actions: Record<string, () => Response> = {}) {
  const calls: Call[] = []
  vi.spyOn(window, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push({ url, method })
    if (url === '/api/docker/stacks') return new Response(JSON.stringify(list()), { status: 200 })
    const action = actions[`${method} ${url}`]
    return action ? action() : new Response(JSON.stringify({ error: 'inattendu' }), { status: 500 })
  })
  return calls
}

const posts = (calls: Call[]) => calls.filter(call => call.method === 'POST').map(call => call.url)

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('ApplicationsPanel', () => {
  it('montre une carte par application, ses services et le groupe « Sans application »', async () => {
    mockApi(stacks)
    const opened: string[] = []
    render(<ApplicationsPanel onOpenContainer={(id, tab) => opened.push(`${id.slice(0, 4)}:${tab}`)} onContainersChanged={() => {}} />)
    const card = await screen.findByRole('article', { name: 'Application blog' })
    expect(within(card).getByText('Partiel · 2/3 services démarrés')).toBeTruthy()
    expect(within(card).getByText('/srv/apps/blog')).toBeTruthy()
    expect(card.querySelector('[data-tone]')?.getAttribute('data-tone')).toBe('transition')
    const rows = [...card.querySelectorAll('li[data-container]')].map(row => row.getAttribute('data-container'))
    expect(rows).toEqual(['blog-db-1', 'blog-app-1', 'blog-migrate-1'])
    const app = card.querySelector('li[data-container="blog-app-1"]') as HTMLElement
    expect(within(app).getByText('app')).toBeTruthy()
    expect(within(app).getByText('En cours')).toBeTruthy()
    expect(within(app).getByText('blog:2')).toBeTruthy()
    expect(within(app).getByText('Ports : 127.0.0.1:8080:3000')).toBeTruthy()
    expect(within(card).getByText('en bonne santé')).toBeTruthy()

    fireEvent.click(within(card).getByRole('button', { name: 'Journaux et détails de blog-db-1' }))
    expect(opened).toEqual(['bbbb:logs'])

    fireEvent.click(within(card).getByRole('button', { name: '3 conteneurs' }))
    expect(card.querySelector('li[data-container]')).toBeNull()

    const alone = screen.getByRole('region', { name: 'Sans application' })
    expect(within(alone).getByText('tool')).toBeTruthy()
    expect(within(alone).queryByRole('button', { name: /Arrêter/ })).toBeNull()
  })

  it('confirme chaque action en listant les conteneurs touchés, dans l’ordre', async () => {
    const changed = vi.fn()
    const calls = mockApi(stacks, {
      'POST /api/docker/stacks/blog/stop': () =>
        new Response(JSON.stringify({
          stack: 'blog', action: 'stop', ok: true,
          results: [
            { id: JOB, name: 'blog-migrate-1', status: 'unchanged' },
            { id: APP, name: 'blog-app-1', status: 'done' },
            { id: DB, name: 'blog-db-1', status: 'done' },
          ],
        }), { status: 200 }),
    })
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={changed} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Arrêter l’application blog' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText('Arrêter l’application « blog » ?')).toBeTruthy()
    // Stop goes dependencies last, and leaves the finished job alone.
    const listed = within(within(dialog).getByRole('list', { name: 'Conteneurs touchés' })).getAllByRole('listitem')
    expect(listed.map(item => item.textContent)).toEqual(['blog-app-1 (app)', 'blog-db-1 (db)'])
    expect(posts(calls)).toEqual([])

    fireEvent.click(within(dialog).getByRole('button', { name: 'Annuler' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(posts(calls)).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Arrêter l’application blog' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Arrêter' }))
    await waitFor(() => expect(posts(calls)).toEqual(['/api/docker/stacks/blog/stop']))
    expect(await screen.findByText('Application arrêtée : 2 conteneurs concernés.')).toBeTruthy()
    expect(changed).toHaveBeenCalled()
  })

  it('liste pour le démarrage les seuls conteneurs arrêtés et désactive les actions sans objet', async () => {
    mockApi(() => stacks({ stacks: [blog({ state: 'running', running: 3, services: blog().services!.map(c => ({ ...c, state: 'running' })) })] }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    expect(((await screen.findByRole('button', { name: 'Démarrer l’application blog' })) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Redémarrer l’application blog' }) as HTMLButtonElement).disabled).toBe(false)
    cleanup()

    mockApi(stacks)
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Démarrer l’application blog' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getAllByRole('listitem').map(item => item.textContent)).toEqual(['blog-migrate-1 (migrate)'])
    expect(within(dialog).getByText(/Rien n’est créé, reconstruit ni téléchargé/)).toBeTruthy()
  })

  it('détaille un échec partiel conteneur par conteneur', async () => {
    mockApi(stacks, {
      'POST /api/docker/stacks/blog/restart': () =>
        new Response(JSON.stringify({
          stack: 'blog', action: 'restart', ok: false,
          results: [
            { id: DB, name: 'blog-db-1', status: 'done' },
            { id: APP, name: 'blog-app-1', status: 'failed', error: 'erreur Docker : driver failed' },
            { id: JOB, name: 'blog-migrate-1', status: 'skipped' },
          ],
        }), { status: 200 }),
    })
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Redémarrer l’application blog' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Redémarrer' }))
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText(/Action interrompue/)).toBeTruthy()
    const lines = [...alert.querySelectorAll('li')].map(line => `${line.getAttribute('data-result')}|${line.textContent}`)
    expect(lines).toEqual([
      'done|blog-db-1 : fait',
      'failed|blog-app-1 : échec — erreur Docker : driver failed',
      'skipped|blog-migrate-1 : non tenté',
    ])
    fireEvent.click(within(alert).getByRole('button', { name: 'Fermer' }))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('affiche le refus du serveur et bloque l’arrêt d’une application protégée', async () => {
    mockApi(stacks, {
      'POST /api/docker/stacks/blog/stop': () => new Response(JSON.stringify({ error: 'une action est déjà en cours sur cette application' }), { status: 409 }),
    })
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Arrêter l’application blog' }))
    fireEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Arrêter' }))
    expect(await screen.findByText('Une action est déjà en cours sur cette application')).toBeTruthy()
    cleanup()

    mockApi(() => stacks({ stacks: [blog({ protected: 'cette application contient Atlas lui-même (conteneur « blog-app-1 »)' })] }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    expect(await screen.findByText(/Arrêt et redémarrage désactivés/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Arrêter l’application blog' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Redémarrer l’application blog' }) as HTMLButtonElement).disabled).toBe(true)
    expect((screen.getByRole('button', { name: 'Démarrer l’application blog' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('présente les routes avec leur source et le dit quand il n’y en a aucune', async () => {
    const opened: string[] = []
    mockApi(stacks)
    render(<ApplicationsPanel onOpenContainer={id => opened.push(id)} onContainersChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Routes (3)' }))
    const table = screen.getByRole('region', { name: 'Routes' })
    const rows = within(table).getAllByRole('row').slice(1).map(row => [...row.querySelectorAll('td')].map(cell => cell.textContent))
    expect(rows).toEqual([
      ['blog.example.com/api', 'blog-app-1blog', '3000', 'Traefik'],
      ['docs.example.com', 'tool(à l’arrêt)', '—', 'Caddy'],
      ['127.0.0.1:8080', 'blog-app-1', '3000', 'Port publié'],
    ])
    expect(screen.getByText(/Un proxy configuré par fichier/)).toBeTruthy()
    fireEvent.click(within(table).getByRole('button', { name: 'tool' }))
    expect(opened).toEqual([TOOL])
    cleanup()

    mockApi(() => stacks({ routes: [] }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Routes (0)' }))
    expect(screen.getByText(/Aucune route détectable/)).toBeTruthy()
    cleanup()

    mockApi(() => stacks({ routes: [routes[2]] }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Routes (1)' }))
    expect(screen.getByText(/seuls les ports publiés sur le serveur sont listés/)).toBeTruthy()
  })

  it('réserve la page aux administrateurs et signale Docker indisponible ou sans application', async () => {
    vi.spyOn(window, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: 'accès interdit' }), { status: 403 }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    expect(await screen.findByText('Cette page est réservée aux administrateurs.')).toBeTruthy()
    cleanup()
    vi.restoreAllMocks()

    mockApi(() => stacks({ available: false, error: 'Docker injoignable', stacks: [], standalone: [], routes: [] }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    expect(await screen.findByText(/Impossible de lire les applications : Docker injoignable/)).toBeTruthy()
    cleanup()
    vi.restoreAllMocks()

    mockApi(() => stacks({ stacks: [], standalone: [] }))
    render(<ApplicationsPanel onOpenContainer={() => {}} onContainersChanged={() => {}} />)
    expect(await screen.findByText(/Aucune application Compose sur ce serveur/)).toBeTruthy()
    expect(screen.queryByRole('region', { name: 'Sans application' })).toBeNull()
  })
})

describe('modèle des applications', () => {
  it('choisit les conteneurs touchés comme le serveur', () => {
    const names = (action: 'start' | 'stop' | 'restart') => stackTargets(blog(), action).map(c => c.name)
    expect(names('start')).toEqual(['blog-migrate-1'])
    expect(names('stop')).toEqual(['blog-app-1', 'blog-db-1'])
    expect(names('restart')).toEqual(['blog-db-1', 'blog-app-1'])
    expect(stackTargets(blog({ services: null }), 'stop')).toEqual([])
  })

  it('colore la pastille selon l’état global', () => {
    const all = (state: string, extra: Partial<DockerContainer> = {}) => blog().services!.map(c => ({ ...c, state, ...extra }))
    expect(stackTone(blog({ state: 'running', services: all('running') }))).toBe('running')
    expect(stackTone(blog())).toBe('transition')
    expect(stackTone(blog({ state: 'stopped', services: all('exited', { exit_code: 0 }) }))).toBe('stopped')
    expect(stackTone(blog({ state: 'stopped', services: all('exited', { exit_code: 137 }) }))).toBe('error')
    expect(stackTone(blog({ state: 'running', services: all('running', { health: 'unhealthy' }) }))).toBe('error')
  })

  it('décrit une route sans domaine ou sans port', () => {
    const base = { container: 'c', container_id: APP, state: 'running' }
    expect(routeLabel({ ...base, source: 'traefik', path: '/static', rule: 'PathPrefix(`/static`)' })).toBe('tous les domaines/static')
    expect(routeLabel({ ...base, source: 'traefik', rule: 'Host(`broken' })).toBe('Host(`broken')
    expect(routePort({ ...base, source: 'traefik' })).toBe('—')
    expect(routePort({ ...base, source: 'port', port: 53, proto: 'udp' })).toBe('53/udp')
  })
})
