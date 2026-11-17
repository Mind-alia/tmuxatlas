import { apiFetch } from '../../../lib/api'
import type { DockerAction, DockerList, DockerLogs, DockerTerminal } from './types'

export class DockerApiError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'DockerApiError'
    this.status = status
  }
}

function capitalize(text: string) {
  return text ? text[0].toUpperCase() + text.slice(1) : text
}

async function failure(response: Response): Promise<DockerApiError> {
  let message = ''
  try {
    const text = await response.text()
    try {
      message = (JSON.parse(text) as { error?: string }).error ?? text
    } catch {
      message = text
    }
  } catch {
    /* corps illisible */
  }
  if (response.status === 401) message = 'Session expirée : reconnecte-toi.'
  if (response.status === 403 && !message) message = 'Cette action est réservée aux administrateurs.'
  return new DockerApiError(response.status, capitalize(message.trim()) || `Erreur ${response.status}`)
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  let response: Response
  try {
    response = await apiFetch(path, { cache: 'no-store', ...init })
  } catch (error) {
    if (error instanceof DOMException && error.name === 'AbortError') throw error
    throw new DockerApiError(0, 'Impossible de joindre le serveur Atlas.')
  }
  if (!response.ok) throw await failure(response)
  return (await response.json()) as T
}

const base = (id: string) => `/api/docker/containers/${encodeURIComponent(id)}`

export function fetchContainers(signal?: AbortSignal) {
  return request<DockerList>('/api/docker/containers', { signal })
}

export function runAction(id: string, action: DockerAction) {
  if (action === 'delete') return request<unknown>(base(id), { method: 'DELETE' })
  return request<unknown>(`${base(id)}/${action}`, { method: 'POST' })
}

export function fetchLogs(id: string, options: { tail?: number; since?: string }, signal?: AbortSignal) {
  const query = new URLSearchParams()
  if (options.tail !== undefined) query.set('tail', String(options.tail))
  if (options.since) query.set('since', options.since)
  return request<DockerLogs>(`${base(id)}/logs?${query}`, { signal })
}

export function fetchInspect(id: string, signal?: AbortSignal) {
  return request<unknown>(`${base(id)}/inspect`, { signal })
}

export function openTerminal(id: string) {
  return request<DockerTerminal>(`${base(id)}/terminal`, { method: 'POST' })
}
