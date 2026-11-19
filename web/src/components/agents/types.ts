export type AgentState = 'running' | 'quiet' | 'stopped' | 'done'

export interface TeamAgent {
  id: string
  parent?: string
  type: string
  description: string
  model?: string
  state: AgentState
  started_at: string
  updated_at: string
  /** Tool calls made so far. Claude Code gives no progress percentage. */
  steps: number
  action: string
  log: string[]
}

export interface AgentTeam {
  session_id: string
  name: string
  cwd: string
  tmux_session?: string
  status: string
  waiting_for?: string
  agents: TeamAgent[]
}

export interface AgentTeamsResponse {
  teams: AgentTeam[]
}
