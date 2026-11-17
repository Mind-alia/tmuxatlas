// Mirrors pkg/monitor Response (GET /api/monitor).

export interface MonitorSample {
  t: number
  cpu: number
  iowait: number
  steal: number
  load1: number
  mem: number
  swap: number
  net_rx: number
  net_tx: number
  disk_r: number
  disk_w: number
  disk_util: number
  psi_cpu: number
  psi_mem: number
  psi_io: number
  tcp: number
}

export interface MonitorHost {
  hostname: string
  os: string
  kernel: string
  arch: string
  cpu_model: string
  cpus: number
  boot_time: number
  reboot_required: boolean
  updates: number
  security_updates: number
}

export interface MonitorNow {
  cpu: { user: number; system: number; iowait: number; steal: number; idle: number }
  cores: number[] | null
  load: [number, number, number]
  memory: { total: number; used: number; available: number; cached: number; swap_total: number; swap_used: number }
  uptime_seconds: number
  procs: number
  procs_running: number
  threads: number
  tcp: { established: number; time_wait: number; listen: number }
  psi: { cpu10: number; cpu60: number; mem10: number; mem60: number; io10: number; io60: number }
}

export interface MonitorFilesystem {
  mount: string
  device: string
  fstype: string
  total: number
  used: number
  available: number
  percent: number
  inodes_percent: number
  days_to_full?: number
  growth_per_day?: number
}

export interface MonitorInterface {
  name: string
  rx_rate: number
  tx_rate: number
  rx_total: number
  tx_total: number
  errors: number
  drops: number
}

export interface MonitorDisk {
  name: string
  read_rate: number
  write_rate: number
  read_iops: number
  write_iops: number
  util: number
}

export interface MonitorProcess {
  pid: number
  name: string
  user: string
  cpu: number
  rss: number
  threads: number
  unit?: string
  command: string
  started: number
}

export interface MonitorListener {
  proto: string
  address: string
  port: number
  public: boolean
  pid?: number
  process?: string
  unit?: string
}

export interface MonitorService {
  unit: string
  scope: 'system' | 'user'
  description: string
  active: string
  sub: string
  result?: string
  custom: boolean
  main_pid?: number
  memory?: number
  cpu_nanos?: number
  restarts: number
  since?: number
}

export interface MonitorTimer {
  unit: string
  scope: 'system' | 'user'
  activates: string
  custom: boolean
  next?: number
  last?: number
  result?: string
  exit_code: number
  running: boolean
}

export interface MonitorEndpoint {
  url: string
  source: string
  up: boolean
  status: number
  error?: string
  latency_ms: number
  cert_expires?: number
  cert_issuer?: string
  checked_at: number
  uptime_percent: number
  history: { t: number; up: boolean; latency_ms: number }[] | null
}

export interface MonitorLogs {
  errors_last_hour: number
  ssh_failures_24h: number
  recent_errors: { t: number; unit: string; message: string }[] | null
  error_units?: { key: string; count: number }[]
  ssh?: MonitorSSHReport
  checked_at: number
}

export interface MonitorAlert {
  severity: 'critical' | 'warning' | 'info'
  title: string
  detail?: string
  facts?: { label: string; value: string }[]
  lists?: { title: string; items: string[] }[]
  commands?: { label: string; command: string }[]
  advice?: string
}

export interface MonitorSSHReport {
  attempts: number
  unique_ips: number
  root_attempts: number
  top_ips?: { ip: string; count: number; banned: boolean }[]
  top_users?: { key: string; count: number }[]
  accepted?: { t: number; user: string; ip: string; method: string }[]
  fail2ban?: {
    currently_failed: number
    total_failed: number
    currently_banned: number
    total_banned: number
    banned?: string[]
    max_retry: number
    find_time: number
    ban_time: number
  }
  config?: Record<string, string>
}

export interface MonitorTokenCounts {
  input: number
  cache_read: number
  cache_write?: number
  output: number
  reasoning?: number
  total: number
  requests?: number
}

export interface MonitorQuotaWindow {
  label: string
  used_percent: number
  window_minutes?: number
  resets_at?: number
  expired?: boolean
}

export interface MonitorExtraUsage {
  enabled: boolean
  used?: number
  limit?: number
  used_percent?: number
  currency?: string
  limit_reached?: boolean
  disabled_reason?: string
  balance?: string
  unlimited?: boolean
}

export interface MonitorCLILiveSession {
  session: string
  command?: string
  path?: string
  attached?: boolean
}

export interface MonitorCLISessionActivity {
  id: string
  project?: string
  title?: string
  branch?: string
  model?: string
  tokens: number
  requests: number
  last_activity: number
  active?: boolean
}

export interface MonitorCLIAccount {
  id: string
  cli: 'claude' | 'codex' | 'agy' | string
  name: string
  person?: string
  /** TmuxAtlas account ID this CLI person is assigned to (admin settings). */
  owner?: string
  owner_name?: string
  account?: string
  plan?: string
  connected: boolean
  quota_source: 'provider_cache' | 'session_events' | 'local_logs_only' | string
  quota_at?: number
  last_activity?: number
  last_model?: string
  context_window?: number
  last_turn?: MonitorTokenCounts
  windows?: MonitorQuotaWindow[]
  extra?: MonitorExtraUsage
  tokens_5h: MonitorTokenCounts
  tokens_24h: MonitorTokenCounts
  tokens_7d: MonitorTokenCounts
  models_7d?: string[]
  live_sessions?: MonitorCLILiveSession[]
  recent_sessions?: MonitorCLISessionActivity[]
}

export interface MonitorCLIUser {
  id: string
  username: string
}

export interface MonitorResponse {
  supported: boolean
  interval: number
  collected_at: number
  host: MonitorHost
  now: MonitorNow
  series: MonitorSample[] | null
  filesystems: MonitorFilesystem[] | null
  interfaces: MonitorInterface[] | null
  disks: MonitorDisk[] | null
  processes: MonitorProcess[] | null
  listeners: MonitorListener[] | null
  services: MonitorService[] | null
  timers: MonitorTimer[] | null
  endpoints: MonitorEndpoint[] | null
  logs: MonitorLogs
  cli_accounts?: MonitorCLIAccount[] | null
  cli_users?: MonitorCLIUser[] | null
  quota_refreshing?: boolean
  alerts: MonitorAlert[] | null
}

export type MonitorRange = '15m' | '1h' | '6h' | '24h'

export const rangeSeconds: Record<MonitorRange, number> = {
  '15m': 15 * 60,
  '1h': 3600,
  '6h': 6 * 3600,
  '24h': 24 * 3600,
}
