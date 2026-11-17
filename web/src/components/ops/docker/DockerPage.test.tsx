import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DockerPage, needsConfirmation } from './DockerPage'
import type { DockerContainer, DockerList } from './types'

const WEB = '1'.repeat(64)
const DB = '2'.repeat(64)
const WORKER = '3'.repeat(64)

function container(overrides: Partial<DockerContainer>): DockerContainer {
  return {
    id: 'f'.repeat(64), short_id: 'f'.repeat(12), name: 'x', image: 'img', state: 'running', status: 'Up',
    ports: [], created: 0, restart_count: 0, ...overrides,
  }
}

const startedAt = new Date(Date.now() - 2 * 3600 * 1000).toISOString()

function listing(overrides: Partial<DockerList> = {}): DockerList {
  return {
    available: true,
    terminal: true,
    collected_at: Date.now(),
    host: { ncpu: 4, mem_total: 8 * 1024 ** 3 },
    summary: { total: 3, running: 2, cpu_percent: 12.5, memory_usage: 512 * 1024 ** 2, stats_pending: false },
    containers: [
      container({
        id: DB, short_id: DB.slice(0, 12), name: 'shop-db-1', image: 'postgres:16', compose_project: 'shop',
        compose_service: 'db', health: 'healthy', started_at: startedAt,
        stats: { cpu_percent: 2.5, memory_usage: 256 * 1024 ** 2, memory_limit: 1024 ** 3, net_rx: 0, net_tx: 0, block_read: 0, block_write: 0, pids: 5, at: 1 },
      }),
      container({
        id: WEB, short_id: WEB.slice(0, 12), name: 'shop-web-1', image: 'nginx:1.27', compose_project: 'shop',
        compose_service: 'web', started_at: startedAt,
        ports: [{ ip: '0.0.0.0', private: 80, public: 8080, type: 'tcp' }],
        stats: { cpu_percent: 10, memory_usage: 256 * 1024 ** 2, memory_limit: 1024 ** 3, net_rx: 0, net_tx: 0, block_read: 0, block_write: 0, pids: 3, at: 1 },
      }),
      container({ id: WORKER, short_id: WORKER.slice(0, 12), name: 'worker', image: 'busybox', state: 'exited', exit_code: 1, status: 'Exited (1) 5 minutes ago' }),
    ],
    ...overrides,
  }
}

interface Call {
  url: string
  method: string
}

function mockApi(options: { list?: () => DockerList; actions?: Record<string, Response | (() => Response)> } = {}) {
  const calls: Call[] = []
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const method = (init?.method ?? 'GET').toUpperCase()
    calls.push({ url, method })
    if (url === '/api/docker/containers') {
      return new Response(JSON.stringify((options.list ?? listing)()), { status: 200 })
    }
    const custom = options.actions?.[`${method} ${url}`]
    if (custom) return typeof custom === 'function' ? custom() : custom
    if (url.includes('/terminal')) {
      return new Response(JSON.stringify({ host_id: 'hote', session: 'docker-shop-web-1', created: true }), { status: 200 })
    }
    if (url.includes('/logs')) return new Response(JSON.stringify({ lines: [], truncated: false }), { status: 200 })
    return new Response(JSON.stringify({ changed: true }), { status: 200 })
  })
  vi.stubGlobal('fetch', fetch)
  return { calls, mutations: () => calls.filter(call => call.method !== 'GET') }
}

describe('DockerPage', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/ops')
  })
  afterEach(() => {
    cleanup()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    window.history.replaceState(null, '', '/')
  })

  it('affiche les tuiles, groupe par projet Compose et remplit les colonnes', async () => {
    mockApi()
    render(<DockerPage />)
    const table = await screen.findByRole('region', { name: 'Conteneurs' })
    expect(screen.getByRole('region', { name: 'CPU des conteneurs' })).toHaveTextContent('13 %')
    expect(screen.getByRole('region', { name: 'CPU des conteneurs' })).toHaveTextContent('sur 400 % (4 CPU disponibles)')
    expect(screen.getByRole('region', { name: 'Mémoire des conteneurs' })).toHaveTextContent('512 Mo')
    expect(screen.getByRole('region', { name: 'Mémoire des conteneurs' })).toHaveTextContent('sur 8 Go')

    const rows = within(table).getAllByRole('row')
    // En-tête, projet shop, ses deux conteneurs, puis worker seul.
    expect(rows).toHaveLength(5)
    expect(rows[1]).toHaveTextContent('shop')
    expect(rows[1]).toHaveTextContent('En cours (2/2)')
    expect(rows[1]).toHaveTextContent('13 %')
    expect(rows[2]).toHaveTextContent('shop-db-1')
    expect(rows[2]).toHaveTextContent('en bonne santé')
    expect(rows[2]).toHaveTextContent(DB.slice(0, 12))
    expect(rows[3]).toHaveTextContent('shop-web-1')
    expect(rows[3]).toHaveTextContent('nginx:1.27')
    expect(rows[3]).toHaveTextContent('8080:80')
    expect(rows[3]).toHaveTextContent('10 %')
    expect(rows[3]).toHaveTextContent('256 Mo')
    expect(rows[3]).toHaveTextContent('il y a 2 h')
    expect(rows[4]).toHaveTextContent('worker')
    expect(rows[4]).toHaveTextContent('Arrêté (code 1)')
    expect(rows[4].querySelector('[data-tone]')).toHaveAttribute('data-tone', 'error')

    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Replier le projet shop' }))
    expect(within(table).getAllByRole('row')).toHaveLength(3)
    fireEvent.click(within(table).getByRole('button', { name: 'Déplier le projet shop' }))
    expect(within(table).getAllByRole('row')).toHaveLength(5)
  })

  it('filtre par recherche et ne montre que les conteneurs en cours', async () => {
    mockApi()
    render(<DockerPage />)
    const table = await screen.findByRole('region', { name: 'Conteneurs' })
    fireEvent.change(screen.getByRole('searchbox', { name: 'Rechercher un conteneur' }), { target: { value: 'busybox' } })
    expect(within(table).getAllByRole('row')).toHaveLength(2)
    expect(screen.getByText('1 sur 3 conteneurs')).toBeInTheDocument()
    fireEvent.change(screen.getByRole('searchbox', { name: 'Rechercher un conteneur' }), { target: { value: '' } })
    fireEvent.click(screen.getByRole('switch', { name: 'Seulement ceux en cours' }))
    expect(within(table).queryByText('worker')).toBeNull()
    expect(within(table).getByText('shop-web-1')).toBeInTheDocument()
  })

  it('demande confirmation avant d’arrêter un conteneur en marche, puis envoie l’arrêt', async () => {
    const api = mockApi()
    render(<DockerPage />)
    await screen.findByRole('region', { name: 'Conteneurs' })
    fireEvent.click(screen.getByRole('button', { name: 'Arrêter shop-web-1' }))
    const dialog = screen.getByRole('alertdialog')
    expect(dialog).toHaveTextContent('Arrêter « shop-web-1 » ?')
    expect(dialog).toHaveTextContent('Le service peut être coupé pendant l’opération')
    expect(dialog).toHaveTextContent('projet Compose « shop » (service web)')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Annuler' }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(api.mutations()).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Arrêter shop-web-1' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Arrêter' }))
    await waitFor(() => expect(api.mutations()).toEqual([{ url: `/api/docker/containers/${WEB}/stop`, method: 'POST' }]))
  })

  it('démarre sans confirmation, mais confirme une suppression définitive', async () => {
    const api = mockApi()
    render(<DockerPage />)
    await screen.findByRole('region', { name: 'Conteneurs' })
    fireEvent.click(screen.getByRole('button', { name: 'Démarrer worker' }))
    await waitFor(() => expect(api.mutations()).toEqual([{ url: `/api/docker/containers/${WORKER}/start`, method: 'POST' }]))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Démarrer worker' })).toBeEnabled())

    fireEvent.click(screen.getByRole('button', { name: 'Plus d’actions pour worker' }))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Supprimer' }))
    const dialog = screen.getByRole('alertdialog')
    expect(dialog).toHaveTextContent('définitive')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Supprimer définitivement' }))
    await waitFor(() => expect(api.mutations()).toContainEqual({ url: `/api/docker/containers/${WORKER}`, method: 'DELETE' }))
  })

  it('n’autorise pas la suppression d’un conteneur en marche', async () => {
    mockApi()
    render(<DockerPage />)
    await screen.findByRole('region', { name: 'Conteneurs' })
    fireEvent.click(screen.getByRole('button', { name: 'Plus d’actions pour shop-web-1' }))
    expect(await screen.findByRole('menuitem', { name: 'Supprimer' })).toBeDisabled()
  })

  it('montre l’erreur de Docker sous la ligne', async () => {
    mockApi({
      actions: {
        [`POST /api/docker/containers/${WORKER}/start`]: () =>
          new Response(JSON.stringify({ error: 'Docker refuse : port déjà utilisé' }), { status: 409 }),
      },
    })
    render(<DockerPage />)
    await screen.findByRole('region', { name: 'Conteneurs' })
    fireEvent.click(screen.getByRole('button', { name: 'Démarrer worker' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Docker refuse : port déjà utilisé')
  })

  it('ouvre un terminal et navigue vers sa session tmux', async () => {
    const api = mockApi()
    const onOpenSession = vi.fn()
    render(<DockerPage onOpenSession={onOpenSession} />)
    await screen.findByRole('region', { name: 'Conteneurs' })
    expect(screen.getByRole('button', { name: 'Ouvrir un terminal dans worker' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Ouvrir un terminal dans shop-web-1' }))
    await waitFor(() => expect(onOpenSession).toHaveBeenCalledWith('hote/docker-shop-web-1'))
    expect(api.mutations()).toEqual([{ url: `/api/docker/containers/${WEB}/terminal`, method: 'POST' }])
  })

  it('ouvre le détail d’un conteneur au clic et y revient avec le bouton', async () => {
    mockApi()
    render(<DockerPage />)
    await screen.findByRole('region', { name: 'Conteneurs' })
    fireEvent.click(screen.getByRole('button', { name: 'shop-web-1' }))
    expect(await screen.findByRole('heading', { name: /shop-web-1/ })).toBeInTheDocument()
    expect(window.location.search).toBe(`?c=${WEB.slice(0, 12)}`)
    expect(screen.getByRole('tab', { name: 'Journaux' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText(/Ports publiés : 8080:80/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('tab', { name: 'Statistiques' }))
    expect(window.location.search).toBe(`?c=${WEB.slice(0, 12)}&onglet=stats`)
    expect(screen.queryByRole('region', { name: 'CPU des conteneurs' })).toBeNull()
  })

  it('passe en cartes sur un écran de téléphone', async () => {
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({
      width: 375, height: 800, top: 0, left: 0, right: 375, bottom: 800, x: 0, y: 0, toJSON: () => ({}),
    } as DOMRect)
    mockApi()
    render(<DockerPage />)
    const list = await screen.findByRole('region', { name: 'Conteneurs' })
    expect(within(list).queryByRole('table')).toBeNull()
    const web = within(list).getByRole('article', { name: 'shop-web-1' })
    expect(web).toHaveTextContent('Ports : 8080:80')
    expect(within(web).getByRole('button', { name: 'Arrêter shop-web-1' })).toBeInTheDocument()
    const group = within(list).getByRole('button', { name: 'Projet shop, En cours (2/2)' })
    expect(group).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(group)
    expect(within(list).queryByRole('article', { name: 'shop-web-1' })).toBeNull()
    expect(within(list).getByRole('article', { name: 'worker' })).toBeInTheDocument()
  })

  it('dit que Docker est indisponible', async () => {
    mockApi({ list: () => listing({ available: false, error: 'Docker injoignable : permission denied', containers: [] }) })
    render(<DockerPage />)
    expect(await screen.findByText('Docker est indisponible.')).toBeInTheDocument()
    expect(screen.getByText('Docker injoignable : permission denied')).toBeInTheDocument()
  })

  it('n’interroge plus Docker quand la page est cachée', async () => {
    vi.useFakeTimers()
    const api = mockApi()
    render(<DockerPage />)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10)
    })
    const lists = () => api.calls.filter(call => call.url === '/api/docker/containers').length
    expect(lists()).toBe(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000)
    })
    expect(lists()).toBe(2)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
    document.dispatchEvent(new Event('visibilitychange'))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(15000)
    })
    expect(lists()).toBe(2)
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    document.dispatchEvent(new Event('visibilitychange'))
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10)
    })
    expect(lists()).toBe(3)
  })

  it('ne confirme que les actions qui coupent un service ou suppriment', () => {
    const running = container({ state: 'running' })
    const stopped = container({ state: 'exited' })
    expect(needsConfirmation(running, 'stop')).toBe(true)
    expect(needsConfirmation(running, 'restart')).toBe(true)
    expect(needsConfirmation(stopped, 'restart')).toBe(false)
    expect(needsConfirmation(stopped, 'start')).toBe(false)
    expect(needsConfirmation(stopped, 'delete')).toBe(true)
  })
})
