import type { MonitorAlert } from '../monitoring/types'

export type OpsTone = 'ok' | 'warning' | 'critical' | 'neutral'

/** One container, as served by GET /api/ops (see pkg/ops/containers.go). */
export interface OpsContainer {
  id: string
  short_id: string
  name: string
  image: string
  state: string
  health: string
  status: string
  tone: OpsTone
  restart_count: number
  restarts_10m: number
  started_at: string
  finished_at: string
  exit_code: number
  oom_killed: boolean
  compose_project?: string
  compose_service?: string
  uptime_seconds?: number
  bad_since?: number
  exited_desc?: string
}

export interface OpsContainers {
  available: boolean
  error?: string
  collected_at?: number
  age_seconds?: number
  stale: boolean
  docker_ok: boolean
  docker_error?: string
  items: OpsContainer[] | null
}

export interface OpsAlert extends MonitorAlert {
  key: string
}

export interface OpsResponse {
  configured: boolean
  dir?: string
  checked_at?: number
  containers: OpsContainers
  alerts: OpsAlert[] | null
}
