import { describe, expect, it } from 'vitest'
import { mergeLines } from './LogsPanel'
import { rates } from './ContainerDetail'
import {
  canDelete,
  dotTone,
  filterContainers,
  groupContainers,
  publishedPorts,
  stateLabel,
  summarizeGroup,
} from './model'
import type { DockerContainer, DockerStats } from './types'
import { appendHistory } from './useDockerContainers'

function container(overrides: Partial<DockerContainer>): DockerContainer {
  return {
    id: 'a'.repeat(64),
    short_id: 'a'.repeat(12),
    name: 'web',
    image: 'nginx:1',
    state: 'running',
    status: 'Up 2 hours',
    ports: [],
    created: 0,
    restart_count: 0,
    ...overrides,
  }
}

describe('modèle des conteneurs', () => {
  it('donne la couleur du point comme Docker Desktop', () => {
    expect(dotTone(container({ state: 'running' }))).toBe('running')
    expect(dotTone(container({ state: 'exited', exit_code: 0 }))).toBe('stopped')
    expect(dotTone(container({ state: 'created' }))).toBe('stopped')
    expect(dotTone(container({ state: 'restarting' }))).toBe('transition')
    expect(dotTone(container({ state: 'paused' }))).toBe('transition')
    expect(dotTone(container({ state: 'exited', exit_code: 137 }))).toBe('error')
    expect(dotTone(container({ state: 'dead' }))).toBe('error')
    expect(stateLabel(container({ state: 'exited', exit_code: 137 }))).toBe('Arrêté (code 137)')
    expect(canDelete(container({ state: 'running' }))).toBe(false)
    expect(canDelete(container({ state: 'exited' }))).toBe(true)
  })

  it('groupe par projet Compose et laisse les autres conteneurs seuls, triés par nom', () => {
    const rows = groupContainers([
      container({ id: '1', name: 'zeta' }),
      container({ id: '2', name: 'shop-web-1', compose_project: 'shop', compose_service: 'web' }),
      container({ id: '3', name: 'alpha' }),
      container({ id: '4', name: 'shop-db-1', compose_project: 'shop', compose_service: 'db' }),
    ])
    expect(rows.map(row => (row.kind === 'group' ? `[${row.project}]` : row.container.name))).toEqual([
      'alpha',
      '[shop]',
      'zeta',
    ])
    const group = rows[1]
    expect(group.kind === 'group' && group.containers.map(c => c.compose_service)).toEqual(['db', 'web'])
  })

  it('résume un groupe : en cours, partiel ou en erreur, CPU et mémoire additionnés', () => {
    const stats = (cpu: number | null, memory: number): DockerStats => ({
      cpu_percent: cpu, memory_usage: memory, memory_limit: 0, net_rx: 0, net_tx: 0, block_read: 0, block_write: 0, pids: 1, at: 1,
    })
    const up = summarizeGroup([
      container({ stats: stats(1.5, 100), started_at: '2026-11-17T08:00:00Z' }),
      container({ stats: stats(2.5, 50), started_at: '2026-11-17T09:00:00Z' }),
    ])
    expect(up).toMatchObject({ running: 2, total: 2, tone: 'running', label: 'En cours (2/2)', cpu: 4, memory: 150 })
    expect(up.lastStart).toBe(Date.parse('2026-11-17T09:00:00Z') / 1000)
    expect(summarizeGroup([container({}), container({ state: 'exited', exit_code: 0 })])).toMatchObject({
      tone: 'transition', label: 'Partiel (1/2)',
    })
    expect(summarizeGroup([container({}), container({ state: 'exited', exit_code: 2 })]).tone).toBe('error')
    expect(summarizeGroup([container({ health: 'unhealthy' })]).tone).toBe('error')
    expect(summarizeGroup([container({ state: 'exited', exit_code: 0 })])).toMatchObject({ tone: 'stopped', label: 'Arrêté (0/1)', cpu: null })
  })

  it('affiche les ports publiés et cherche partout', () => {
    const web = container({
      name: 'web',
      compose_project: 'shop',
      ports: [
        { ip: '0.0.0.0', private: 80, public: 8080, type: 'tcp' },
        { ip: '127.0.0.1', private: 53, public: 5353, type: 'udp' },
        { private: 443, type: 'tcp' },
      ],
    })
    expect(publishedPorts(web.ports)).toEqual(['8080:80', '127.0.0.1:5353:53/udp'])
    const db = container({ id: 'b'.repeat(64), name: 'db', image: 'postgres:16', state: 'exited' })
    expect(filterContainers([web, db], 'POSTGRES', false)).toEqual([db])
    expect(filterContainers([web, db], 'shop', false)).toEqual([web])
    expect(filterContainers([web, db], '8080', false)).toEqual([web])
    expect(filterContainers([web, db], 'bbbb', false)).toEqual([db])
    expect(filterContainers([web, db], '', true)).toEqual([web])
  })

  it('garde un historique borné des échantillons', () => {
    const list = (containers: DockerContainer[]) => ({
      available: true, terminal: false, collected_at: 0, containers,
      summary: { total: containers.length, running: containers.length, cpu_percent: 0, memory_usage: 0, stats_pending: false },
    })
    const sample = (at: number): DockerStats => ({
      cpu_percent: 1, memory_usage: 1, memory_limit: 1, net_rx: at * 10, net_tx: 0, block_read: 0, block_write: 0, pids: 1, at,
    })
    let history = appendHistory(new Map(), list([container({ id: 'x', stats: sample(1000) })]))
    history = appendHistory(history, list([container({ id: 'x', stats: sample(1000) })]))
    expect(history.get('x')).toHaveLength(1)
    for (let i = 2; i < 200; i++) {
      history = appendHistory(history, list([container({ id: 'x', stats: sample(i * 1000) })]))
    }
    expect(history.get('x')).toHaveLength(100)
    expect(rates(history.get('x')!.slice(-3), s => s.net_rx)).toEqual([10000, 10000])
    history = appendHistory(history, list([]))
    expect(history.size).toBe(0)
  })

  it('n’ajoute pas deux fois les lignes de journal à la frontière de since', () => {
    const a = { t: '2026-11-17T08:00:00Z', s: 'out' as const, m: 'a' }
    const b = { t: '2026-11-17T08:00:01Z', s: 'out' as const, m: 'b' }
    expect(mergeLines([a], [a, b])).toEqual([a, b])
    expect(mergeLines([a, b], [])).toEqual([a, b])
  })
})
