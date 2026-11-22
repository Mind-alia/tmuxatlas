import { request } from '../api'
import type { DeployRecord, DeployState } from './types'

const base = (stack: string) => `/api/docker/stacks/${encodeURIComponent(stack)}/deploy`

export function fetchDeployState(stack: string, signal?: AbortSignal) {
  return request<DeployState>(base(stack), { signal })
}

export function fetchDeployRecord(stack: string, id: string, signal?: AbortSignal) {
  return request<DeployRecord>(`${base(stack)}/history/${encodeURIComponent(id)}`, { signal })
}

export function startDeploy(stack: string) {
  return request<DeployRecord>(base(stack), { method: 'POST' })
}

export function startRollback(stack: string, id: string) {
  return request<DeployRecord>(`${base(stack)}/rollback`, { method: 'POST', body: JSON.stringify({ id }) })
}

export function cancelDeploy(stack: string) {
  return request<DeployRecord>(`${base(stack)}/cancel`, { method: 'POST' })
}
