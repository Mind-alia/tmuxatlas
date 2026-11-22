import { formatBytes, formatPercent, formatRelative } from '../../monitoring/format'
import type {
  DockerContainer,
  DockerCreatePort,
  DockerPort,
  DockerRoute,
  DockerRouteSource,
  DockerStack,
  DockerStackAction,
  DockerStats,
} from './types'

/** Colour of the status dot: green running, grey stopped, orange in transition, red failed. */
export type DotTone = 'running' | 'stopped' | 'transition' | 'error'

export function isActive(c: DockerContainer) {
  return c.state === 'running' || c.state === 'restarting' || c.state === 'paused'
}

/** A container that is running, restarting or paused can be stopped; the others started. */
export function canStop(c: DockerContainer) {
  return isActive(c)
}

/** Docker refuses to delete a container that is running, paused or restarting. */
export function canDelete(c: DockerContainer) {
  return !isActive(c) && c.state !== 'removing'
}

export function dotTone(c: DockerContainer): DotTone {
  switch (c.state) {
    case 'running':
      return 'running'
    case 'restarting':
    case 'paused':
    case 'removing':
      return 'transition'
    case 'dead':
      return 'error'
    case 'exited':
      return c.exit_code !== undefined && c.exit_code !== 0 ? 'error' : 'stopped'
    default:
      return 'stopped'
  }
}

export function stateLabel(c: DockerContainer): string {
  switch (c.state) {
    case 'running':
      return 'En cours'
    case 'restarting':
      return 'Redémarre'
    case 'paused':
      return 'En pause'
    case 'removing':
      return 'Suppression…'
    case 'created':
      return 'Créé'
    case 'dead':
      return 'Mort'
    case 'exited':
      if (c.oom_killed) return 'Arrêté (mémoire épuisée)'
      return c.exit_code !== undefined && c.exit_code !== 0 ? `Arrêté (code ${c.exit_code})` : 'Arrêté'
    default:
      return c.state
  }
}

export const healthLabel: Record<string, string> = {
  healthy: 'en bonne santé',
  unhealthy: 'en mauvaise santé',
  starting: 'vérification…',
}

/** Unix seconds of the last start, or undefined if it never ran. */
export function startedAt(c: DockerContainer): number | undefined {
  if (!c.started_at) return undefined
  const ms = Date.parse(c.started_at)
  return Number.isNaN(ms) ? undefined : Math.floor(ms / 1000)
}

export function lastStarted(c: DockerContainer, nowMs = Date.now()): string {
  const at = startedAt(c)
  return at ? formatRelative(at, nowMs) : '—'
}

/** Published ports, as Docker Desktop shows them: "8080:80", with the address unless it is any. */
export function publishedPorts(ports: DockerPort[] | null | undefined): string[] {
  return (ports ?? [])
    .filter(p => p.public)
    .map(p => {
      const any = !p.ip || p.ip === '0.0.0.0' || p.ip === '::'
      const proto = p.type && p.type !== 'tcp' ? `/${p.type}` : ''
      return `${any ? '' : `${p.ip}:`}${p.public}:${p.private}${proto}`
    })
}

export function exposedPorts(ports: DockerPort[] | null | undefined): string[] {
  return (ports ?? []).filter(p => !p.public).map(p => `${p.private}/${p.type || 'tcp'}`)
}

/** Shortens an image given by its digest only. */
export function imageLabel(image: string): string {
  return image.startsWith('sha256:') ? image.slice(0, 19) : image
}

export function cpuText(stats: DockerStats | undefined): string {
  if (!stats || stats.cpu_percent === null || stats.cpu_percent === undefined) return '—'
  return formatPercent(stats.cpu_percent)
}

export function memoryText(stats: DockerStats | undefined): string {
  return stats ? formatBytes(stats.memory_usage) : '—'
}

export type Row =
  | { kind: 'group'; project: string; containers: DockerContainer[] }
  | { kind: 'container'; container: DockerContainer }

/** Compose projects become one group each; other containers stay on their own, everything sorted by name. */
export function groupContainers(list: DockerContainer[]): Row[] {
  const groups = new Map<string, DockerContainer[]>()
  const rows: Row[] = []
  for (const c of list) {
    if (!c.compose_project) {
      rows.push({ kind: 'container', container: c })
      continue
    }
    const members = groups.get(c.compose_project)
    if (members) members.push(c)
    else groups.set(c.compose_project, [c])
  }
  for (const [project, containers] of groups) {
    containers.sort((a, b) => (a.compose_service || a.name).localeCompare(b.compose_service || b.name))
    rows.push({ kind: 'group', project, containers })
  }
  const key = (row: Row) => (row.kind === 'group' ? row.project : row.container.name)
  return rows.sort((a, b) => key(a).localeCompare(key(b)))
}

export interface GroupSummary {
  running: number
  total: number
  tone: DotTone
  label: string
  cpu: number | null
  memory: number
  /** Most recent start, unix seconds. */
  lastStart?: number
}

export function summarizeGroup(containers: DockerContainer[]): GroupSummary {
  const running = containers.filter(c => c.state === 'running').length
  const failed = containers.some(c => dotTone(c) === 'error' || c.health === 'unhealthy')
  const moving = containers.some(c => dotTone(c) === 'transition')
  let tone: DotTone = 'stopped'
  if (failed) tone = 'error'
  else if (moving || (running > 0 && running < containers.length)) tone = 'transition'
  else if (running === containers.length && running > 0) tone = 'running'
  const label =
    running === containers.length
      ? `En cours (${running}/${containers.length})`
      : running === 0
        ? `Arrêté (0/${containers.length})`
        : `Partiel (${running}/${containers.length})`
  let cpu: number | null = null
  let memory = 0
  let lastStart: number | undefined
  for (const c of containers) {
    if (c.stats?.cpu_percent !== undefined && c.stats.cpu_percent !== null) cpu = (cpu ?? 0) + c.stats.cpu_percent
    memory += c.stats?.memory_usage ?? 0
    const at = startedAt(c)
    if (at && (!lastStart || at > lastStart)) lastStart = at
  }
  return { running, total: containers.length, tone, label, cpu, memory, lastStart }
}

/** Matches the name, the ID, the image, the Compose project or service, and the ports. */
export function matches(c: DockerContainer, query: string): boolean {
  const q = query.trim().toLowerCase()
  if (!q) return true
  const haystack = [
    c.name,
    c.id,
    c.image,
    c.compose_project ?? '',
    c.compose_service ?? '',
    ...publishedPorts(c.ports),
  ]
  return haystack.some(value => value.toLowerCase().includes(q))
}

export function filterContainers(list: DockerContainer[], query: string, onlyRunning: boolean) {
  return list.filter(c => (!onlyRunning || isActive(c)) && matches(c, query))
}

/** Parses the "Run" form: one "8080:80" or "8080:80/udp" per line. */
export function parsePorts(text: string, hostIp: string): DockerCreatePort[] | string {
  const out: DockerCreatePort[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const match = /^(\d{1,5}):(\d{1,5})(?:\/(tcp|udp))?$/.exec(line)
    if (!match) return `Port illisible : « ${line} » (attendu : 8080:80)`
    const host = Number(match[1])
    const container = Number(match[2])
    if (host < 1 || host > 65535 || container < 1 || container > 65535) return `Port hors limites : « ${line} »`
    out.push({ host_ip: hostIp, host, container, proto: (match[3] as 'tcp' | 'udp' | undefined) ?? 'tcp' })
  }
  return out
}

/** One "volume:/chemin" per line. */
export function parseMounts(text: string): { volume: string; path: string }[] | string {
  const out: { volume: string; path: string }[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    const match = /^([A-Za-z0-9][A-Za-z0-9_.-]*):(\/[A-Za-z0-9._/@+-]+)$/.exec(line)
    if (!match) return `Volume illisible : « ${line} » (attendu : nom-du-volume:/chemin)`
    out.push({ volume: match[1], path: match[2] })
  }
  return out
}

/** One "NOM=valeur" per line. */
export function parseEnv(text: string): string[] | string {
  const out: string[] = []
  for (const raw of text.split('\n')) {
    const line = raw.trim()
    if (!line) continue
    if (!/^[A-Za-z_][A-Za-z0-9_]*=/.test(line)) return `Variable illisible : « ${line} » (attendu : NOM=valeur)`
    out.push(line)
  }
  return out
}

/** The dot of a stack: red when a container failed, orange when only part of it runs. */
export function stackTone(stack: DockerStack): DotTone {
  const services = stack.services ?? []
  if (services.some(c => dotTone(c) === 'error' || c.health === 'unhealthy')) return 'error'
  if (stack.state === 'running') return services.some(c => dotTone(c) === 'transition') ? 'transition' : 'running'
  return stack.state === 'partial' ? 'transition' : 'stopped'
}

export const stackStateLabel: Record<DockerStack['state'], string> = {
  running: 'En cours',
  partial: 'Partiel',
  stopped: 'Arrêté',
}

/**
 * The containers a stack action touches, in the order the server follows
 * (pkg/dockerui/stacks.go): start takes the stopped ones, dependencies first;
 * stop the active ones, dependencies last; restart the active ones only.
 */
export function stackTargets(stack: DockerStack, action: DockerStackAction): DockerContainer[] {
  const services = stack.services ?? []
  if (action === 'start') return services.filter(c => c.state === 'created' || c.state === 'exited' || c.state === 'dead')
  const active = services.filter(isActive)
  return action === 'stop' ? active.reverse() : active
}

export const routeSourceLabel: Record<DockerRouteSource, string> = {
  traefik: 'Traefik',
  caddy: 'Caddy',
  port: 'Port publié',
}

/** What the first column of the routes table shows: the domain, else the path alone, else the raw rule. */
export function routeLabel(route: DockerRoute): string {
  if (route.host) return `${route.host}${route.path ?? ''}`
  if (route.path) return `tous les domaines${route.path}`
  return route.rule ?? '—'
}

export function routePort(route: DockerRoute): string {
  if (!route.port) return '—'
  return route.proto && route.proto !== 'tcp' ? `${route.port}/${route.proto}` : String(route.port)
}
