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

export interface OpsBackupRun {
  job: string
  run_id: string
  result: 'running' | 'success' | 'failure' | 'partial'
  started_at: string
  finished_at: string
  exit_code: number
  step?: string
  failed_step?: string
  message?: string
  local: { ok: boolean; file: string; size: number; sha256: string; retention_days: number; count: number }
  remote: {
    provider: string
    attempted: boolean
    ok: boolean
    verified: boolean
    rclone_exit_code: number
    retention_days: number
    count: number
  }
}

export type OpsBackupState = 'ok' | 'running' | 'failed' | 'r2-failed' | 'late' | 'stuck' | 'unknown'

export interface OpsBackups {
  state: OpsBackupState
  tone: OpsTone
  reason?: string
  error?: string
  current?: OpsBackupRun
  last_success?: OpsBackupRun
  last_success_at?: number
  next_expected_by?: number
  local_retention_days: number
  r2_retention_days: number
  r2_ok: boolean
  r2_verified: boolean
  running_for_seconds?: number
}

export interface OpsAlert extends MonitorAlert {
  key: string
}

export interface OpsAlertState {
  key: string
  severity: string
  title: string
  detail?: string
  phase: 'pending' | 'firing' | 'resolving'
  since: string
  fired_at?: string
  last_sent?: string
  notified: boolean
  due?: string
}

export interface OpsNotification {
  key: string
  kind: 'firing' | 'reminder' | 'resolved' | 'group' | 'test'
  severity: string
  title: string
  body: string
  at: string
  keys?: string[]
}

export interface OpsNotifications {
  channels: string[] | null
  alerts: OpsAlertState[] | null
  recent: OpsNotification[] | null
}

export interface OpsResponse {
  configured: boolean
  dir?: string
  checked_at?: number
  containers: OpsContainers
  backups?: OpsBackups
  notifications?: OpsNotifications
  alerts: OpsAlert[] | null
}
