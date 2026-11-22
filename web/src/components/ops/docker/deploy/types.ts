export interface DeployCommit {
  sha: string
  short: string
  subject: string
  date: string
}

export interface DeployGit {
  repo: boolean
  error?: string
  branch?: string
  detached?: boolean
  upstream?: string
  /** True when ahead and behind are known. */
  tracking: boolean
  ahead: number
  behind: number
  commit?: DeployCommit
  dirty: string[] | null
  dirty_count: number
}

export type DeployStepStatus = 'pending' | 'running' | 'done' | 'failed' | 'skipped'
export type DeployStepName = 'check' | 'fetch' | 'merge' | 'images' | 'retag' | 'build' | 'up' | 'verify'

export interface DeployStep {
  name: DeployStepName
  status: DeployStepStatus
  commands?: string[]
  started_at?: string
  duration_ms: number
  output?: string
  truncated?: boolean
  error?: string
}

export interface DeployImage {
  service: string
  container: string
  image: string
  image_id: string
  rollback_tag?: string
}

export type DeployResult = 'running' | 'success' | 'failed' | 'cancelled' | 'interrupted'

export interface DeployRecord {
  id: string
  kind: 'deploy' | 'rollback'
  stack: string
  actor: string
  started_at: string
  finished_at?: string
  result: DeployResult
  error?: string
  branch?: string
  upstream?: string
  commit_before?: DeployCommit
  commit_after?: DeployCommit
  images: DeployImage[] | null
  rollback_of?: string
  steps: DeployStep[] | null
  cancel_requested?: boolean
}

export interface DeployState {
  stack: string
  enabled: boolean
  disabled_reason?: string
  blocked?: string
  working_dir?: string
  config_files: string[] | null
  services: string[] | null
  git: DeployGit
  current?: DeployRecord
  history: DeployRecord[] | null
  rollback_id?: string
}
