import { apiFetch } from './api'

export type SudoStatus = 'pending' | 'approved' | 'running' | 'closed' | 'done' | 'refused' | 'expired' | 'cancelled'

export interface SudoOrigin {
  host_id: string
  host_name?: string
  session?: string
  window: number
  pane?: string
  claude_session_id?: string
  cwd?: string
  pid?: number
  unix_user?: string
}

export interface SudoRevision {
  version: number
  command: string
  by: string
  at: string
}

/** When the terminal closed; never what was shown or typed in it. */
export interface SudoSession {
  version: number
  duration: number
  reason?: string
}

export interface SudoRequest {
  id: string
  status: SudoStatus
  version: number
  command: string
  original: string
  reason?: string
  origin: SudoOrigin
  revisions: SudoRevision[]
  created_at: string
  updated_at: string
  expires_at: string
  approver?: { id: string; name: string }
  peer?: string
  session?: SudoSession
  note?: string
}

export interface SudoPeer {
  id: string
  name: string
  unix_user?: string
  online: boolean
}

export interface SudoGrant {
  grant: string
  request_id: string
  version: number
  expires_at: string
}

/** Thrown when this server does not offer the sudo terminal to this user. */
export class SudoUnavailableError extends Error {}

export const OPEN_SUDO_STATUSES: SudoStatus[] = ['pending', 'approved', 'running', 'closed']

async function sudoJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await apiFetch(`/api/sudo${path}`, { cache: 'no-store', ...init })
  if (response.status === 403 || response.status === 404) {
    const body = (await response.json().catch(() => null)) as { error?: string; code?: string } | null
    if (!body?.code) throw new SudoUnavailableError(body?.error || 'Terminal sudo indisponible')
    throw new Error(body.error || 'Demande introuvable')
  }
  if (!response.ok) {
    const body = (await response.json().catch(() => null)) as { error?: string } | null
    throw new Error(body?.error || `Erreur ${response.status}`)
  }
  return (await response.json()) as T
}

function post<T>(path: string, body: unknown): Promise<T> {
  return sudoJSON<T>(path, { method: 'POST', body: JSON.stringify(body) })
}

export const sudoApi = {
  list: () => sudoJSON<SudoRequest[]>('/requests'),
  peers: () => sudoJSON<SudoPeer[]>('/peers'),
  edit: (id: string, version: number, command: string) =>
    post<SudoRequest>(`/requests/${encodeURIComponent(id)}/edit`, { version, command }),
  refuse: (id: string, note: string) => post<SudoRequest>(`/requests/${encodeURIComponent(id)}/refuse`, { note }),
  approve: (id: string, version: number, peer: string) =>
    post<SudoGrant>(`/requests/${encodeURIComponent(id)}/approve`, { version, peer }),
  done: (id: string, note: string) => post<SudoRequest>(`/requests/${encodeURIComponent(id)}/done`, { note }),
}

/** Opens the sudo terminal. The grant travels in the first frame, never in the URL. */
export function openSudoSocket(grant: string, cols: number, rows: number): WebSocket {
  const scheme = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const socket = new WebSocket(`${scheme}://${window.location.host}/ws/sudo`)
  socket.binaryType = 'arraybuffer'
  socket.addEventListener('open', () => {
    socket.send(JSON.stringify({ type: 'open', grant, cols, rows }))
  })
  return socket
}

/** Requests of one session, open ones first. */
export function requestsForSession(requests: SudoRequest[], hostId: string, session: string): SudoRequest[] {
  return requests
    .filter(request => request.origin.host_id === hostId && request.origin.session === session)
    .sort((a, b) => {
      const openA = OPEN_SUDO_STATUSES.includes(a.status) ? 0 : 1
      const openB = OPEN_SUDO_STATUSES.includes(b.status) ? 0 : 1
      return openA - openB || b.created_at.localeCompare(a.created_at)
    })
}
