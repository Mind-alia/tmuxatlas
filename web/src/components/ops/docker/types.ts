/** Payloads of /api/docker/* (see pkg/dockerui/types.go). */

export interface DockerPort {
  ip?: string
  private: number
  public?: number
  type: string
}

export interface DockerStats {
  /** 100 is one full CPU; null until two samples can be compared. */
  cpu_percent: number | null
  memory_usage: number
  memory_limit: number
  net_rx: number
  net_tx: number
  block_read: number
  block_write: number
  pids: number
  /** Sample time, unix milliseconds. */
  at: number
}

export type DockerHealth = 'healthy' | 'unhealthy' | 'starting'

export interface DockerContainer {
  id: string
  short_id: string
  name: string
  image: string
  /** created | running | paused | restarting | removing | exited | dead */
  state: string
  /** Docker's own text, such as "Up 2 hours (healthy)". */
  status: string
  health?: DockerHealth
  exit_code?: number
  oom_killed?: boolean
  compose_project?: string
  compose_service?: string
  ports: DockerPort[] | null
  /** Unix seconds. */
  created: number
  started_at?: string
  finished_at?: string
  restart_count: number
  stats?: DockerStats
}

export interface DockerHost {
  ncpu: number
  mem_total: number
  version?: string
}

export interface DockerSummary {
  total: number
  running: number
  cpu_percent: number
  memory_usage: number
  stats_pending: boolean
}

export interface DockerList {
  available: boolean
  error?: string
  host?: DockerHost
  summary: DockerSummary
  containers: DockerContainer[] | null
  /** Whether a shell can be opened in a container on this server. */
  terminal: boolean
  collected_at: number
}

export interface DockerLogLine {
  /** RFC 3339 timestamp from Docker. */
  t?: string
  s: 'out' | 'err'
  m: string
}

export interface DockerLogs {
  lines: DockerLogLine[] | null
  next_since?: string
  truncated: boolean
}

export type DockerAction = 'start' | 'stop' | 'restart' | 'delete'

export interface DockerTerminal {
  host_id: string
  session: string
  created: boolean
}

export interface DockerImage {
  id: string
  short_id: string
  tags: string[] | null
  size: number
  /** Unix seconds. */
  created: number
  containers: string[] | null
}

export interface DockerImages {
  images: DockerImage[] | null
  total_size: number
  collected_at: number
}

export interface DockerVolume {
  name: string
  driver: string
  created_at?: string
  /** null while Docker has not measured the volume. */
  size: number | null
  anonymous?: boolean
  compose_project?: string
  containers: string[] | null
}

export interface DockerVolumes {
  volumes: DockerVolume[] | null
  collected_at: number
}

export interface DockerResourceResult {
  name: string
  action: string
  detail?: string
}

export interface DockerCreatePort {
  host_ip?: string
  host: number
  container: number
  proto?: 'tcp' | 'udp'
}

export interface DockerCreateRequest {
  image: string
  name?: string
  ports?: DockerCreatePort[]
  env?: string[]
  volumes?: { volume: string; path: string }[]
  restart?: 'no' | 'always' | 'unless-stopped' | 'on-failure'
  start?: boolean
}

/** One Compose project (see pkg/dockerui/stacks.go). */
export interface DockerStack {
  name: string
  working_dir?: string
  config_files: string[] | null
  state: 'running' | 'partial' | 'stopped'
  running: number
  total: number
  /** The containers in start order: dependencies first. No stats here. */
  services: DockerContainer[] | null
  /** Why the stack cannot be stopped nor restarted from Atlas. */
  protected?: string
}

export type DockerRouteSource = 'traefik' | 'caddy' | 'port'

/** One way into a container: a proxy rule read from its labels, or a published port. */
export interface DockerRoute {
  source: DockerRouteSource
  /** A domain, or "address:port" on the host for a published port. */
  host?: string
  path?: string
  /** The label value the route was read from. */
  rule?: string
  /** Container port; absent when the labels do not say. */
  port?: number
  proto?: string
  container: string
  container_id: string
  compose_project?: string
  compose_service?: string
  state: string
}

export interface DockerStacks {
  available: boolean
  error?: string
  stacks: DockerStack[] | null
  standalone: DockerContainer[] | null
  routes: DockerRoute[] | null
  collected_at: number
}

export type DockerStackAction = 'start' | 'stop' | 'restart'

export interface DockerStackResult {
  id: string
  name: string
  service?: string
  status: 'done' | 'unchanged' | 'failed' | 'skipped'
  error?: string
}

export interface DockerStackActionResponse {
  stack: string
  action: DockerStackAction
  /** False when a container failed: results then tells which ones. */
  ok: boolean
  results: DockerStackResult[] | null
}
