import { apiFetch } from '../../lib/api'

export type LoopCLI = 'claude' | 'codex' | 'agy'

export type LoopStatus = 'idle' | 'running' | 'waiting_reset' | 'paused' | 'completed' | 'stopped' | 'error'

export type WorkerStatus = 'ready' | 'running' | 'paused' | 'rate_limited' | 'error' | 'disabled'

export type WorkerActionType =
  | 'pause'
  | 'resume'
  | 'enable'
  | 'disable'
  | 'retry'
  | 'wake'
  | 'enable_all'
  | 'pause_all'
  | 'resume_all'

export interface LoopWorker {
  id: string
  cli: LoopCLI
  person?: string
  label?: string
  model?: string
  enabled: boolean
  paused: boolean
  pause_requested?: boolean
  status: WorkerStatus
  rate_limited_at?: string
  reset_at?: string
  last_error?: string
  last_run_at?: string
  run_count: number
}

export interface WorkerInput {
  cli: LoopCLI
  person?: string
  label?: string
  model?: string
  enabled: boolean
  paused?: boolean
}

export interface LoopProfile {
  id: string
  provider: LoopCLI
  name: string
  person?: string
  account?: string
}

export interface ProjectEntry {
  name: string
  path: string
  is_git: boolean
  branch?: string
}

export interface CLIAvailability {
  cli: LoopCLI
  label: string
  available: boolean
  path?: string
}

export interface LoopPreset {
  id: string
  label: string
  description: string
  prompt: string
}

export interface LoopsMeta {
  host_id: string
  projects: ProjectEntry[]
  accounts: Record<LoopCLI, LoopProfile[]>
  clis: CLIAvailability[]
  subscriptions?: LoopWorker[]
  presets: LoopPreset[]
}

export interface IterationRecord {
 decision?: string
 step_id?: string
  verification?: { command: string; output: string; passed: boolean }
  result?: { problem: string; change: string; checks: string; limitations: string; next: string; decision: string }
  snapshot?: boolean
  instruction?: string

  number: number
  status: string
  worker_id?: string
  cli?: LoopCLI
  person?: string
  worker_label?: string
  started_at: string
  finished_at?: string
  duration_ms: number
  exit_code: number
  changed_files: number
  diff_stat?: string
  log_tail?: string
  done_signal?: boolean
  rate_limited?: boolean
  reset_at?: string
  error?: string
}

export interface PlanStep { id: string; title: string; done: boolean; state?: string; expected?: string; depends_on?: string[]; evidence_iteration?: number }

export interface LoopItem {
 limit_reason?: string
  phase?: string
  plan?: PlanStep[]
  time_limit_minutes?: number
  verify_command?: string
  mode?: string
  scope?: string
  criteria?: string
  memory?: string
  outcome?: string
  pending_instruction?: string

  id: string
  name: string
  project_dir: string
  work_dir: string
  branch?: string
  cli: LoopCLI
  person?: string
  model?: string
  prompt: string
  max_iterations: number
  delay_seconds: number
  auto_approve: boolean
  use_worktree: boolean
  stop_when_done: boolean
  owner_id?: string
  owner_name?: string
  status: LoopStatus
  current_iteration: number
  pause_requested?: boolean
  auto_paused?: boolean
  active_worker_id?: string
  next_reset_at?: string
  workers?: LoopWorker[]
  last_summary?: string
  last_error?: string
  live_output?: string
  created_at: string
  updated_at: string
  started_at?: string
  finished_at?: string
  iterations: IterationRecord[]
}

export interface LoopLogResponse {
  loop_id: string
  iteration: number
  content: string
  git_status?: string
  git_diff?: string
  truncated?: boolean
}

export interface ChatResponse {
  host_id: string
  session: string
  key: string
  cwd: string
  cli: LoopCLI
  person?: string
  reused?: boolean
}

async function parseJson<T>(res: Response): Promise<T> {
  const data = (await res.json().catch(() => null)) as (T & { error?: string }) | null
  if (!res.ok) {
    throw new Error(data?.error || `Erreur HTTP ${res.status}`)
  }
  return data as T
}

export async function fetchLoopsMeta(signal?: AbortSignal): Promise<LoopsMeta> {
  const res = await apiFetch('/api/loops/meta', { signal, cache: 'no-store' })
  return parseJson<LoopsMeta>(res)
}

export async function fetchLoops(signal?: AbortSignal): Promise<LoopItem[]> {
  const res = await apiFetch('/api/loops', { signal, cache: 'no-store' })
  const data = await parseJson<{ loops?: LoopItem[] }>(res)
  return data.loops ?? []
}

export async function fetchLoopLogs(id: string, iter?: number, signal?: AbortSignal): Promise<LoopLogResponse> {
  const params = new URLSearchParams({ id })
  if (iter && iter > 0) params.set('iter', String(iter))
  const res = await apiFetch(`/api/loops/logs?${params}`, { signal, cache: 'no-store' })
  return parseJson<LoopLogResponse>(res)
}

async function postJson<T>(url: string, body: Record<string, unknown>): Promise<T> {
  const res = await apiFetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  return parseJson<T>(res)
}

export function createLoop(payload: {
  time_limit_minutes?: number
  verify_command?: string
  mode?: string
  scope?: string
  criteria?: string

  name?: string
  project_dir: string
  cli?: LoopCLI
  person?: string
  model?: string
  workers?: WorkerInput[]
  prompt: string
  max_iterations: number
  continuous?: boolean
  delay_seconds: number
  auto_approve: boolean
  use_worktree: boolean
  stop_when_done: boolean
  start_now: boolean
}) {
  return postJson<LoopItem>('/api/loops/create', payload)
}

export function updateLoop(payload: {
  time_limit_minutes?: number
  verify_command?: string
  mode?: string
  scope?: string
  criteria?: string

  id: string
  name?: string
  cli?: LoopCLI
  person?: string
  model?: string
  workers?: WorkerInput[]
  prompt: string
  max_iterations: number
  continuous?: boolean
  delay_seconds: number
  auto_approve: boolean
  stop_when_done: boolean
}) {
  return postJson<LoopItem>('/api/loops/update', payload)
}

export function deleteLoop(id: string) {
  return postJson<{ ok: boolean }>('/api/loops/delete', { id })
}

export function startLoop(id: string) {
  return postJson<LoopItem>('/api/loops/start', { id })
}

export function pauseLoop(id: string) {
  return postJson<LoopItem>('/api/loops/pause', { id })
}

export function stopLoop(id: string) {
  return postJson<LoopItem>('/api/loops/stop', { id })
}

export function workerAction(payload: {
  loop_id: string
  worker_id?: string
  action: WorkerActionType
  cli?: LoopCLI
  person?: string
  label?: string
  model?: string
}) {
  return postJson<LoopItem>('/api/loops/worker', payload)
}

export function openCliChat(payload: {
  cli: LoopCLI
  cwd: string
  person?: string
  model?: string
  session?: string
  prompt?: string
  reuse?: boolean
}) {
  return postJson<ChatResponse>('/api/loops/chat', payload)
}

export function queueInstruction(id: string, text: string) { return postJson<LoopItem>("/api/loops/instruction", { id, text }) }

export function reviewLoop(id: string) { return postJson<LoopItem>("/api/loops/review", { id }) }

export function saveLoopPlan(id: string, plan: PlanStep[]) { return postJson<LoopItem>("/api/loops/plan", {id, plan}) }
